package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"strings"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// The Mini App's team endpoint. What it serves is public by nature — a
// team's name and crest are the same for everybody — which is what lets it
// skip the initData handshake and be cached. These cover the two things
// that must not drift: the crest chosen for a request, and the refusal of
// anything it was not asked about.

type stubTeams struct {
	teams []competition.Team
	err   error
	game  competition.GameCode
	limit int
}

func (s *stubTeams) TeamsForGame(_ context.Context, game competition.GameCode, limit int) ([]competition.Team, error) {
	s.game, s.limit = game, limit
	return s.teams, s.err
}

func decodeTeams(t *testing.T, body []byte) teamsResponse {
	t.Helper()
	var parsed teamsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode: %v — body %s", err, body)
	}
	return parsed
}

// stubLogos stands for the mirrored crests. Only digests matter here: the
// endpoint builds URLs, it does not serve bytes.
type stubLogos struct {
	digests map[common.TeamID]map[enrichment.Source]string
	chips   map[common.TeamID]map[enrichment.Source]bool
}

func (s *stubLogos) LogoChips(context.Context) (map[common.TeamID]map[enrichment.Source]bool, error) {
	return s.chips, nil
}

func (s *stubLogos) FindLogo(context.Context, common.TeamID, enrichment.Source) (*enrichment.TeamLogo, error) {
	return nil, nil
}

func (s *stubLogos) LogoDigests(context.Context) (map[common.TeamID]map[enrichment.Source]string, error) {
	return s.digests, nil
}

// Every crest URL the app is handed points back at this bot. The whole
// reason the images are mirrored is that a viewer's browser must never
// fetch one from HLTV's or PandaScore's CDN, and an endpoint that hands
// out their URLs would undo that on its own.
func TestMiniappTeams_ServesCrestsFromThisOriginNeverTheProvidersCDN(t *testing.T) {
	vitality := common.NewTeamID()
	qualifier := common.NewTeamID()
	teams := &stubTeams{teams: []competition.Team{
		{ID: vitality, Name: "Vitality", Location: "FR", HLTVLocation: "DE",
			LogoURL: "https://cdn-api.pandascore.co/vitality.png", HLTVLogoURL: "https://img-cdn.hltv.org/vitality.png"},
		// A team HLTV's ranking has never listed — most of them.
		{ID: qualifier, Name: "Qualifier Five", LogoURL: "https://cdn-api.pandascore.co/five.png"},
	}}
	logos := &stubLogos{
		digests: map[common.TeamID]map[enrichment.Source]string{
			vitality:  {"PANDASCORE": "aaaa1111", enrichment.SourceHLTV: "bbbb2222"},
			qualifier: {"PANDASCORE": "cccc3333"},
		},
		// A light mark needs a dark chip behind it; the unmeasured one
		// gets no answer rather than a guessed one.
		chips: map[common.TeamID]map[enrichment.Source]bool{
			vitality: {"PANDASCORE": true},
		},
	}
	handler := teamsHandler(teams, logos)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=CS2", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeTeams(t, recorder.Body.Bytes())
	if teams.game != competition.GameCS2 {
		t.Fatalf("asked the catalogue for %q, want CS2 — the code is case-insensitive", teams.game)
	}

	// Nothing anywhere in the payload may name a third-party host.
	raw := recorder.Body.String()
	for _, host := range []string{"pandascore.co", "hltv.org"} {
		if strings.Contains(raw, host) {
			t.Fatalf("the payload hands out %s URLs, which is the traffic the mirror exists to stop: %s", host, raw)
		}
	}

	if body.LogoSrc != "provider" {
		t.Fatalf("default source = %q, want the provider's", body.LogoSrc)
	}
	wantProvider := "/api/miniapp/v1/teams/" + vitality.Value.String() + "/logo?src=provider&v=aaaa1111"
	if body.Teams[0].Logo != wantProvider {
		t.Fatalf("crest = %q, want %q", body.Teams[0].Logo, wantProvider)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2&logos=hltv", nil))
	body = decodeTeams(t, recorder.Body.Bytes())
	wantHLTV := "/api/miniapp/v1/teams/" + vitality.Value.String() + "/logo?src=hltv&v=bbbb2222"
	if body.LogoSrc != "hltv" || body.Teams[0].Logo != wantHLTV {
		t.Fatalf("asked for HLTV, got %+v", body.Teams[0])
	}
	// The flag follows the same preference as the crest.
	if body.Teams[0].Location != "DE" {
		t.Fatalf("location = %q, want HLTV's answer when HLTV was asked for", body.Teams[0].Location)
	}
	// HLTV lists thirty teams; everybody else still needs a crest, so the
	// preference falls back rather than blanking them.
	if !strings.Contains(body.Teams[1].Logo, "src=provider&v=cccc3333") {
		t.Fatalf("a team HLTV never ranked must keep the provider's crest, got %+v", body.Teams[1])
	}
	// Both sources travel with the row, so a client can offer the same
	// switch without asking again — as our URLs, not theirs.
	if body.Teams[0].LogoProvider == "" || body.Teams[0].LogoHLTV == "" {
		t.Fatalf("expected both sources on the row, got %+v", body.Teams[0])
	}
}

// No one background shows a white wordmark and a black one equally well,
// so the chip is decided per crest — and left undecided rather than
// guessed when the picture could not be measured.
func TestMiniappTeams_TellsTheAppWhichChipToDrawBehindEachCrest(t *testing.T) {
	light := common.NewTeamID()
	unmeasured := common.NewTeamID()
	teams := &stubTeams{teams: []competition.Team{
		{ID: light, Name: "Vitality", LogoURL: "https://cdn-api.pandascore.co/v.png"},
		{ID: unmeasured, Name: "Svg Team", LogoURL: "https://cdn-api.pandascore.co/s.svg"},
	}}
	logos := &stubLogos{
		digests: map[common.TeamID]map[enrichment.Source]string{
			light:      {"PANDASCORE": "aaaa1111"},
			unmeasured: {"PANDASCORE": "bbbb2222"},
		},
		chips: map[common.TeamID]map[enrichment.Source]bool{light: {"PANDASCORE": true}},
	}

	recorder := httptest.NewRecorder()
	teamsHandler(teams, logos).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))

	body := decodeTeams(t, recorder.Body.Bytes())
	if body.Teams[0].Chip != "dark" {
		t.Fatalf("chip = %q, want a dark chip behind a light mark", body.Teams[0].Chip)
	}
	if body.Teams[1].Chip != "" {
		t.Fatalf("chip = %q, want no answer for a crest nothing could measure", body.Teams[1].Chip)
	}
}

// Nothing mirrored yet is not a broken board: the app draws initials for a
// team with no crest, which is already what it does for teams nobody
// published one for.
func TestMiniappTeams_LeavesCrestsEmptyUntilTheyAreMirrored(t *testing.T) {
	teams := &stubTeams{teams: []competition.Team{
		{ID: common.NewTeamID(), Name: "Vitality", LogoURL: "https://cdn-api.pandascore.co/vitality.png"},
	}}
	recorder := httptest.NewRecorder()
	teamsHandler(teams, &stubLogos{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))

	body := decodeTeams(t, recorder.Body.Bytes())
	if body.Teams[0].Logo != "" {
		t.Fatalf("crest = %q, want empty until the mirror has the bytes", body.Teams[0].Logo)
	}
	if strings.Contains(recorder.Body.String(), "pandascore.co") {
		t.Fatal("falling back to the provider's own URL is exactly the traffic being avoided")
	}
}

func TestMiniappTeams_RefusesAGameItDoesNotFollow(t *testing.T) {
	handler := teamsHandler(&stubTeams{}, nil)
	for _, query := range []string{"", "?game=", "?game=chess", "?game=cs2&limit=0", "?game=cs2&limit=nope"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%q: status = %d, want 400 — an empty list would read as 'this game has no teams'", query, recorder.Code)
		}
	}
}

// The cap is the endpoint's own, not the caller's to raise.
func TestMiniappTeams_CapsTheListWhateverWasAskedFor(t *testing.T) {
	teams := &stubTeams{}
	handler := teamsHandler(teams, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2&limit=100000", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if teams.limit != miniappTeamsLimit {
		t.Fatalf("limit = %d, want it capped at %d", teams.limit, miniappTeamsLimit)
	}
}

func TestMiniappTeams_ReportsFailuresRatherThanAnEmptyBoard(t *testing.T) {
	handler := teamsHandler(&stubTeams{err: errors.New("database down")}, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	// Not wired at all is a different answer from broken: a deployment
	// without the catalogue says so instead of 404-ing a path that exists.
	recorder = httptest.NewRecorder()
	teamsHandler(nil, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}
