package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/platform/common"
)

// memFeedback is an in-memory feedback.Repository: enough of one to let the
// service's own decisions be observed end to end.
type memFeedback struct {
	attempts    []feedback.Attempt
	suggestions []feedback.Suggestion
}

func (m *memFeedback) RecentAttempts(_ context.Context, _ common.UserID, since time.Time) ([]feedback.Attempt, error) {
	var out []feedback.Attempt
	for _, a := range m.attempts {
		if a.CreatedAt.After(since) {
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *memFeedback) HasFingerprint(_ context.Context, _ common.UserID, fingerprint string, since time.Time) (bool, error) {
	for _, s := range m.suggestions {
		if s.Fingerprint == fingerprint && s.CreatedAt.After(since) {
			return true, nil
		}
	}
	return false, nil
}
func (m *memFeedback) RecordAttempt(_ context.Context, attempt feedback.Attempt, suggestion *feedback.Suggestion) error {
	m.attempts = append(m.attempts, attempt)
	if suggestion != nil {
		m.suggestions = append(m.suggestions, *suggestion)
	}
	return nil
}

func newFeedbackService(t *testing.T, admins []int64) (*FeedbackService, *memFeedback, *fakeSyncOutbox) {
	t.Helper()
	repo, outbox := &memFeedback{}, &fakeSyncOutbox{}
	return &FeedbackService{
		Repo: repo, Outbox: outbox, Policy: feedback.DefaultPolicy(),
		Clock: common.SystemUTCClock(), Metrics: newTestMetrics(), Log: slog.Default(),
		AdminChatIDs: admins,
	}, repo, outbox
}

func TestFeedbackService_AcceptedIdeaIsStoredAndPassedOn(t *testing.T) {
	svc, repo, outbox := newFeedbackService(t, []int64{10, 11})

	decision, err := svc.Submit(context.Background(), common.UserID{Value: 1}, "Аня", "anya", "Добавьте статистику по картам")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != feedback.OutcomeAccepted {
		t.Fatalf("outcome = %s, want accepted", decision.Outcome)
	}
	if len(repo.suggestions) != 1 || repo.suggestions[0].Text != "Добавьте статистику по картам" {
		t.Fatalf("expected the idea to be stored, got %+v", repo.suggestions)
	}
	if len(outbox.enqueued) != 2 {
		t.Fatalf("expected one message per administrator chat, got %v", outbox.enqueued)
	}
	for _, call := range outbox.enqueued {
		if call.eventType != "telegram.suggestion" {
			t.Fatalf("eventType = %q, want telegram.suggestion", call.eventType)
		}
	}
}

// Rejections are recorded too — they are what the flood threshold counts,
// and a limiter that only counted successes would be defeated by failing
// on purpose.
func TestFeedbackService_RecordsRefusalsAndTellsNobodyAboutThem(t *testing.T) {
	svc, repo, outbox := newFeedbackService(t, []int64{10})

	decision, err := svc.Submit(context.Background(), common.UserID{Value: 1}, "Аня", "", "   ")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != feedback.OutcomeEmpty {
		t.Fatalf("outcome = %s, want empty", decision.Outcome)
	}
	if len(repo.attempts) != 1 || repo.attempts[0].Outcome != feedback.OutcomeEmpty {
		t.Fatalf("expected the refused attempt to be recorded, got %+v", repo.attempts)
	}
	if len(repo.suggestions) != 0 {
		t.Fatal("a refused attempt keeps no copy of what was sent")
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("nobody is paged about a refusal, got %v", outbox.enqueued)
	}
}

// The same idea sent twice is answered as already-received rather than
// landing in somebody's inbox a second time.
func TestFeedbackService_RefusesTheSameIdeaTwice(t *testing.T) {
	svc, _, outbox := newFeedbackService(t, []int64{10})
	svc.Policy.Cooldown = 0 // the pause is not what is under test here

	if _, err := svc.Submit(context.Background(), common.UserID{Value: 1}, "Аня", "", "Добавьте карты"); err != nil {
		t.Fatal(err)
	}
	decision, err := svc.Submit(context.Background(), common.UserID{Value: 1}, "Аня", "", "  добавьте   КАРТЫ ")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != feedback.OutcomeDuplicate {
		t.Fatalf("outcome = %s, want duplicate", decision.Outcome)
	}
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected the second copy not to be delivered, got %v", outbox.enqueued)
	}
}

// A flood is stopped, and — past the threshold — stops being answered at
// all, which is the part that matters: a reply per message is what makes
// an open channel worth flooding.
func TestFeedbackService_StopsAnsweringAFlood(t *testing.T) {
	svc, _, outbox := newFeedbackService(t, []int64{10})
	userID := common.UserID{Value: 1}

	var lastAnswered, silent int
	for i := 0; i < svc.Policy.FloodAttempts+5; i++ {
		decision, err := svc.Submit(context.Background(), userID, "Аня", "", "idea number "+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if decision.Silent {
			silent++
			continue
		}
		lastAnswered = i
	}

	if silent == 0 {
		t.Fatal("past the flood threshold the bot must stop replying entirely")
	}
	if lastAnswered >= svc.Policy.FloodAttempts+1 {
		t.Fatalf("the bot kept answering until attempt %d, which is the amplification this prevents", lastAnswered)
	}
	if len(outbox.enqueued) > svc.Policy.Quota {
		t.Fatalf("a flood must never page the administrators more than the daily quota, got %d", len(outbox.enqueued))
	}
}
