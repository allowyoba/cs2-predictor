package telegram

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/platform/common"
)

// The conversation around the Ideas channel: what the person is told
// before they write, and what they are told about what they wrote. The
// limits themselves are the domain's business (see feedback.Policy).

// memFeedbackRepo is the smallest feedback.Repository that still shows the
// flow working end to end.
type memFeedbackRepo struct {
	attempts    []feedback.Attempt
	suggestions []feedback.Suggestion
}

func (m *memFeedbackRepo) RecentAttempts(_ context.Context, _ common.UserID, since time.Time) ([]feedback.Attempt, error) {
	var out []feedback.Attempt
	for _, a := range m.attempts {
		if a.CreatedAt.After(since) {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memFeedbackRepo) HasFingerprint(context.Context, common.UserID, string, time.Time) (bool, error) {
	return false, nil
}
func (m *memFeedbackRepo) RecordAttempt(_ context.Context, a feedback.Attempt, s *feedback.Suggestion) error {
	m.attempts = append(m.attempts, a)
	if s != nil {
		m.suggestions = append(m.suggestions, *s)
	}
	return nil
}

// countingOutbox records how many messages were enqueued, whatever their
// type — enough to tell "the administrators were told" from "they were
// not".
type countingOutbox struct {
	fakeOutbox
	count int
}

func (c *countingOutbox) Enqueue(ctx context.Context, aggregateType, aggregateID, eventType, payload string) (uuid.UUID, error) {
	c.count++
	return c.fakeOutbox.Enqueue(ctx, aggregateType, aggregateID, eventType, payload)
}

// withFeedback wires the Ideas channel into a test handler.
func withFeedback(h *UpdateHandler) (*memFeedbackRepo, *countingOutbox) {
	repo, outbox := &memFeedbackRepo{}, &countingOutbox{}
	h.Feedback = &app.FeedbackService{
		Repo: repo, Outbox: outbox, Policy: feedback.DefaultPolicy(),
		Clock: common.SystemUTCClock(), Log: slog.Default(), AdminChatIDs: []int64{99},
	}
	return repo, outbox
}

// suggestionReply builds the message a person sends as an answer to the
// ForceReply prompt.
func suggestionReply(h *UpdateHandler, text string) *Message {
	prompt := stripHTML(h.Texts.Get("idea.prompt", common.LocaleRU, h.Feedback.Policy.MaxRunes))
	return &Message{
		MessageID: 2, Chat: Chat{ID: 5, Type: "private"},
		From:           &User{ID: 5, FirstName: "Аня"},
		Text:           &text,
		ReplyToMessage: &Message{MessageID: 1, Text: &prompt},
	}
}

// Somebody typing a long idea has to know the ceiling before they write to
// it, not after — so the limits are stated on the screen and in the prompt.
func TestSuggestionMenu_StatesTheLimitsUpFront(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	withFeedback(h)

	if err := h.suggestionMenu(context.Background(), sendTarget(common.ChatID{Value: 5}, nil), common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "1000") || !strings.Contains(text, "5") {
		t.Fatalf("expected the size and count limits to be visible, got %q", text)
	}

	*calls = nil
	if err := h.requestSuggestion(context.Background(), common.ChatID{Value: 5}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	prompt, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(prompt, "1000") {
		t.Fatalf("expected the prompt itself to name the size limit, got %q", prompt)
	}
	markup, _ := (*calls)[0]["reply_markup"].(map[string]any)
	if markup["force_reply"] != true {
		t.Fatalf("expected a force-reply prompt, got %v", markup)
	}
}

func TestSuggestionReply_AcceptedIdeaIsConfirmedAndForwarded(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	repo, outbox := withFeedback(h)

	msg := suggestionReply(h, "Добавьте статистику по картам")
	if err := h.applySuggestionReply(context.Background(), msg, msg.From, common.LocaleRU, *msg.Text); err != nil {
		t.Fatal(err)
	}

	if len(repo.suggestions) != 1 {
		t.Fatalf("expected the idea to be stored, got %+v", repo.suggestions)
	}
	if outbox.count != 1 {
		t.Fatalf("expected it to be passed to the administrators, got %d", outbox.count)
	}
	text, _ := (*calls)[len(*calls)-1]["text"].(string)
	if !strings.Contains(text, ru(t, "idea.accepted")) {
		t.Fatalf("expected a confirmation, got %q", text)
	}
}

// An over-long message is refused with the number to aim for, not silently
// truncated: truncation would deliver half an idea as if it were whole.
func TestSuggestionReply_RefusesAnOverlongMessageWithTheLimit(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	repo, outbox := withFeedback(h)

	msg := suggestionReply(h, strings.Repeat("я", h.Feedback.Policy.MaxRunes+1))
	if err := h.applySuggestionReply(context.Background(), msg, msg.From, common.LocaleRU, *msg.Text); err != nil {
		t.Fatal(err)
	}

	if len(repo.suggestions) != 0 || outbox.count != 0 {
		t.Fatal("an over-long message must not be stored or forwarded")
	}
	text, _ := (*calls)[len(*calls)-1]["text"].(string)
	if !strings.Contains(text, "1000") {
		t.Fatalf("expected the refusal to name the limit, got %q", text)
	}
}

// Past the flood threshold the bot answers nothing at all: replying to
// every message is exactly what makes an open channel worth flooding.
func TestSuggestionReply_GoesSilentUnderAFlood(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	withFeedback(h)

	for i := 0; i < h.Feedback.Policy.FloodAttempts+3; i++ {
		msg := suggestionReply(h, "идея "+string(rune('а'+i)))
		if err := h.applySuggestionReply(context.Background(), msg, msg.From, common.LocaleRU, *msg.Text); err != nil {
			t.Fatal(err)
		}
	}
	before := len(*calls)

	*calls = nil
	msg := suggestionReply(h, "ещё одна")
	if err := h.applySuggestionReply(context.Background(), msg, msg.From, common.LocaleRU, *msg.Text); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("expected no reply at all once flooding, got %v", *calls)
	}
	if before == 0 {
		t.Fatal("the person must have been told once what happened before the silence")
	}
}

// The reply is only recognized as an idea when it actually answers the
// prompt — an unrelated message in a DM must not be swallowed by this flow.
func TestIsSuggestionReply_OnlyMatchesThePrompt(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	withFeedback(h)

	idea := suggestionReply(h, "идея")
	if !h.isSuggestion(idea, common.LocaleRU) {
		t.Fatal("expected a reply to the prompt to be recognized")
	}
	other := "просто сообщение"
	plain := &Message{MessageID: 3, Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}, Text: &other}
	if h.isSuggestion(plain, common.LocaleRU) {
		t.Fatal("a message that is not a reply to the prompt is not an idea")
	}
}
