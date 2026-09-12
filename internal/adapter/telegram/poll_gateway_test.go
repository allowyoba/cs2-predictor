package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// --- composePollQuestion / formatTeamRecord ---

func TestComposePollQuestion_BothRecords(t *testing.T) {
	got := composePollQuestion("G2", "2–1", "5star", "1–1")
	if want := "G2 (2–1) · 5star (1–1)"; got != want {
		t.Fatalf("composePollQuestion = %q, want %q", got, want)
	}
}

func TestComposePollQuestion_OneRecordMissingOmitsItsParentheses(t *testing.T) {
	if got, want := composePollQuestion("G2", "", "5star", "1–1"), "G2 · 5star (1–1)"; got != want {
		t.Fatalf("composePollQuestion = %q, want %q", got, want)
	}
	if got, want := composePollQuestion("G2", "2–1", "5star", ""), "G2 (2–1) · 5star"; got != want {
		t.Fatalf("composePollQuestion = %q, want %q", got, want)
	}
}

func TestComposePollQuestion_BothRecordsMissing(t *testing.T) {
	if got, want := composePollQuestion("G2", "", "5star", ""), "G2 · 5star"; got != want {
		t.Fatalf("composePollQuestion = %q, want %q", got, want)
	}
}

func TestComposePollQuestion_TruncatesExtremelyLongNames(t *testing.T) {
	long := strings.Repeat("Very Long Team Name ", 20)
	got := composePollQuestion(long, "2–1", long, "1–1")
	if n := len([]rune(got)); n > telegramPollQuestionLimit {
		t.Fatalf("question is %d runes, over the %d limit", n, telegramPollQuestionLimit)
	}
}

func TestFormatTeamRecord(t *testing.T) {
	if got, want := formatTeamRecord(competition.TeamBalance{Wins: 2, Losses: 1}), "2–1"; got != want {
		t.Fatalf("formatTeamRecord = %q, want %q", got, want)
	}
	if got, want := formatTeamRecord(competition.TeamBalance{Wins: 2, Losses: 1, Draws: 1}), "2–1–1"; got != want {
		t.Fatalf("formatTeamRecord (with a draw) = %q, want %q", got, want)
	}
	if got := formatTeamRecord(competition.TeamBalance{}); got != "" {
		t.Fatalf("formatTeamRecord (no games) = %q, want empty", got)
	}
}

// --- composePollDescription ---

func TestComposePollDescription_FullExampleMatchesSpec(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "FISSURE PLAYGROUND Season 3 2026", "Group A", "BO3", "10.09", "15:00 MSK", "#8 (1723) · #121 (867)", "", "", "")
	want := "🏆 FISSURE PLAYGROUND Season 3 2026\n" +
		"🎯 Group A · BO3\n" +
		"🕒 10.09 · 15:00 MSK\n" +
		"📈 VRS: #8 (1723) · #121 (867)"
	if got != want {
		t.Fatalf("composePollDescription = %q, want %q", got, want)
	}
}

func TestComposePollDescription_MissingStageKeepsOnlyFormat(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "", "BO3", "", "", "", "", "", "")
	if want := "🏆 Major\n🎯 BO3"; got != want {
		t.Fatalf("composePollDescription = %q, want %q", got, want)
	}
}

func TestComposePollDescription_MissingFormatKeepsOnlyStage(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "Group A", "", "", "", "", "", "", "")
	if want := "🏆 Major\n🎯 Group A"; got != want {
		t.Fatalf("composePollDescription = %q, want %q", got, want)
	}
}

func TestComposePollDescription_MissingStageAndFormatOmitsTheWholeLine(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "", "", "", "", "", "", "", "")
	if strings.Contains(got, "🎯") {
		t.Fatalf("expected no 🎯 line when both stage and format are missing, got %q", got)
	}
}

func TestComposePollDescription_MissingTournamentOmitsItsLine(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "", "Group A", "BO3", "", "", "", "", "", "")
	if strings.Contains(got, "🏆") {
		t.Fatalf("expected no 🏆 line when the tournament name is empty, got %q", got)
	}
}

func TestComposePollDescription_MissingDateTimeOmitsItsLine(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "", "", "", "", "", "", "", "")
	if strings.Contains(got, "🕒") {
		t.Fatalf("expected no 🕒 line when date/time are both empty, got %q", got)
	}
}

func TestComposePollDescription_MissingVRSOmitsItsLine(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "", "", "", "", "", "", "", "")
	if strings.Contains(got, "VRS") {
		t.Fatalf("expected no VRS line when the caller passed an empty vrsLine, got %q", got)
	}
}

func TestComposePollDescription_EverythingEmptyProducesEmptyString(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	if got := composePollDescription(texts, common.LocaleRU, "", "", "", "", "", "", "", "", ""); got != "" {
		t.Fatalf("composePollDescription = %q, want empty", got)
	}
}

func TestComposePollDescription_IncludesFormAndH2HAfterVRS(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "", "", "", "", "", "#1 (1993) · #3 (1908)", "", "4–1 · 3–2", "6–4")
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected exactly 3 lines (VRS, form, H2H), got %v", lines)
	}
	if !strings.Contains(lines[0], "VRS") || !strings.Contains(lines[1], "4–1") || !strings.Contains(lines[2], "6–4") {
		t.Fatalf("unexpected line order/content: %v", lines)
	}
}

// No leading/trailing blank lines, and no blank line ever appears between
// two present blocks — every "\n" in the result separates two real lines.
func TestComposePollDescription_NoBlankLines(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	got := composePollDescription(texts, common.LocaleRU, "Major", "Group A", "BO3", "10.09", "15:00 MSK", "#1 (1993) · #3 (1908)", "", "4–1 · 3–2", "6–4")
	if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
		t.Fatalf("expected no leading/trailing newline, got %q", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("expected no blank line between blocks, got %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("expected no blank line in %q", got)
		}
	}
}

// --- formatRankingCombined / formatRankingSide ---

func TestFormatRankingCombined_BothTeamsRankedAndPointed(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank, secondRank := 8, 121
	firstPoints, secondPoints := 1723, 867
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first:  {TeamID: first, GlobalRank: &firstRank, Points: &firstPoints},
		second: {TeamID: second, GlobalRank: &secondRank, Points: &secondPoints},
	}
	got := formatRankingCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#8 (1723) · #121 (867)"; got != want {
		t.Fatalf("formatRankingCombined = %q, want %q", got, want)
	}
}

func TestFormatRankingCombined_RankOnlyOmitsParentheses(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank, secondRank := 1, 3
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first:  {TeamID: first, GlobalRank: &firstRank},
		second: {TeamID: second, GlobalRank: &secondRank},
	}
	got := formatRankingCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#1 · #3"; got != want {
		t.Fatalf("formatRankingCombined = %q, want %q", got, want)
	}
}

// If only one team has cached VRS data, the other side must show "N/A"
// rather than being dropped — a bare "#1" with no second value would leave
// the reader unable to tell which team it's for.
func TestFormatRankingCombined_OneTeamUnrankedShowsNAForTheOtherSide(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	firstRank := 1
	rankings := map[common.TeamID]enrichment.TeamRanking{
		first: {TeamID: first, GlobalRank: &firstRank},
	}
	got := formatRankingCombined(rankings, &competition.Team{ID: first}, &competition.Team{ID: second})
	if want := "#1 · N/A"; got != want {
		t.Fatalf("formatRankingCombined = %q, want %q", got, want)
	}
	if strings.Contains(got, "#0") {
		t.Fatalf("expected no #0 placeholder in %q", got)
	}
}

func TestFormatRankingCombined_NeitherTeamRankedOmitsLine(t *testing.T) {
	first, second := common.NewTeamID(), common.NewTeamID()
	if got := formatRankingCombined(nil, &competition.Team{ID: first}, &competition.Team{ID: second}); got != "" {
		t.Fatalf("formatRankingCombined = %q, want empty (line omitted)", got)
	}
}

func TestFormatRankingCombined_NilTeamOmitsLine(t *testing.T) {
	rank := 1
	teamID := common.NewTeamID()
	rankings := map[common.TeamID]enrichment.TeamRanking{teamID: {TeamID: teamID, GlobalRank: &rank}}
	if got := formatRankingCombined(rankings, &competition.Team{ID: teamID}, nil); got != "" {
		t.Fatalf("formatRankingCombined = %q, want empty when a side of the match is unknown", got)
	}
}

// --- formatForm / formatH2H ---

func TestFormatForm_BothTeamsHaveForm(t *testing.T) {
	home := &enrichment.RecentForm{Wins: 4, Losses: 1}
	away := &enrichment.RecentForm{Wins: 3, Losses: 2}
	if got, want := formatForm(home, away), "4–1 · 3–2"; got != want {
		t.Fatalf("formatForm = %q, want %q", got, want)
	}
}

func TestFormatForm_OneTeamMissingShowsOnlyTheOther(t *testing.T) {
	home := &enrichment.RecentForm{Wins: 4, Losses: 1}
	if got, want := formatForm(home, nil), "4–1"; got != want {
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
	if got, want := formatH2H(h2h), "6–4"; got != want {
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

// --- Send() integration tests: exercise the real HTTP payload ---

// fakeRankingRepository is a minimal enrichment.RankingRepository — only
// FindRankings is exercised by PollGateway.Send. Send now queries this
// repository once per configured ranking source (VRS, HLTV): a test that
// only sets rankings gets that same data back regardless of which source
// asked (fine for tests that don't care about per-source scoping); a test
// that needs the two sources to differ sets bySource instead.
type fakeRankingRepository struct {
	rankings map[common.TeamID]enrichment.TeamRanking
	bySource map[enrichment.Source]map[common.TeamID]enrichment.TeamRanking
	err      error
}

func (f *fakeRankingRepository) SaveRanking(context.Context, enrichment.TeamRanking) error {
	return nil
}
func (f *fakeRankingRepository) FindRanking(context.Context, common.TeamID, enrichment.Source) (*enrichment.TeamRanking, error) {
	return nil, nil
}
func (f *fakeRankingRepository) FindRankings(_ context.Context, teamIDs []common.TeamID, source enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	if f.err != nil {
		return nil, f.err
	}
	src := f.rankings
	if f.bySource != nil {
		src = f.bySource[source]
	}
	out := map[common.TeamID]enrichment.TeamRanking{}
	for _, id := range teamIDs {
		if r, ok := src[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// factsCatalog wraps dataCatalog to additionally implement
// competition.MatchFactsCatalog, so Send's per-team tournament record
// lookup has something to find.
type factsCatalog struct {
	*dataCatalog
	facts competition.MatchFacts
}

func (c *factsCatalog) MatchFacts(context.Context, *competition.Match) (competition.MatchFacts, error) {
	return c.facts, nil
}

// sentPoll is the shape of the sendPoll call sendTestPoll captures.
type sentPoll struct {
	question    string
	description string
	closeDate   any // nil if the payload carried no close_date at all
}

// sendTestPoll wires a real PollGateway against a recording HTTP server and
// returns the question/description of the resulting sendPoll call.
func sendTestPoll(t *testing.T, catalog competition.Catalog, enrichmentSources PollEnrichmentSources, firstTeam, secondTeam competition.Team) sentPoll {
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
	if catalog == nil {
		catalog = &dataCatalog{
			events:           map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}},
			unstartedMatches: map[common.EventID][]competition.Match{eventID: {match}},
		}
	} else if base, ok := catalog.(*dataCatalog); ok {
		base.events = map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}}
		base.unstartedMatches = map[common.EventID][]competition.Match{eventID: {match}}
	} else if fc, ok := catalog.(*factsCatalog); ok {
		fc.events = map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}}
		fc.unstartedMatches = map[common.EventID][]competition.Match{eventID: {match}}
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
			d, _ := c["description"].(string)
			return sentPoll{question: q, description: d, closeDate: c["close_date"]}
		}
	}
	t.Fatal("no sendPoll call recorded")
	return sentPoll{}
}

func TestSend_QuestionIsShortTeamAndRecordOnly(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "G2"}
	second := competition.Team{ID: common.NewTeamID(), Name: "5star"}
	catalog := &factsCatalog{dataCatalog: &dataCatalog{}, facts: competition.MatchFacts{
		FirstEventBalance:  competition.TeamBalance{Wins: 2, Losses: 1},
		SecondEventBalance: competition.TeamBalance{Wins: 1, Losses: 1},
	}}
	sent := sendTestPoll(t, catalog, PollEnrichmentSources{}, first, second)
	if want := "G2 (2–1) · 5star (1–1)"; sent.question != want {
		t.Fatalf("question = %q, want %q", sent.question, want)
	}
}

func TestSend_QuestionNeverContainsTournamentInfo(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	for _, forbidden := range []string{"Major", "🏆", "🎯", "🕒"} {
		if strings.Contains(sent.question, forbidden) {
			t.Fatalf("expected no tournament info in question, found %q in %q", forbidden, sent.question)
		}
	}
	if !strings.Contains(sent.description, "🏆 Major") {
		t.Fatalf("expected the tournament name in description instead, got %q", sent.description)
	}
}

func TestSend_QuestionNeverContainsVRS(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(sent.question, "VRS") || strings.Contains(sent.question, "#1") {
		t.Fatalf("expected no VRS in question, got %q", sent.question)
	}
	if !strings.Contains(sent.description, "VRS: #1 · #3") {
		t.Fatalf("expected the VRS line in description instead, got %q", sent.description)
	}
}

func TestSend_QuestionNeverContainsHashtags(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if strings.Contains(sent.question, "#") || strings.Contains(sent.description, "#Major") {
		t.Fatalf("expected no hashtags anywhere, got question=%q description=%q", sent.question, sent.description)
	}
}

func TestSend_DescriptionHasRealNewlinesInTheRightOrder(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)

	if !strings.Contains(sent.description, "\n") {
		t.Fatalf("expected real newlines in description, got %q", sent.description)
	}
	tournamentIdx := strings.Index(sent.description, "🏆")
	stageIdx := strings.Index(sent.description, "🎯")
	timeIdx := strings.Index(sent.description, "🕒")
	vrsIdx := strings.Index(sent.description, "📈")
	if tournamentIdx == -1 || stageIdx == -1 || timeIdx == -1 || vrsIdx == -1 {
		t.Fatalf("expected all four blocks present, got %q", sent.description)
	}
	if tournamentIdx >= stageIdx || stageIdx >= timeIdx || timeIdx >= vrsIdx {
		t.Fatalf("expected block order tournament < stage < time < VRS, got %q", sent.description)
	}
}

func TestSend_VRSTeamNamesNotDuplicated(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	vrsLine := sent.description[strings.Index(sent.description, "📈"):]
	if strings.Contains(vrsLine, "Spirit") || strings.Contains(vrsLine, "NAVI") {
		t.Fatalf("expected no team names in the VRS line, got %q", vrsLine)
	}
}

// VRS order must match the question's team order (first, second) exactly.
func TestSend_VRSOrderMatchesQuestionTeamOrder(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 8, 121
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	if !strings.Contains(sent.question, "Spirit") || !strings.Contains(sent.question, "NAVI") {
		t.Fatalf("sanity check failed, got question %q", sent.question)
	}
	spiritIdx := strings.Index(sent.question, "Spirit")
	naviIdx := strings.Index(sent.question, "NAVI")
	rank8Idx := strings.Index(sent.description, "#8")
	rank121Idx := strings.Index(sent.description, "#121")
	if spiritIdx > naviIdx {
		t.Fatal("test assumption broken: expected Spirit before NAVI in the question")
	}
	if rank8Idx == -1 || rank121Idx == -1 || rank8Idx > rank121Idx {
		t.Fatalf("expected #8 (Spirit's rank) before #121 (NAVI's rank) in description, got %q", sent.description)
	}
}

func TestSend_MissingRecordOmitsParenthesesNotTheTeam(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "G2"}
	second := competition.Team{ID: common.NewTeamID(), Name: "5star"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if want := "G2 · 5star"; sent.question != want {
		t.Fatalf("question = %q, want %q (no facts catalog wired, so no record)", sent.question, want)
	}
}

func TestSend_MissingVRSOmitsTheLineCleanly(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if strings.Contains(sent.description, "VRS") || strings.Contains(sent.description, "📈") {
		t.Fatalf("expected no VRS line when rankings aren't wired, got %q", sent.description)
	}
}

// HLTV is checked against the same enrichment.RankingRepository as VRS, but
// as a wholly independent source (enrichment.SourceHLTV) — one having cached
// data must never leak into, or be conflated with, the other's line.
func TestSend_IncludesHLTVLineIndependentlyFromVRS(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	vrsRank, hltvRank := 1, 5
	rankings := &fakeRankingRepository{bySource: map[enrichment.Source]map[common.TeamID]enrichment.TeamRanking{
		enrichment.SourceValveVRS: {first.ID: {TeamID: first.ID, GlobalRank: &vrsRank}},
		enrichment.SourceHLTV:     {second.ID: {TeamID: second.ID, GlobalRank: &hltvRank}},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	if !strings.Contains(sent.description, "📈 VRS: #1 · N/A") {
		t.Fatalf("expected the VRS line scoped to its own source only, got %q", sent.description)
	}
	if !strings.Contains(sent.description, "🌐 HLTV: N/A · #5") {
		t.Fatalf("expected the HLTV line scoped to its own source only, got %q", sent.description)
	}
}

func TestSend_MissingHLTVOmitsTheLineCleanly(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if strings.Contains(sent.description, "HLTV") || strings.Contains(sent.description, "🌐") {
		t.Fatalf("expected no HLTV line when rankings aren't wired, got %q", sent.description)
	}
}

func TestSend_DescriptionHasNoLeadingOrTrailingBlankLines(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if strings.HasPrefix(sent.description, "\n") || strings.HasSuffix(sent.description, "\n") {
		t.Fatalf("expected no leading/trailing blank line, got %q", sent.description)
	}
}

func TestSend_DescriptionHasNoExtraBlankLinesBetweenBlocks(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(sent.description, "\n\n") {
		t.Fatalf("expected no blank line between description blocks, got %q", sent.description)
	}
}

// The old " • " separator that used to squeeze every insight into one
// crowded question line must never appear anywhere in the new payload.
func TestSend_NoLongerUsesTheOldBulletSeparator(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	catalog := &factsCatalog{dataCatalog: &dataCatalog{}, facts: competition.MatchFacts{
		FirstEventBalance:  competition.TeamBalance{Wins: 2, Losses: 1},
		SecondEventBalance: competition.TeamBalance{Wins: 1, Losses: 1},
	}}
	firstRank, secondRank := 1, 3
	rankings := &fakeRankingRepository{rankings: map[common.TeamID]enrichment.TeamRanking{
		first.ID:  {TeamID: first.ID, GlobalRank: &firstRank},
		second.ID: {TeamID: second.ID, GlobalRank: &secondRank},
	}}
	sent := sendTestPoll(t, catalog, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(sent.question, " • ") || strings.Contains(sent.description, " • ") {
		t.Fatalf("expected the old bullet separator to be gone entirely, got question=%q description=%q", sent.question, sent.description)
	}
}

func TestSend_OmitsVRSLineWhenLookupFails(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	rankings := &fakeRankingRepository{err: errors.New("ranking cache unavailable")}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Rankings: rankings}, first, second)
	if strings.Contains(sent.description, "VRS") {
		t.Fatalf("expected no VRS line when the cache lookup errors, got %q", sent.description)
	}
}

func TestSend_IncludesFormAndH2HWhenCached(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	form := &fakeFormRepository{forms: map[common.TeamID]enrichment.RecentForm{
		first.ID:  {Wins: 4, Losses: 1},
		second.ID: {Wins: 3, Losses: 2},
	}}
	h2h := &fakeHeadToHeadRepository{h2h: &enrichment.HeadToHead{TeamAWins: 6, TeamBWins: 4, Sample: 10}}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{Form: form, H2H: h2h}, first, second)
	if !strings.Contains(sent.description, "4–1 · 3–2") {
		t.Fatalf("expected the form line in description, got %q", sent.description)
	}
	if !strings.Contains(sent.description, "6–4") {
		t.Fatalf("expected the H2H line in description, got %q", sent.description)
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

// --- close_date ---

func TestSend_SetsCloseDateForANormalLeadTime(t *testing.T) {
	first := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	second := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	sent := sendTestPoll(t, nil, PollEnrichmentSources{}, first, second)
	if sent.closeDate == nil {
		t.Fatal("expected close_date to be set for a poll closing an hour from now")
	}
}

// TestSend_OmitsCloseDateWhenTheLeadTimeIsTooShort covers a match starting
// almost immediately: sendPoll would reject close_date outright below its
// documented 5-second minimum, so Send must skip the field entirely rather
// than risk the whole call failing over it — the scheduled CloseDue job
// still closes the poll the ordinary way.
func TestSend_OmitsCloseDateWhenTheLeadTimeIsTooShort(t *testing.T) {
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
	scheduledAt := time.Now().Add(2 * time.Second)
	matchID := common.NewMatchID()
	match := competition.Match{
		ID: matchID, EventID: eventID, Format: format, Status: competition.MatchNotStarted,
		FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "Spirit"},
		SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "NAVI"},
		ScheduledAt: &scheduledAt,
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
	gateway := NewPollGateway(client, catalog, chats, texts, slog.Default(), PollEnrichmentSources{})
	if _, err := gateway.Send(context.Background(), poll); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if c["__method"] == "sendPoll" {
			if _, ok := c["close_date"]; ok {
				t.Fatalf("expected no close_date for a 2-second lead time, got %v", c["close_date"])
			}
			return
		}
	}
	t.Fatal("no sendPoll call recorded")
}

// TestStop_TreatsAlreadyClosedAsSuccess covers the race close_date
// introduces: the scheduled CloseDue job's own stopPoll call can lose the
// race to Telegram's own timer and arrive after the poll is already closed
// — that must not surface as an error.
func TestStop_TreatsAlreadyClosedAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: poll has already been closed"}`))
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewPollGateway(client, &dataCatalog{}, newFakeChats(), texts, slog.Default(), PollEnrichmentSources{})
	messageID := int64(42)
	poll := prediction.Poll{ID: common.NewPollID(), ChatID: common.ChatID{Value: -1}, TelegramMessageID: &messageID}
	if err := gateway.Close(context.Background(), poll); err != nil {
		t.Fatalf("expected 'poll has already been closed' to be treated as success, got %v", err)
	}
}

// TestStop_TreatsCantBeStoppedAsSuccess is a regression test for a real
// production incident: Telegram uses "poll can't be stopped" for the exact
// same already-closed condition "poll has already been closed" covers, and
// the un-caught wording left CloseDue retrying the same handful of stuck
// polls on every single tick, indefinitely, since a stop() that returned an
// error never let close() persist the local CLOSED status.
func TestStop_TreatsCantBeStoppedAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: poll can't be stopped"}`))
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewPollGateway(client, &dataCatalog{}, newFakeChats(), texts, slog.Default(), PollEnrichmentSources{})
	messageID := int64(42)
	poll := prediction.Poll{ID: common.NewPollID(), ChatID: common.ChatID{Value: -1}, TelegramMessageID: &messageID}
	if err := gateway.Close(context.Background(), poll); err != nil {
		t.Fatalf("expected 'poll can't be stopped' to be treated as success, got %v", err)
	}
}

func TestStop_PropagatesOtherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewPollGateway(client, &dataCatalog{}, newFakeChats(), texts, slog.Default(), PollEnrichmentSources{})
	messageID := int64(42)
	poll := prediction.Poll{ID: common.NewPollID(), ChatID: common.ChatID{Value: -1}, TelegramMessageID: &messageID}
	if err := gateway.Close(context.Background(), poll); err == nil {
		t.Fatal("expected an unrelated stopPoll error to propagate")
	}
}
