package competition

import (
	"reflect"
	"testing"
	"time"
)

// Exact score sequences and point values the poll-option/scoring logic
// depends on.
func TestPossibleScores(t *testing.T) {
	toStrings := func(scores []MatchScore) []string {
		out := make([]string, len(scores))
		for i, s := range scores {
			out[i] = s.String()
		}
		return out
	}

	bo3, err := NewSeriesFormat(BestOf, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toStrings(bo3.PossibleScores()), []string{"2:0", "2:1", "1:2", "0:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("BO3 possibleScores = %v, want %v", got, want)
	}
	if bo3.ExactPoints() != 2 {
		t.Errorf("BO3 exactPoints = %d, want 2", bo3.ExactPoints())
	}

	fixedMaps2, err := NewSeriesFormat(FixedMaps, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toStrings(fixedMaps2.PossibleScores()), []string{"2:0", "1:1", "0:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("FIXED_MAPS(2) possibleScores = %v, want %v", got, want)
	}
	if fixedMaps2.ExactPoints() != 2 {
		t.Errorf("FIXED_MAPS(2) exactPoints = %d, want 2", fixedMaps2.ExactPoints())
	}

	firstTo4, err := NewSeriesFormat(FirstTo, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"4:0", "4:1", "4:2", "4:3", "3:4", "2:4", "1:4", "0:4"}
	if got := toStrings(firstTo4.PossibleScores()); !reflect.DeepEqual(got, want) {
		t.Errorf("FIRST_TO(4) possibleScores = %v, want %v", got, want)
	}
	if firstTo4.ExactPoints() != 4 {
		t.Errorf("FIRST_TO(4) exactPoints = %d, want 4", firstTo4.ExactPoints())
	}
}

func TestSeriesSizeMustBePositive(t *testing.T) {
	if _, err := NewSeriesFormat(BestOf, 0); err == nil {
		t.Fatal("expected error for size 0")
	}
}

func TestMatchScoreOutcome(t *testing.T) {
	cases := []struct {
		score MatchScore
		want  Outcome
	}{
		{MatchScore{2, 0}, OutcomeFirst},
		{MatchScore{0, 2}, OutcomeSecond},
		{MatchScore{1, 1}, OutcomeDraw},
	}
	for _, c := range cases {
		if got := c.score.Outcome(); got != c.want {
			t.Errorf("%v.Outcome() = %s, want %s", c.score, got, c.want)
		}
	}
}

func TestSeriesFormat_Label(t *testing.T) {
	bo3, _ := NewSeriesFormat(BestOf, 3)
	if got := bo3.Label(); got != "BO3" {
		t.Errorf("BestOf(3).Label() = %q, want BO3", got)
	}
	fixedMaps2, _ := NewSeriesFormat(FixedMaps, 2)
	if got := fixedMaps2.Label(); got != "BO2" {
		t.Errorf("FixedMaps(2).Label() = %q, want BO2 (FIXED_MAPS is labeled like BEST_OF)", got)
	}
	firstTo4, _ := NewSeriesFormat(FirstTo, 4)
	if got := firstTo4.Label(); got != "FT4" {
		t.Errorf("FirstTo(4).Label() = %q, want FT4", got)
	}
}

func TestNewMatchScore_RejectsNegativeComponents(t *testing.T) {
	if _, err := NewMatchScore(2, -1); err == nil {
		t.Fatal("expected an error for a negative score component")
	}
	if _, err := NewMatchScore(-1, 2); err == nil {
		t.Fatal("expected an error for a negative score component")
	}
	score, err := NewMatchScore(2, 1)
	if err != nil || score != (MatchScore{First: 2, Second: 1}) {
		t.Fatalf("NewMatchScore(2, 1) = %v, %v; want {2 1}, nil", score, err)
	}
}

func TestEventTier_IsTopTierAndBadge(t *testing.T) {
	cases := []struct {
		tier      EventTier
		isTopTier bool
		badge     string
	}{
		{TierS, true, "🌟 "},
		{TierA, true, "⭐ "},
		{TierB, false, "🔹 "},
		{TierC, false, ""},
		{TierUnranked, false, ""},
		{TierUnknown, false, ""},
	}
	for _, c := range cases {
		if got := c.tier.IsTopTier(); got != c.isTopTier {
			t.Errorf("%s.IsTopTier() = %v, want %v", c.tier, got, c.isTopTier)
		}
		if got := c.tier.Badge(); got != c.badge {
			t.Errorf("%s.Badge() = %q, want %q", c.tier, got, c.badge)
		}
	}
}

func TestEvent_IsSubscribable(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name  string
		event Event
		want  bool
	}{
		{"running, no end date", Event{Status: EventRunning}, true},
		{"upcoming, ends in the future", Event{Status: EventUpcoming, EndsAt: &future}, true},
		{"finished", Event{Status: EventFinished}, false},
		{"cancelled", Event{Status: EventCancelled}, false},
		{"running but its own end date already passed", Event{Status: EventRunning, EndsAt: &past}, false},
	}
	for _, c := range cases {
		if got := c.event.IsSubscribable(now); got != c.want {
			t.Errorf("%s: IsSubscribable() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMatch_ParticipantsKnownAndShouldCancelPrediction(t *testing.T) {
	team := &Team{Name: "Spirit"}

	if (Match{}).ParticipantsKnown() {
		t.Error("ParticipantsKnown() = true with no teams set")
	}
	if !(Match{FirstTeam: team, SecondTeam: team}).ParticipantsKnown() {
		t.Error("ParticipantsKnown() = false with both teams set")
	}

	for _, status := range []MatchStatus{MatchCancelled, MatchForfeit} {
		if !(Match{Status: status}).ShouldCancelPrediction() {
			t.Errorf("ShouldCancelPrediction() = false for status %s, want true", status)
		}
	}
	for _, status := range []MatchStatus{MatchNotStarted, MatchRunning, MatchFinished, MatchPostponed} {
		if (Match{Status: status}).ShouldCancelPrediction() {
			t.Errorf("ShouldCancelPrediction() = true for status %s, want false (postponed matches reschedule instead)", status)
		}
	}
}

// TestMatch_MainStreamURL covers the deliberately narrow selection: only
// the provider's main (always-official) broadcast, and only when it's in
// English — never a non-main community stream, and never another language
// just because no English one was reported.
func TestMatch_MainStreamURL(t *testing.T) {
	cases := []struct {
		name    string
		streams []Stream
		wantURL string
		wantOK  bool
	}{
		{"no streams at all", nil, "", false},
		{
			"main English wins over a non-main Russian community stream",
			[]Stream{
				{Language: "ru", URL: "https://twitch.tv/betboom_cs_ru3", Main: false, Official: false},
				{Language: "en", URL: "https://kick.com/cct_cs2", Main: true, Official: true},
			},
			"https://kick.com/cct_cs2", true,
		},
		{
			"main stream in a non-English language is not returned",
			[]Stream{{Language: "pt", URL: "https://kick.com/gaules", Main: true, Official: true}},
			"", false,
		},
		{
			"English stream that isn't the main one is not returned",
			[]Stream{{Language: "en", URL: "https://twitch.tv/somecaster", Main: false, Official: false}},
			"", false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, ok := (Match{Streams: c.streams}).MainStreamURL()
			if url != c.wantURL || ok != c.wantOK {
				t.Errorf("MainStreamURL() = %q, %v, want %q, %v", url, ok, c.wantURL, c.wantOK)
			}
		})
	}
}
