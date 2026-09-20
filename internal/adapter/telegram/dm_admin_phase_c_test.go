package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeAdminMembership implements chat.MembershipGateway and the optional
// chat.AdministratorLister, with a configurable Telegram-admin list — used
// to drive AuthorizationService.OtherManagers for the unsubscribe fan-out
// tests. Every listed admin passes Role() as ADMINISTRATOR; anyone else is
// a plain MEMBER.
type fakeAdminMembership struct {
	admins []common.UserID
}

func (m *fakeAdminMembership) Role(_ context.Context, _ common.ChatID, userID common.UserID) (chat.MemberRole, error) {
	for _, id := range m.admins {
		if id == userID {
			return chat.RoleAdministrator, nil
		}
	}
	return chat.RoleMember, nil
}
func (m *fakeAdminMembership) Administrators(context.Context, common.ChatID) ([]common.UserID, error) {
	return m.admins, nil
}

var _ chat.AdministratorLister = (*fakeAdminMembership)(nil)

type fakePendingApprovals struct {
	items map[string]chat.PendingApproval
}

func newFakePendingApprovals() *fakePendingApprovals {
	return &fakePendingApprovals{items: map[string]chat.PendingApproval{}}
}
func (f *fakePendingApprovals) Create(_ context.Context, p chat.PendingApproval) error {
	f.items[p.ID.String()] = p
	return nil
}
func (f *fakePendingApprovals) Find(_ context.Context, id common.RequestID) (*chat.PendingApproval, error) {
	p, ok := f.items[id.String()]
	if !ok {
		return nil, nil
	}
	return &p, nil
}
func (f *fakePendingApprovals) Resolve(_ context.Context, id common.RequestID) error {
	delete(f.items, id.String())
	return nil
}

// newSelectiveServer behaves like newRecordingServer but returns a
// Telegram-style "bot can't initiate conversation" error for sendMessage/
// sendMessageWithKeyboard calls whose chat_id is in unreachable — modeling
// a manager who has never started a chat with the bot.
func newSelectiveServer(t *testing.T, unreachable map[int64]bool) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["__method"] = strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		calls = append(calls, body)
		w.Header().Set("Content-Type", "application/json")
		if chatID, ok := body["chat_id"].(float64); ok && unreachable[int64(chatID)] {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	return server, &calls
}

// fakeOutbox captures the confirmation requests the unsubscribe flow hands
// off for delivery, standing in for the real dispatcher.
type fakeOutbox struct {
	enqueued []common.UnsubscribeConfirmationNotification
}

func (f *fakeOutbox) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	if eventType == "telegram.unsubscribe-confirmation" {
		var n common.UnsubscribeConfirmationNotification
		if err := json.Unmarshal([]byte(payload), &n); err != nil {
			return uuid.Nil, err
		}
		f.enqueued = append(f.enqueued, n)
	}
	return uuid.New(), nil
}
func (f *fakeOutbox) Pending(context.Context, int) ([]common.OutboxMessage, error) { return nil, nil }
func (f *fakeOutbox) Published(context.Context, uuid.UUID, time.Time) error        { return nil }
func (f *fakeOutbox) Failed(context.Context, uuid.UUID, time.Time, string) error   { return nil }
func (f *fakeOutbox) Defer(context.Context, uuid.UUID, time.Time, time.Time) error { return nil }

func setupUnsubscribeTest(t *testing.T, admins []common.UserID, unreachable map[int64]bool, eventID common.EventID, chatID common.ChatID) (*UpdateHandler, *fakeChats, *fakePendingApprovals, *[]map[string]any) {
	t.Helper()
	server, calls := newSelectiveServer(t, unreachable)
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: admins})
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}}}
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{ChatID: chatID, EventID: eventID}}}
	pending := newFakePendingApprovals()
	handler.PendingApprovals = pending
	handler.Outbox = &fakeOutbox{}
	// Everyone except the requester counts as DM-reachable unless the test
	// says otherwise: that is what decides whether a second signature can be
	// asked for at all.
	for _, admin := range admins {
		if !unreachable[admin.Value] {
			_ = chats.SetDMReachable(context.Background(), admin, true)
		}
	}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	return handler, chats, pending, calls
}

func TestUnsubscribe_NoOtherManagers_CreatesSelfConfirmablePending(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	handler, chats, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester}, nil, eventID, chatID)
	_ = chats.SetDMSession(context.Background(), requester, chatID)

	data := "unsubscribe:" + eventID.Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(pending.items) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(pending.items))
	}
	var p chat.PendingApproval
	for _, v := range pending.items {
		p = v
	}
	if !p.SelfConfirmable {
		t.Fatal("expected SelfConfirmable=true with no other managers")
	}
	cds, _ := findKeyboardButtons(*calls)
	if !containsPrefix(cds, "unsubok:") {
		t.Fatalf("expected a self-confirm button, got %v", cds)
	}
}

func TestUnsubscribe_ReachableOtherManager_FansOutAndRequesterCannotSelfConfirm(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, chats, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	_ = chats.SetDMSession(context.Background(), requester, chatID)

	data := "unsubscribe:" + eventID.Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	var p chat.PendingApproval
	for _, v := range pending.items {
		p = v
	}
	if p.SelfConfirmable {
		t.Fatal("expected SelfConfirmable=false: another manager was reachable")
	}

	// The requester's own message must NOT carry a confirm button.
	requesterCds, _ := findKeyboardButtons([]map[string]any{(*calls)[len(*calls)-1]})
	if containsPrefix(requesterCds, "unsubok:") {
		t.Fatalf("requester's own screen must not offer self-confirm, got %v", requesterCds)
	}

	// The other manager must have a confirmation request queued for delivery
	// — handed to the outbox rather than sent inline, so the webhook that
	// started this returns immediately.
	outbox := handler.Outbox.(*fakeOutbox)
	if len(outbox.enqueued) != 1 || outbox.enqueued[0].UserID != other.Value {
		t.Fatalf("expected exactly one queued confirmation request for the other manager, got %+v", outbox.enqueued)
	}
	if outbox.enqueued[0].RequestID != p.ID.String() {
		t.Fatalf("queued request id = %q, want the stored request %q", outbox.enqueued[0].RequestID, p.ID.String())
	}
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == other.Value {
			t.Fatal("the fan-out must not send inline from the callback handler")
		}
	}
}

func TestUnsubscribe_UnreachableOtherManagers_FallsBackToSelfConfirm(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, chats, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, map[int64]bool{2: true}, eventID, chatID)
	_ = chats.SetDMSession(context.Background(), requester, chatID)

	data := "unsubscribe:" + eventID.Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	var p chat.PendingApproval
	for _, v := range pending.items {
		p = v
	}
	if !p.SelfConfirmable {
		t.Fatal("expected SelfConfirmable=true when the only other manager is unreachable")
	}
	cds, _ := findKeyboardButtons(*calls)
	if !containsPrefix(cds, "unsubok:") {
		t.Fatalf("expected a self-confirm fallback button, got %v", cds)
	}
}

func TestConfirmUnsubscribe_SelfConfirmable_RequesterConfirmsSuccessfully(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	handler, _, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	requestID := common.NewRequestID()
	now := time.Now()
	_ = pending.Create(context.Background(), chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &eventID, RequestedBy: requester,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), SelfConfirmable: true,
	})

	data := "unsubok:" + requestID.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.unsubscribed) != 1 || subs.unsubscribed[0] != eventID {
		t.Fatalf("expected the event to be unsubscribed, got %v", subs.unsubscribed)
	}
	if _, stillPending := pending.items[requestID.String()]; stillPending {
		t.Fatal("expected the pending request to be resolved")
	}
	announced := false
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == chatID.Value {
			if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "events.unsubscribed_announcement", bold(escapeHTML("Major")))) {
				announced = true
			}
		}
	}
	if !announced {
		t.Fatal("expected exactly one group-chat announcement of the completed unsubscribe")
	}
}

func TestConfirmUnsubscribe_NotSelfConfirmable_RequesterCannotConfirmOwnRequest(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, _, pending, _ := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	requestID := common.NewRequestID()
	now := time.Now()
	_ = pending.Create(context.Background(), chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &eventID, RequestedBy: requester,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), SelfConfirmable: false,
	})

	data := "unsubok:" + requestID.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.unsubscribed) != 0 {
		t.Fatalf("the requester must not be able to confirm their own multi-manager request, got %v", subs.unsubscribed)
	}
	if _, stillPending := pending.items[requestID.String()]; !stillPending {
		t.Fatal("the request must remain pending after a rejected self-confirm attempt")
	}
}

func TestConfirmUnsubscribe_OtherManagerConfirms_UnsubscribesAndNotifiesRequester(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, _, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	requestID := common.NewRequestID()
	now := time.Now()
	_ = pending.Create(context.Background(), chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &eventID, RequestedBy: requester,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), SelfConfirmable: false,
	})

	data := "unsubok:" + requestID.String()
	cb := &CallbackQuery{ID: "cb2", From: User{ID: 2, FirstName: "Other"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 2, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.unsubscribed) != 1 {
		t.Fatalf("expected the event to be unsubscribed by the other manager, got %v", subs.unsubscribed)
	}
	requesterNotified := false
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == requester.Value {
			if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "events.unsubscribe_request_confirmed", bold(escapeHTML("Major")), "Other")) {
				requesterNotified = true
			}
		}
	}
	if !requesterNotified {
		t.Fatal("expected the original requester to be notified their request was confirmed")
	}
}

func TestConfirmUnsubscribe_UnknownRequestToastsExpired(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	handler, _, _, _ := setupUnsubscribeTest(t, []common.UserID{requester}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	data := "unsubok:" + common.NewRequestID().String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if len(subs.unsubscribed) != 0 {
		t.Fatal("an unknown request id must never trigger an unsubscribe")
	}
}

func TestRejectUnsubscribe_OtherManagerRejects_NotifiesRequesterWithoutUnsubscribing(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, _, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	requestID := common.NewRequestID()
	now := time.Now()
	_ = pending.Create(context.Background(), chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &eventID, RequestedBy: requester,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), SelfConfirmable: false,
	})

	data := "unsubreject:" + requestID.String()
	cb := &CallbackQuery{ID: "cb2", From: User{ID: 2, FirstName: "Other"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 2, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.unsubscribed) != 0 {
		t.Fatal("a rejected request must never unsubscribe")
	}
	if _, stillPending := pending.items[requestID.String()]; stillPending {
		t.Fatal("expected the pending request to be resolved (rejected)")
	}
	requesterNotified := false
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == requester.Value {
			if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "events.unsubscribe_request_rejected", bold(escapeHTML("Major")), "Other")) {
				requesterNotified = true
			}
		}
	}
	if !requesterNotified {
		t.Fatal("expected the original requester to be notified their request was rejected")
	}
}

func TestUnsubscribeConfirmationPublisher_SendsConfirmAndRejectButtons(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	chats := newFakeChats()
	pub := NewUnsubscribeConfirmationPublisher(NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client()), chats, texts, nil)

	requestID := common.NewRequestID()
	payload, _ := json.Marshal(common.UnsubscribeConfirmationNotification{
		UserID: 2, RequestID: requestID.String(), ChatTitle: "Test Chat",
		EventName: "Major", Requester: "Admin", Locale: string(common.LocaleRU),
	})
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !containsPrefix(cds, "unsubok:") || !containsPrefix(cds, "unsubreject:") {
		t.Fatalf("expected confirm and reject buttons, got %v", cds)
	}
	if (*calls)[0]["chat_id"] != float64(2) {
		t.Fatalf("chat_id = %v, want the manager's own DM (2)", (*calls)[0]["chat_id"])
	}
}

// Telegram refusing to deliver is the expected outcome for a manager who
// never started a chat with the bot: it must not be retried forever, and it
// must stop future requests from counting on them.
func TestUnsubscribeConfirmationPublisher_MarksUnreachableInsteadOfFailing(t *testing.T) {
	server, _ := newSelectiveServer(t, map[int64]bool{2: true})
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	chats := newFakeChats()
	_ = chats.SetDMReachable(context.Background(), common.UserID{Value: 2}, true)
	pub := NewUnsubscribeConfirmationPublisher(NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client()), chats, texts, nil)

	payload, _ := json.Marshal(common.UnsubscribeConfirmationNotification{
		UserID: 2, RequestID: common.NewRequestID().String(), ChatTitle: "Test Chat", EventName: "Major",
	})
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatalf("an undeliverable DM must not fail the outbox message: %v", err)
	}
	if chats.reachable[2] {
		t.Fatal("the recipient should have been marked unreachable so future requests don't count on them")
	}
}

// A request waiting on someone else's signature is the one screen with
// nothing to do on it. The requester must be able to take it back rather
// than wait out the 24h TTL.
func TestUnsubscribe_PendingScreenOffersWithdraw(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, chats, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	_ = chats.SetDMSession(context.Background(), requester, chatID)

	data := "unsubscribe:" + eventID.Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	var requestID common.RequestID
	for _, p := range pending.items {
		if p.SelfConfirmable {
			t.Fatal("expected the request to need a second signature")
		}
		requestID = p.ID
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "unsubreject:"+requestID.String()) {
		t.Fatalf("expected a withdraw button on the waiting screen, got %v", cds)
	}
}

func TestRejectUnsubscribe_RequesterWithdraws_TellsTheManagersWhoWereAsked(t *testing.T) {
	chatID := common.ChatID{Value: -1}
	eventID := common.NewEventID()
	requester := common.UserID{Value: 1}
	other := common.UserID{Value: 2}
	handler, _, pending, calls := setupUnsubscribeTest(t, []common.UserID{requester, other}, nil, eventID, chatID)
	subs := handler.Subscriptions.(*dataSubs)

	requestID := common.NewRequestID()
	now := time.Now()
	_ = pending.Create(context.Background(), chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &eventID, RequestedBy: requester,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), SelfConfirmable: false,
	})

	data := "unsubreject:" + requestID.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.unsubscribed) != 0 {
		t.Fatal("withdrawing must never unsubscribe")
	}
	if _, stillPending := pending.items[requestID.String()]; stillPending {
		t.Fatal("expected the withdrawn request to be resolved")
	}
	notice := handler.Texts.Get("events.unsubscribe_request_withdrawn", common.LocaleRU, bold("Major"), "Test Chat")
	notified := false
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == other.Value {
			if text, ok := c["text"].(string); ok && text == notice {
				notified = true
			}
		}
	}
	if !notified {
		t.Fatal("expected the manager who was asked to decide to be told the request was withdrawn")
	}
	// The withdrawal is the requester's own doing — no "someone rejected
	// your request" notice back to themselves.
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == requester.Value {
			if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "events.unsubscribe_request_rejected", bold(escapeHTML("Major")), "Other")) {
				t.Fatal("withdrawing must not read as someone else rejecting the request")
			}
		}
	}
}
