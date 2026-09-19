package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/platform/common"
)

// FeedbackService is the Ideas channel: it decides whether an incoming
// suggestion is accepted, records the attempt either way, and hands an
// accepted one to the administrators through the outbox.
//
// The whole surface is open to anybody who can start a private chat with
// the bot, which is everybody, so every decision about it is made by
// feedback.Policy rather than at the call site — see there for what the
// limits are and why.
type FeedbackService struct {
	Repo    feedback.Repository
	Outbox  common.Outbox
	Policy  feedback.Policy
	Clock   common.Clock
	Metrics *Metrics
	Log     *slog.Logger
	// AdminChatIDs receive the accepted ideas (DEPLOY_NOTIFY_CHAT_IDS).
	// With none configured an idea is still accepted and stored — losing
	// somebody's suggestion because of a missing environment variable
	// would be worse than nobody being paged about it.
	AdminChatIDs []int64
}

// Submit evaluates one attempt and returns what the person should be told.
// An error means the attempt could not be evaluated at all (the database
// is down); a refusal is a normal return with a non-accepted outcome.
func (s *FeedbackService) Submit(ctx context.Context, userID common.UserID, displayName, username, raw string) (feedback.Decision, error) {
	now := s.Clock.Now()
	window := s.Policy.QuotaWindow
	if s.Policy.FloodWindow > window {
		window = s.Policy.FloodWindow
	}
	recent, err := s.Repo.RecentAttempts(ctx, userID, now.Add(-window))
	if err != nil {
		return feedback.Decision{}, err
	}
	// The duplicate check is a second read, so it is only worth making
	// when the message could still be accepted — a flood is not asked
	// whether it is also repetitive.
	duplicate := false
	if fingerprint := feedback.Fingerprint(raw); fingerprint != "" && len(recent) < s.Policy.FloodAttempts {
		duplicate, err = s.Repo.HasFingerprint(ctx, userID, fingerprint, now.Add(-s.Policy.QuotaWindow))
		if err != nil {
			return feedback.Decision{}, err
		}
	}

	decision := s.Policy.Evaluate(now, raw, recent, duplicate)
	attempt := feedback.Attempt{UserID: userID, Outcome: decision.Outcome, CreatedAt: now}
	var suggestion *feedback.Suggestion
	if decision.Outcome == feedback.OutcomeAccepted {
		suggestion = &feedback.Suggestion{
			ID: common.NewRequestID(), UserID: userID,
			Text: decision.Text, Fingerprint: decision.Fingerprint, CreatedAt: now,
		}
	}
	if err := s.Repo.RecordAttempt(ctx, attempt, suggestion); err != nil {
		return feedback.Decision{}, err
	}
	if s.Metrics != nil {
		s.Metrics.Suggestions.WithLabelValues(string(decision.Outcome)).Inc()
	}
	if decision.Outcome != feedback.OutcomeAccepted {
		s.Log.Info("suggestion refused", "userId", userID.Value, "outcome", decision.Outcome)
		return decision, nil
	}
	s.notify(ctx, *suggestion, displayName, username)
	return decision, nil
}

// notify hands the accepted idea to each administrator chat. Best effort
// on purpose: the suggestion is already stored, and failing the submission
// after the fact would tell the person their idea was lost when it was
// not.
func (s *FeedbackService) notify(ctx context.Context, suggestion feedback.Suggestion, displayName, username string) {
	for _, chatID := range s.AdminChatIDs {
		payload, err := json.Marshal(common.SuggestionNotification{
			ChatID: chatID, SuggestionID: suggestion.ID.String(),
			UserID: suggestion.UserID.Value, DisplayName: displayName, Username: username,
			Text: suggestion.Text,
		})
		if err != nil {
			s.Log.Error("suggestion payload marshal failed", "error", err)
			continue
		}
		if _, err := s.Outbox.Enqueue(ctx, "SUGGESTION", suggestion.ID.String(), "telegram.suggestion", string(payload)); err != nil {
			s.Log.Error("suggestion enqueue failed", "chatId", chatID, "error", err)
		}
	}
}
