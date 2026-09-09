package telegram

import (
	"context"
	"testing"
	"time"
)

func TestChatIDFromPayload_ReadsEveryNumericShape(t *testing.T) {
	cases := []struct {
		payload any
		want    int64
	}{
		{map[string]any{"chat_id": int64(-100)}, -100},
		{map[string]any{"chat_id": -100}, -100},
		{map[string]any{"chat_id": float64(-100)}, -100},
		{map[string]any{"chat_id": "@channelname"}, 0}, // no numeric id to pace by
		{map[string]any{"callback_query_id": "x"}, 0},  // not a chat-directed call
		{nil, 0},
	}
	for _, c := range cases {
		if got := chatIDFromPayload(c.payload); got != c.want {
			t.Fatalf("chatIDFromPayload(%v) = %d, want %d", c.payload, got, c.want)
		}
	}
}

// Zero limits mean "no pacing" — the default for a Client built without
// them, which is what keeps the test suite running at full speed.
func TestRateLimiter_ZeroLimitsDisablePacing(t *testing.T) {
	if l := newRateLimiter(0, 0); l != nil {
		t.Fatal("zero limits should produce no limiter at all")
	}
	var l *rateLimiter
	if err := l.wait(context.Background(), -100); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimiter_PacesRepeatedSendsToTheSameChat(t *testing.T) {
	l := newRateLimiter(DefaultGlobalMessagesPerSecond, DefaultPerChatMessagesPerSecond)
	ctx := context.Background()

	// The first call into a chat spends the burst token immediately; the
	// second has to wait for the per-chat bucket to refill.
	if err := l.wait(ctx, -100); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	deadlined, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	err := l.wait(deadlined, -100)
	if err == nil && time.Since(start) < 100*time.Millisecond {
		t.Fatal("a second immediate send to the same chat should have been paced")
	}
}

// Limiters for chats nobody has messaged in a long time are dropped, so a
// long-lived process doesn't accumulate one per chat forever.
func TestRateLimiter_ForgetsIdleChats(t *testing.T) {
	l := newRateLimiter(DefaultGlobalMessagesPerSecond, DefaultPerChatMessagesPerSecond)
	now := time.Now()
	l.now = func() time.Time { return now }

	l.forChat(-100)
	if len(l.perChat) != 1 {
		t.Fatalf("perChat = %d entries, want 1", len(l.perChat))
	}

	now = now.Add(2 * perChatLimiterTTL)
	l.forChat(-200) // a call for another chat triggers the sweep
	if _, stillThere := l.perChat[-100]; stillThere {
		t.Fatal("an idle chat's limiter should have been swept")
	}
	if _, ok := l.perChat[-200]; !ok {
		t.Fatal("the chat being used must survive its own sweep")
	}
}
