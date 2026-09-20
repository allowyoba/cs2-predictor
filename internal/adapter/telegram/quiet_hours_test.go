package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// A chat's quiet hours hold proactive messages instead of dropping them,
// and hold nothing that would be worthless late.

// recordingPublisher stands in for whatever is being wrapped.
type recordingPublisher struct{ published int }

func (p *recordingPublisher) Supports(string) bool { return true }
func (p *recordingPublisher) Publish(context.Context, common.OutboxMessage) error {
	p.published++
	return nil
}

// quietChat stores one chat with the given window, and returns the
// repository plus a message addressed to it.
func quietChat(t *testing.T, from, to *int) (*fakeChats, common.OutboxMessage) {
	t.Helper()
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{
		ChatID: chatID, Locale: common.LocaleRU, Timezone: "Europe/Moscow", Active: true,
		QuietFromMinute: from, QuietToMinute: to,
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"chatId": chatID.Value, "eventName": "Major"})
	if err != nil {
		t.Fatal(err)
	}
	return chats, common.OutboxMessage{Type: "telegram.big-event-discovered", Payload: string(payload)}
}

func minutesAt(h int) *int {
	v := h * 60
	return &v
}

// atMoscow is an instant that reads as this hour in the chat's zone.
func atMoscow(t *testing.T, hour int) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 10, hour, 0, 0, 0, chat.ZoneOrDefault("Europe/Moscow"))
}

func TestQuietHours_HoldsAMessageUntilTheWindowEnds(t *testing.T) {
	chats, message := quietChat(t, minutesAt(23), minutesAt(8))
	inner := &recordingPublisher{}
	publisher := WithQuietHours(inner, chats, common.FixedClock(atMoscow(t, 3)))

	err := publisher.Publish(context.Background(), message)

	var deferred *common.DeferredError
	if !errors.As(err, &deferred) {
		t.Fatalf("expected the message to be held, got %v", err)
	}
	if deferred.Until.Hour() != 8 {
		t.Fatalf("held until %s, want the end of the window", deferred.Until)
	}
	if inner.published != 0 {
		t.Fatal("nothing may be posted while a chat is quiet")
	}
	// Held, not dropped: the dispatcher reschedules on this error rather
	// than counting a failed attempt (see app.OutboxDispatcher).
	if deferred.Reason == "" {
		t.Fatal("a held message must say why, or the logs cannot explain the delay")
	}
}

func TestQuietHours_PostsNormallyOutsideTheWindow(t *testing.T) {
	chats, message := quietChat(t, minutesAt(23), minutesAt(8))
	inner := &recordingPublisher{}
	publisher := WithQuietHours(inner, chats, common.FixedClock(atMoscow(t, 12)))

	if err := publisher.Publish(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if inner.published != 1 {
		t.Fatal("outside the window the message goes out as usual")
	}
}

func TestQuietHours_DoNothingForAChatThatSetNoWindow(t *testing.T) {
	chats, message := quietChat(t, nil, nil)
	inner := &recordingPublisher{}
	publisher := WithQuietHours(inner, chats, common.FixedClock(atMoscow(t, 3)))

	if err := publisher.Publish(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if inner.published != 1 {
		t.Fatal("a chat with no window is never quiet")
	}
}

// A payload the decorator cannot read, or one not addressed to a chat, is
// passed straight through: guessing here would silently swallow messages.
func TestQuietHours_PassesThroughWhatItCannotRead(t *testing.T) {
	chats, _ := quietChat(t, minutesAt(23), minutesAt(8))
	inner := &recordingPublisher{}
	publisher := WithQuietHours(inner, chats, common.FixedClock(atMoscow(t, 3)))

	for _, payload := range []string{`{"not":"a chat"}`, `not json at all`} {
		if err := publisher.Publish(context.Background(), common.OutboxMessage{Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if inner.published != 2 {
		t.Fatalf("expected both to reach the inner publisher, got %d", inner.published)
	}
}

// The screen states the window in the chat's own zone, and switching it off
// is one tap from the same place.
func TestQuietHoursView_ShowsThePresetsAndTheWayOut(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "Europe/Moscow", Active: true}

	if err := h.quietHoursView(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "Europe/Moscow") {
		t.Fatalf("expected the chat's zone to be named, got %q", text)
	}
	markup, _ := json.Marshal((*calls)[0]["reply_markup"])
	for _, want := range []string{"23:00–08:00", "settings:quiet:-1"} {
		if !strings.Contains(string(markup), want) {
			t.Fatalf("expected %q among the choices, got %s", want, markup)
		}
	}
}
