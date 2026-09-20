package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/platform/common"
)

// Everything the bot shows in a private chat used to be rendered in the
// timezone of whichever group the reader happened to be in. For somebody
// living anywhere else that is quietly the wrong number on every screen,
// so the zone is theirs now — separate from any chat's, exactly like the
// language already is.

func TestUserTimezone_DefaultsToTheProductDefaultAndThenToTheirChoice(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	userID := common.UserID{Value: 7}

	if got := h.userZone(context.Background(), userID).String(); got != "Europe/Moscow" {
		t.Fatalf("zone = %q, want the product default until they choose", got)
	}

	// Pick Berlin from the picker (index 2 in commonTimezones).
	berlin := "Europe/Berlin"
	var index int
	for i, zone := range commonTimezones {
		if zone == berlin {
			index = i
		}
	}
	cb := &CallbackQuery{ID: "cb1", From: User{ID: userID.Value}, Message: &Message{Chat: Chat{ID: userID.Value, Type: "private"}}}
	if err := h.setUserTimezone(context.Background(), cb, sendTarget(common.ChatID(userID), nil),
		userID, common.LocaleRU, itoa(index)); err != nil {
		t.Fatal(err)
	}

	if got := h.userZone(context.Background(), userID).String(); got != berlin {
		t.Fatalf("zone = %q, want %q", got, berlin)
	}
	stored, err := chats.UserTimezone(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != berlin {
		t.Fatalf("stored zone = %v, want %q", stored, berlin)
	}
	// The picker re-renders with the choice marked, so the screen shows
	// state as well as options.
	last, _ := (*calls)[len(*calls)-1]["text"].(string)
	if !strings.Contains(last, berlin) {
		t.Fatalf("expected the picker to show the new zone, got %q", last)
	}
}

// A zone this process does not have is refused rather than silently
// rendering everything in UTC.
func TestUserTimezone_RefusesAChoiceOutsideThePicker(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 7}, Message: &Message{Chat: Chat{ID: 7, Type: "private"}}}

	for _, raw := range []string{"-1", "999", "not-a-number"} {
		if err := h.setUserTimezone(context.Background(), cb, sendTarget(common.ChatID{Value: 7}, nil),
			common.UserID{Value: 7}, common.LocaleRU, raw); err == nil {
			t.Fatalf("expected %q to be refused", raw)
		}
	}
}

// The person's own zone reaches the screens that show them dates.
func TestPrivateBets_RenderDatesInTheReadersOwnZone(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	userID := common.UserID{Value: 7}
	if err := chats.SetUserTimezone(context.Background(), userID, "UTC"); err != nil {
		t.Fatal(err)
	}

	if err := h.userTimezoneView(context.Background(), sendTarget(common.ChatID{Value: 7}, nil), userID, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	text, _ := (*calls)[len(*calls)-1]["text"].(string)
	if !strings.Contains(text, "UTC") {
		t.Fatalf("expected the reader's own zone on the screen, got %q", text)
	}
	markup, _ := json.Marshal((*calls)[len(*calls)-1]["reply_markup"])
	if !strings.Contains(string(markup), "pstats:tz:") {
		t.Fatalf("expected the picker's own callbacks, got %s", markup)
	}
}
