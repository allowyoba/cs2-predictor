package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

func TestPollHashtag_NormalizesReadableNames(t *testing.T) {
	cases := map[string]string{
		"Team Spirit":                     "#TeamSpirit",
		"FISSURE Playground 2":            "#FISSUREPlayground2",
		"CCT 2026: Challengers Europe #6": "#CCT2026ChallengersEurope6",
		"NAVI Junior / Academy":           "#NAVIJuniorAcademy",
		"  Спирит Академия  ":             "#СпиритАкадемия",
	}
	for in, want := range cases {
		if got := pollHashtag(in); got != want {
			t.Fatalf("pollHashtag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppendPollHashtags_IncludesTournamentAndTeamsWithinTelegramLimit(t *testing.T) {
	first := &competition.Team{Name: "Team Spirit"}
	second := &competition.Team{Name: "Team Vitality"}
	got := appendPollHashtags(strings.Repeat("матч ", 100), "FISSURE Playground 2", first, second)

	for _, want := range []string{"#FISSUREPlayground2", "#TeamSpirit", "#TeamVitality"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if n := len([]rune(got)); n > telegramPollQuestionLimit {
		t.Fatalf("question has %d runes, limit is %d", n, telegramPollQuestionLimit)
	}
}

func TestAppendPollHashtags_DeduplicatesSameTeam(t *testing.T) {
	team := &competition.Team{Name: "Spirit"}
	got := appendPollHashtags("Q", "Spirit", team, team)
	if strings.Count(strings.ToLower(got), "#spirit") != 1 {
		t.Fatalf("expected one #Spirit hashtag, got %q", got)
	}
}

func TestFormatVRSCombined_BothTeamsRankedAndPointed(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank, secondRank := 1, 3
	firstPoints, secondPoints := 1993, 1908
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first:  {TeamID: first, GlobalRank: &firstRank, Points: &firstPoints},
		second: {TeamID: second, GlobalRank: &secondRank, Points: &secondPoints},
	}
	got := formatVRSCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#1(1993) — #3(1908)"; got != want {
		t.Fatalf("formatVRSCombined = %q, want %q", got, want)
	}
}

func TestFormatVRSCombined_RankOnlyOmitsParentheses(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank, secondRank := 1, 3
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first:  {TeamID: first, GlobalRank: &firstRank},
		second: {TeamID: second, GlobalRank: &secondRank},
	}
	got := formatVRSCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#1 — #3"; got != want {
		t.Fatalf("formatVRSCombined = %q, want %q", got, want)
	}
}

func TestFormatVRSCombined_OneTeamUnrankedShowsDash(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank := 1
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first: {TeamID: first, GlobalRank: &firstRank},
	}
	got := formatVRSCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#1 — —"; got != want {
		t.Fatalf("formatVRSCombined = %q, want %q", got, want)
	}
}

func TestFormatVRSCombined_NeitherTeamRankedOmitsLine(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	if got := formatVRSCombined(nil, &competition.Team{ID: first}, &competition.Team{ID: second}); got != "" {
		t.Fatalf("formatVRSCombined = %q, want empty (line omitted)", got)
	}
}

func TestFormatVRSCombined_NilTeamOmitsLine(t *testing.T) {
	rank := 1
	teamID := common.NewTeamID()
	rankings := map[common.TeamID]enrichment.TeamRanking{teamID: {TeamID: teamID, GlobalRank: &rank}}
	if got := formatVRSCombined(rankings, &competition.Team{ID: teamID}, nil); got != "" {
		t.Fatalf("formatVRSCombined = %q, want empty when a side of the match is unknown", got)
	}
}

// fakeRankingRepository is a minimal enrichment.RankingRepository — only
// FindRankings is exercised by PollGateway.Send.
type fakeRankingRepository struct {
	rankings map[common.TeamID]enrichment.TeamRanking
	err      error
}

func (f *fakeRankingRepository) SaveRanking(context.Context, enrichment.TeamRanking) error {
	return nil
}
func (f *fakeRankingRepository) FindRanking(context.Context, common.TeamID, enrichment.Source) (*enrichment.TeamRanking, error) {
	return nil, nil
}
func (f *fakeRankingRepository) FindRankings(_ context.Context, teamIDs []common.TeamID, _ enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[common.TeamID]enrichment.TeamRanking{}
	for _, id := range teamIDs {
		if r, ok := f.rankings[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// sendTestPoll wires a real PollGateway against a recording HTTP server and
// returns the question text of the resulting sendPoll call.
func sendTestPoll(t *testing.T, enrichmentSources PollEnrichmentSources, firstTeam, secondTeam competition.Team) string {
	t.Helper()
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	client := NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	eventID := common.NewEventID()
	format, err := competition.NewSeriesFormat(competition.BestOf, 3)
	if err != nil {
		t.Fatal(err)
	}
	scheduledAt := time.Now().Add(time.Hour)
	matchID := common.NewMatchID()
	match := competition.Match{
		ID: matchID, EventID: eventID, Format: format, Status: competition.MatchNotStarted,
		FirstTeam: &firstTeam, SecondTeam: &secondTeam, ScheduledAt: &scheduledAt,
	}
	catalog := &dataCatalog{
		events:           map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}},
		unstartedMatches: map[common.EventID][]competition.Match{eventID: {match}},
	}

	score, err := competition.NewMatchScore(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: matchID,
		Options: []prediction.Option{{Index: 0, Score: score}},
		Status:  prediction.PollOpen, ClosesAt: scheduledAt,
	}

	gateway := NewPollGateway(client, catalog, chats, texts, slog.Default(), enrichmentSources)
	if _, err := gateway.Send(context.Background(), poll); err != nil {
		t.Fatal(err)
	}

	for _, c := range *calls {
		if c["__method"] == "sendPoll" {
			q, _ := c["question"].(string)
			return q
		}
	}
	t.Fatal("no sendPoll call recorded")
	return ""
}

func TestSend_IncludesVRSLineWhenBothTeamsAreRanked(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}

	question := sendTestPoll(t, PollEnrichmentSources{Rankings: rankings}, first, second)
	if !strings.Contains(question, "VRS #1 — #3") {
		t.Fatalf("expected VRS line in question, got %q", question)
	}
}

func TestSend_OmitsVRSLineWhenNeitherTeamIsRanked(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	rankings := &fakeRankingRepository{}

	question := sendTestPoll(t, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(question, "VRS") {
		t.Fatalf("expected no VRS line when neither team is ranked, got %q", question)
	}
}

func TestSend_OmitsVRSLineWhenRankingsIsNil(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}

	question := sendTestPoll(t, PollEnrichmentSources{}, first, second)
	if strings.Contains(question, "VRS") {
		t.Fatalf("expected no VRS line when rankings is nil, got %q", question)
	}
}

func TestSend_OmitsVRSLineWhenLookupFails(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	rankings := &fakeRankingRepository{err: errors.New("ranking cache unavailable")}

	question := sendTestPoll(t, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(question, "VRS") {
		t.Fatalf("expected no VRS line when the cache lookup errors, got %q", question)
	}
}

func TestFormatForm_BothTeamsHaveForm(t *testing.T) {
	home := &enrichment.RecentForm{Wins: 4, Losses: 1}
	away := &enrichment.RecentForm{Wins: 3, Losses: 2}
	if got, want := formatForm(home, away), "4-1 — 3-2"; got != want {
		t.Fatalf("formatForm = %q, want %q", got, want)
	}
}

func TestFormatForm_OneTeamMissingShowsDash(t *testing.T) {
	home := &enrichment.RecentForm{Wins: 4, Losses: 1}
	if got, want := formatForm(home, nil), "4-1 — —"; got != want {
		t.Fatalf("formatForm = %q, want %q", got, want)
	}
}

func TestFormatForm_NeitherTeamOmitsLine(t *testing.T) {
	if got := formatForm(nil, nil); got != "" {
		t.Fatalf("formatForm = %q, want empty", got)
	}
}

func TestFormatH2H_RendersTeamAAndTeamBWins(t *testing.T) {
	h2h := &enrichment.HeadToHead{TeamAWins: 6, TeamBWins: 4, Sample: 10}
	if got, want := formatH2H(h2h), "6-4"; got != want {
		t.Fatalf("formatH2H = %q, want %q", got, want)
	}
}

func TestFormatH2H_NilOrZeroSampleOmitsLine(t *testing.T) {
	if got := formatH2H(nil); got != "" {
		t.Fatalf("formatH2H(nil) = %q, want empty", got)
	}
	if got := formatH2H(&enrichment.HeadToHead{Sample: 0}); got != "" {
		t.Fatalf("formatH2H(zero-sample) = %q, want empty", got)
	}
}

// fakeFormRepository is a minimal enrichment.FormRepository — only FindForm
// is exercised by PollGateway.Send.
type fakeFormRepository struct {
	forms map[common.TeamID]enrichment.RecentForm
	err   error
}

func (f *fakeFormRepository) SaveForm(context.Context, common.TeamID, enrichment.RecentForm) error {
	return nil
}
func (f *fakeFormRepository) FindForm(_ context.Context, teamID common.TeamID, _ enrichment.Source) (*enrichment.RecentForm, error) {
	if f.err != nil {
		return nil, f.err
	}
	if form, ok := f.forms[teamID]; ok {
		return &form, nil
	}
	return nil, nil
}

// fakeHeadToHeadRepository is a minimal enrichment.HeadToHeadRepository —
// only FindHeadToHead is exercised by PollGateway.Send.
type fakeHeadToHeadRepository struct {
	h2h *enrichment.HeadToHead
	err error
}

func (f *fakeHeadToHeadRepository) SaveHeadToHead(context.Context, common.TeamID, common.TeamID, enrichment.HeadToHead) error {
	return nil
}
func (f *fakeHeadToHeadRepository) FindHeadToHead(context.Context, common.TeamID, common.TeamID, enrichment.Source) (*enrichment.HeadToHead, error) {
	return f.h2h, f.err
}

func TestSend_IncludesFormAndH2HLinesWhenCached(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	form := &fakeFormRepository{forms: map[common.TeamID]enrichment.RecentForm{
		first.ID:  {Wins: 4, Losses: 1},
		second.ID: {Wins: 3, Losses: 2},
	}}
	h2h := &fakeHeadToHeadRepository{h2h: &enrichment.HeadToHead{TeamAWins: 6, TeamBWins: 4, Sample: 10}}

	question := sendTestPoll(t, PollEnrichmentSources{Form: form, H2H: h2h}, first, second)
	if !strings.Contains(question, "4-1 — 3-2") {
		t.Fatalf("expected form line in question, got %q", question)
	}
	if !strings.Contains(question, "6-4") {
		t.Fatalf("expected H2H line in question, got %q", question)
	}
}

func TestSend_OrdersEnrichmentLinesByPriority(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	firstPoints, secondPoints := 1993, 1908
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank, Points: &firstPoints},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank, Points: &secondPoints},
	}}
	form := &fakeFormRepository{forms: map[common.TeamID]enrichment.RecentForm{
		first.ID:  {Wins: 4, Losses: 1},
		second.ID: {Wins: 3, Losses: 2},
	}}
	h2h := &fakeHeadToHeadRepository{h2h: &enrichment.HeadToHead{TeamAWins: 6, TeamBWins: 4, Sample: 10}}

	question := sendTestPoll(t, PollEnrichmentSources{Rankings: rankings, Form: form, H2H: h2h}, first, second)

	rankIdx := strings.Index(question, "#1(1993) — #3(1908)")
	formIdx := strings.Index(question, "4-1 — 3-2")
	h2hIdx := strings.Index(question, "6-4")
	if rankIdx == -1 || formIdx == -1 || h2hIdx == -1 {
		t.Fatalf("expected all three enrichment lines present, got %q", question)
	}
	if rankIdx >= formIdx || formIdx >= h2hIdx {
		t.Fatalf("expected enrichment line order VRS < form < H2H, got %q", question)
	}
}

// A long event name plus long team names plus a full set of insight lines
// pushes past Telegram's poll-question limit. The lines must then be
// dropped whole, from the lowest priority up, rather than the whole
// question being truncated through the middle of one.
func TestComposePollQuestion_DropsWholeInsightLinesInsteadOfCuttingThem(t *testing.T) {
	header := "🏆 " + strings.Repeat("Tournament ", 18) + "\n\n⚔️ Spirit — NAVI"
	insights := []string{
		ru(t, "poll.balance", "4-1", "3-2"),
		ru(t, "poll.vrs", "#1(1993) — #3(1908)"),
		ru(t, "poll.form", "4-1 — 3-2"),
		ru(t, "poll.h2h", "6-4"),
	}
	first := &competition.Team{Name: "Team Spirit"}
	second := &competition.Team{Name: "Natus Vincere"}

	question, dropped := composePollQuestion(header, insights, "Very Long Tournament Name 2026", first, second)

	if n := len([]rune(question)); n > telegramPollQuestionLimit {
		t.Fatalf("question is %d runes, over the %d limit", n, telegramPollQuestionLimit)
	}
	if dropped == 0 {
		t.Fatal("expected some insight lines to be dropped for this oversized input")
	}
	// Whatever survived must have survived intact — no half lines.
	for i, line := range insights {
		if strings.Contains(question, line) {
			continue
		}
		// Once one line is gone, every lower-priority line must be gone too,
		// and no fragment of it may remain.
		for _, later := range insights[i:] {
			if strings.Contains(question, later) {
				t.Fatalf("line %q was dropped but lower-priority %q survived", line, later)
			}
			for cut := len([]rune(later)) - 1; cut > 4; cut-- {
				if frag := string([]rune(later)[:cut]); strings.Contains(question, frag) {
					t.Fatalf("found a truncated fragment of a dropped line: %q", frag)
				}
			}
		}
		break
	}
	// The hashtags identify the match and must always survive.
	if !strings.Contains(question, "#TeamSpirit") {
		t.Fatalf("hashtags must survive the budget squeeze, got %q", question)
	}
}

func TestComposePollQuestion_KeepsEverythingWhenItFits(t *testing.T) {
	header := "🏆 Major\n\n⚔️ Spirit — NAVI"
	insights := []string{"🌍 VRS #1 — #3", "⚔️ H2H 6-4"}
	question, dropped := composePollQuestion(header, insights, "Major", &competition.Team{Name: "Spirit"}, &competition.Team{Name: "NAVI"})

	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0 — this input fits comfortably", dropped)
	}
	for _, line := range insights {
		if !strings.Contains(question, line) {
			t.Fatalf("missing insight line %q in %q", line, question)
		}
	}
}
