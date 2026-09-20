package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cs2predictor/internal/domain/competition"
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

func TestMiniappTeams_ServesTheProvidersCrestByDefaultAndHLTVOnRequest(t *testing.T) {
	teams := &stubTeams{teams: []competition.Team{
		{ID: common.NewTeamID(), Name: "Vitality", Location: "FR",
			LogoURL: "https://cdn-api.pandascore.co/vitality.png", HLTVLogoURL: "https://img-cdn.hltv.org/vitality.png"},
		// A team HLTV's ranking has never listed — most of them.
		{ID: common.NewTeamID(), Name: "Qualifier Five", LogoURL: "https://cdn-api.pandascore.co/five.png"},
	}}
	handler := teamsHandler(teams)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=CS2", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeTeams(t, recorder.Body.Bytes())
	if teams.game != competition.GameCS2 {
		t.Fatalf("asked the catalogue for %q, want CS2 — the code is case-insensitive", teams.game)
	}
	if body.LogoSrc != "provider" || body.Teams[0].Logo != "https://cdn-api.pandascore.co/vitality.png" {
		t.Fatalf("default must be the provider's crest, got %+v", body.Teams[0])
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2&logos=hltv", nil))
	body = decodeTeams(t, recorder.Body.Bytes())
	if body.LogoSrc != "hltv" || body.Teams[0].Logo != "https://img-cdn.hltv.org/vitality.png" {
		t.Fatalf("asked for HLTV, got %+v", body.Teams[0])
	}
	// HLTV lists thirty teams; everybody else still needs a crest, so the
	// preference falls back rather than blanking them.
	if body.Teams[1].Logo != "https://cdn-api.pandascore.co/five.png" {
		t.Fatalf("a team HLTV never ranked must keep the provider's crest, got %+v", body.Teams[1])
	}
	// Both raw sources travel with the row, so a client can offer the same
	// switch without asking again.
	if body.Teams[0].LogoProvider == "" || body.Teams[0].LogoHLTV == "" {
		t.Fatalf("expected both sources on the row, got %+v", body.Teams[0])
	}
}

func TestMiniappTeams_RefusesAGameItDoesNotFollow(t *testing.T) {
	handler := teamsHandler(&stubTeams{})
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
	handler := teamsHandler(teams)

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
	handler := teamsHandler(&stubTeams{err: errors.New("database down")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}

	// Not wired at all is a different answer from broken: a deployment
	// without the catalogue says so instead of 404-ing a path that exists.
	recorder = httptest.NewRecorder()
	teamsHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/teams?game=cs2", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}
