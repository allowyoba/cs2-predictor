package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func startMsg(userID int64) *Message {
	text := "/start"
	return &Message{Chat: Chat{ID: userID, Type: "private"}, From: &User{ID: userID}, Text: &text}
}

// Someone with no admin rights anywhere and no operator role must land
// straight on their personal dashboard — a hub offering two dead-end
// options they can't use would be pure friction.
func TestStartLanding_NoRolesGoesStraightToPersonalDashboard(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.handlePrivateMessage(context.Background(), startMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if strings.Contains(body, ru(t, "hub.title")) {
		t.Fatalf("expected the personal dashboard directly, not the hub, got %q", body)
	}
}

// Someone who manages at least one chat gets the explicit hub instead,
// offering "personal" and "manage" but not "system" (they're not an
// operator).
func TestStartLanding_ManagerSeesHubWithPersonalAndManageOnly(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	userID := common.UserID{Value: 1}
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(context.Background(), chatID, userID); err != nil {
		t.Fatal(err)
	}

	if err := handler.handlePrivateMessage(context.Background(), startMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "hub.title")) {
		t.Fatalf("expected the hub screen, got %q", body)
	}
	labels := buttonLabels(t, lastKeyboardCall(t, *calls))
	if !slicesContainLabel(labels, ru(t, "hub.personal")) || !slicesContainLabel(labels, ru(t, "hub.manage")) {
		t.Fatalf("expected personal + manage buttons, got %v", labels)
	}
	if slicesContainLabel(labels, ru(t, "hub.system")) {
		t.Fatalf("expected no system-tools button for a non-operator, got %v", labels)
	}
}

// A root operator sees all three hub entries, including system tools.
func TestStartLanding_RootOperatorSeesAllThreePanels(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}

	if err := handler.handlePrivateMessage(context.Background(), startMsg(1)); err != nil {
		t.Fatal(err)
	}
	labels := buttonLabels(t, lastKeyboardCall(t, *calls))
	for _, want := range []string{ru(t, "hub.personal"), ru(t, "hub.system")} {
		if !slicesContainLabel(labels, want) {
			t.Fatalf("expected button %q, got %v", want, labels)
		}
	}
}

func TestSystemToolsMenu_DeniedForNonOperator(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(2, "hub:system")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected forbidden reply for a non-operator, got %q", lastText(*calls))
	}
}

func TestSystemToolsMenu_RootSeesProviderStatusAndOperatorsButDelegateDoesNot(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.TeamMatchOperators = newFakeTeamMatchOperatorRepo()
	handler.TeamMatchOperators.(*fakeTeamMatchOperatorRepo).appointed[2] = true

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:system")); err != nil {
		t.Fatal(err)
	}
	rootLabels := buttonLabels(t, lastKeyboardCall(t, *calls))
	if !slicesContainLabel(rootLabels, ru(t, "hub.provider_status")) {
		t.Fatalf("expected the provider-status button for a root operator, got %v", rootLabels)
	}

	*calls = nil
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(2, "hub:system")); err != nil {
		t.Fatal(err)
	}
	delegateLabels := buttonLabels(t, lastKeyboardCall(t, *calls))
	if slicesContainLabel(delegateLabels, ru(t, "hub.provider_status")) {
		t.Fatalf("expected no provider-status button for a delegated (non-root) operator, got %v", delegateLabels)
	}
	if !slicesContainLabel(delegateLabels, ru(t, "hub.team_matches")) {
		t.Fatalf("expected a delegated operator to still see team-match review, got %v", delegateLabels)
	}
}

func slicesContainLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// lastKeyboardCall returns the most recent recorded call that actually
// carries a reply_markup — skips the trailing bare answerCallbackQuery ack
// every callback-driven screen ends with.
func lastKeyboardCall(t *testing.T, calls []map[string]any) map[string]any {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if _, ok := calls[i]["reply_markup"]; ok {
			return calls[i]
		}
	}
	t.Fatalf("no recorded call carried a reply_markup: %+v", calls)
	return nil
}
