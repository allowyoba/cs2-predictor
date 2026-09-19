package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/platform/common"
)

// FeedbackRepository implements feedback.Repository against
// feature_suggestion — one row per attempt, accepted or not (see the
// migration for why rejections are kept).
type FeedbackRepository struct {
	pool *pgxpool.Pool
}

func NewFeedbackRepository(pool *pgxpool.Pool) *FeedbackRepository {
	return &FeedbackRepository{pool: pool}
}

func (r *FeedbackRepository) RecentAttempts(ctx context.Context, userID common.UserID, since time.Time) ([]feedback.Attempt, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT outcome, created_at FROM feature_suggestion
		  WHERE user_id = $1 AND created_at > $2
		  ORDER BY created_at DESC`, userID.Value, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []feedback.Attempt
	for rows.Next() {
		attempt := feedback.Attempt{UserID: userID}
		if err := rows.Scan(&attempt.Outcome, &attempt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (r *FeedbackRepository) HasFingerprint(ctx context.Context, userID common.UserID, fingerprint string, since time.Time) (bool, error) {
	var exists bool
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM feature_suggestion
		   WHERE user_id = $1 AND fingerprint = $2 AND created_at > $3)`,
		userID.Value, fingerprint, since).Scan(&exists)
	return exists, err
}

// RecordAttempt writes the attempt, and the suggestion's text alongside it
// when there is one. The user row is ensured first: somebody can reach the
// Ideas screen having never voted in any chat, so this may be the first
// time the bot stores anything about them at all.
func (r *FeedbackRepository) RecordAttempt(ctx context.Context, attempt feedback.Attempt, suggestion *feedback.Suggestion) error {
	ex := executor(ctx, r.pool)
	if err := ensureUser(ctx, ex, attempt.UserID); err != nil {
		return err
	}
	id := common.NewRequestID()
	var body, fingerprint *string
	if suggestion != nil {
		id = suggestion.ID
		body, fingerprint = &suggestion.Text, &suggestion.Fingerprint
	}
	_, err := ex.Exec(ctx,
		`INSERT INTO feature_suggestion(id, user_id, outcome, body, fingerprint, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id.Value, attempt.UserID.Value, string(attempt.Outcome), body, fingerprint, attempt.CreatedAt)
	return err
}
