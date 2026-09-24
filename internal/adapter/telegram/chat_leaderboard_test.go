package telegram

import (
	"testing"

	"cs2predictor/internal/domain/competition"
)

func TestChatLeaderboardCallbackRoundTrips(t *testing.T) {
	data := chatLeaderboardCallback(chatLeaderboardYear, competition.GameCS2)
	if data != "pstats:chatlb:y:CS2" {
		t.Fatalf("chatLeaderboardCallback = %q", data)
	}
	period, game := parseChatLeaderboardCallback("y:CS2")
	if period != chatLeaderboardYear || game != competition.GameCS2 {
		t.Fatalf("parseChatLeaderboardCallback = (%q, %q), want (y, CS2)", period, game)
	}
}

func TestChatLeaderboardCallback_AllGamesHasNoTrailingGame(t *testing.T) {
	period, game := parseChatLeaderboardCallback("a:")
	if period != chatLeaderboardAllTime || game != "" {
		t.Fatalf("parseChatLeaderboardCallback = (%q, %q), want (a, \"\")", period, game)
	}
}

func TestParseChatLeaderboardCallback_UnknownPeriodFallsBackToAllTime(t *testing.T) {
	period, _ := parseChatLeaderboardCallback("bogus:CS2")
	if period != chatLeaderboardAllTime {
		t.Fatalf("period = %q, want fallback to all-time", period)
	}
}
