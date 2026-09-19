package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// startMsg always builds the same user id — every caller in this file
// checks who is shown a hub built for user 1, not whose id is used.
func startMsg() *Message {
	text := "/start"
	return &Message{Chat: Chat{ID: 1, Type: "private"}, From: &User{ID: 1}, Text: &text}
}

// Someone with no admin rights anywhere and no operator role must land
// straight on their personal dashboard — a hub offering two dead-end
// options they can't use would be pure friction.
func TestStartLanding_NoRolesGoesStraightToPersonalDashboard(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.handlePrivateMessage(context.Background(), startMsg()); err != nil {
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

	if err := handler.handlePrivateMessage(context.Background(), startMsg()); err != nil {
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

	if err := handler.handlePrivateMessage(context.Background(), startMsg()); err != nil {
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

// TestPrivateStatsMenu_OffersBackToHubForSomeoneWhoHasOne is a regression
// test for a real navigation dead end: an operator (or manager) who opened
// their personal dashboard via hub:personal had no way back to the hub —
// privateStatsMenu rendered no back button at all.
func TestPrivateStatsMenu_OffersBackToHubForSomeoneWhoHasOne(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:personal")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if !containsPrefix(cds, "hub:root") {
		t.Fatalf("expected a back-to-hub button, got callback data %v", cds)
	}
}

// TestPrivateStatsMenu_NoHubButtonForAPlainVoter is the flip side: someone
// with no managed chat and no operator role never saw the hub in the first
// place (see TestStartLanding_NoRolesGoesStraightToPersonalDashboard), so a
// back-to-hub button here would point nowhere useful.
func TestPrivateStatsMenu_NoHubButtonForAPlainVoter(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.handlePrivateMessage(context.Background(), startMsg()); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if containsPrefix(cds, "hub:root") {
		t.Fatalf("a plain voter with no hub access must not see a back-to-hub button, got %v", cds)
	}
}

// TestManagedChatsMenu_BackTargetMatchesHowItWasReached is a regression test
// for the same class of bug: managedChatsMenu always sent its back button
// to "pstats:menu" regardless of entry point, stranding an operator who
// opened it via hub:manage — they'd be bounced sideways into the unrelated
// personal-stats screen instead of back to the hub.
func TestManagedChatsMenu_BackTargetMatchesHowItWasReached(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	userID := common.UserID{Value: 1}
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(context.Background(), chatID, userID); err != nil {
		t.Fatal(err)
	}

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:manage")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if !containsPrefix(cds, "hub:root") {
		t.Fatalf("expected the back button to return to the hub when reached via hub:manage, got %v", cds)
	}

	*calls = nil
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "manage:chats")); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if !containsPrefix(cds, "pstats:menu") {
		t.Fatalf("expected the back button to return to personal stats when reached via manage:chats, got %v", cds)
	}
}

// TestProviderStatusView_OffersBackToSystemTools is a regression test for
// another dead end reported from production: opening provider status from
// the system-tools panel rendered no keyboard at all, so there was no way
// back short of retyping /start.
func TestProviderStatusView_OffersBackToSystemTools(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:provider_status")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if !containsPrefix(cds, "hub:system") {
		t.Fatalf("expected a back-to-system-tools button, got %v", cds)
	}
}

// TestListTeamMatchOperators_OffersBackToSystemTools covers the same dead
// end for the operator roster screen.
func TestListTeamMatchOperators_OffersBackToSystemTools(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.TeamMatchOperators = newFakeTeamMatchOperatorRepo()

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:team_match_operators")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if !containsPrefix(cds, "hub:system") {
		t.Fatalf("expected a back-to-system-tools button, got %v", cds)
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

// Group management belongs to exactly one place: the hub. It used to be
// offered twice — once there, and again as a row inside the personal
// dashboard — and the second copy was shown to everyone, including the
// majority who manage nothing and could only ever reach an empty list.
func TestPrivateStatsMenu_DoesNotDuplicateGroupManagement(t *testing.T) {
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

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "hub:personal")); err != nil {
		t.Fatal(err)
	}
	buttons, _ := findKeyboardButtons([]map[string]any{lastKeyboardCall(t, *calls)})
	if slices.Contains(buttons, "manage:chats") {
		t.Fatalf("the personal dashboard must not offer group management; buttons: %v", buttons)
	}
	// Still one tap away: the back row leads to the hub, which carries the
	// single remaining entry.
	if !slices.Contains(buttons, "hub:root") {
		t.Fatalf("expected a back row to the hub, got %v", buttons)
	}
}
