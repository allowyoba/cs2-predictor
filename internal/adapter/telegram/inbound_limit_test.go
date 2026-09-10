package telegram

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func TestInboundLimiter_NilDisablesThrottling(t *testing.T) {
	var l *InboundLimiter
	for i := 0; i < 100; i++ {
		if !l.Allow(1) {
			t.Fatal("nil InboundLimiter should always allow")
		}
	}
}

func TestInboundLimiter_AllowsBurstThenBlocks(t *testing.T) {
	l := NewInboundLimiter(1, 2) // 1/s, burst 2
	now := time.Now()
	l.now = func() time.Time { return now }

	first := l.Allow(1)
	second := l.Allow(1)
	if !first || !second {
		t.Fatalf("expected the initial burst of 2 to be allowed, got %v then %v", first, second)
	}
	if l.Allow(1) {
		t.Fatal("expected the 3rd immediate call to be throttled")
	}

	// A different user has their own, untouched bucket.
	if !l.Allow(2) {
		t.Fatal("expected a different user's bucket to be independent")
	}

	// After the refill interval, the first user's bucket has a token again.
	now = now.Add(time.Second)
	if !l.Allow(1) {
		t.Fatal("expected a token to be available after the refill interval")
	}
}

func TestInboundLimiter_GCDropsIdleUsersAfterTTL(t *testing.T) {
	l := NewInboundLimiter(1, 1)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Allow(1)
	if len(l.perKey) != 1 {
		t.Fatalf("expected one tracked user, got %d", len(l.perKey))
	}

	now = now.Add(inboundLimiterTTL + time.Minute)
	l.Allow(2) // triggers a GC sweep as a side effect
	if _, stillTracked := l.perKey[1]; stillTracked {
		t.Fatal("expected the idle user's limiter to be garbage collected")
	}
}

func TestUpdateActor(t *testing.T) {
	if _, ok := updateActor(Update{}); ok {
		t.Fatal("expected an empty update to have no attributable actor")
	}
	if id, ok := updateActor(Update{Message: &Message{From: &User{ID: 7}}}); !ok || id != 7 {
		t.Fatalf("message actor = %v, %v, want 7, true", id, ok)
	}
	if id, ok := updateActor(Update{CallbackQuery: &CallbackQuery{From: User{ID: 8}}}); !ok || id != 8 {
		t.Fatalf("callback actor = %v, %v, want 8, true", id, ok)
	}
	if id, ok := updateActor(Update{PollAnswer: &PollAnswer{User: User{ID: 9}}}); !ok || id != 9 {
		t.Fatalf("poll answer actor = %v, %v, want 9, true", id, ok)
	}
}

// TestHandle_DropsUpdatesOverTheInboundLimit is the end-to-end check: a
// flood of messages from the same user past the configured burst is
// dropped silently (no error, no dispatch), while a different user is
// unaffected.
func TestHandle_DropsUpdatesOverTheInboundLimit(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.InboundLimiter = NewInboundLimiter(1, 1) // burst of exactly 1
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	text := "/help"
	from := User{ID: 1, FirstName: "Flooder"}
	for i := 0; i < 5; i++ {
		update := Update{UpdateID: int64(i + 1), Message: &Message{MessageID: int64(i + 1), Chat: Chat{ID: -1, Type: "group"}, Text: &text, From: &from}}
		if err := handler.Handle(context.Background(), update); err != nil {
			t.Fatal(err)
		}
	}
	sends := 0
	for _, c := range *calls {
		if c["__method"] == "sendMessage" {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("expected exactly 1 reply out of 5 rapid updates from the same user, got %d", sends)
	}

	other := User{ID: 2, FirstName: "Someone else"}
	update := Update{UpdateID: 100, Message: &Message{MessageID: 100, Chat: Chat{ID: -1, Type: "group"}, Text: &text, From: &other}}
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	sends = 0
	for _, c := range *calls {
		if c["__method"] == "sendMessage" {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("expected a different user's request to go through independently, got %d total sends", sends)
	}
}
