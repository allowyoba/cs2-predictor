package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// MilestoneRepository implements scoring.MilestoneRepository against
// prediction_milestone, with the counts read from score_award — the awards
// are the truth, the table only remembers what has already been said.
type MilestoneRepository struct {
	pool *pgxpool.Pool
}

func NewMilestoneRepository(pool *pgxpool.Pool) *MilestoneRepository {
	return &MilestoneRepository{pool: pool}
}

var _ scoring.MilestoneRepository = (*MilestoneRepository)(nil)

// exactAwardsInChat is every exact-score award one person has in one chat.
// Joined through the poll because the chat hangs off it — see migration 0049.
const exactAwardsInChat = `
	  FROM score_award a
	  JOIN match_poll p ON p.id = a.poll_id
	 WHERE p.chat_id = $1 AND a.user_id = $2 AND a.kind = 'EXACT_SCORE'`

func (r *MilestoneRepository) ClaimMilestone(ctx context.Context, chatID common.ChatID, userID common.UserID,
	milestone int, at time.Time) (bool, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO prediction_milestone(chat_id, user_id, milestone, reached_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (chat_id, user_id, milestone) DO NOTHING`,
		chatID.Value, userID.Value, milestone, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *MilestoneRepository) ExactCount(ctx context.Context, chatID common.ChatID, userID common.UserID) (int, error) {
	var count int
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT COUNT(*)`+exactAwardsInChat,
		chatID.Value, userID.Value).Scan(&count)
	return count, err
}

func (r *MilestoneRepository) UserExactTotal(ctx context.Context, userID common.UserID) (int, error) {
	var count int
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT COUNT(*) FROM score_award WHERE user_id = $1 AND kind = 'EXACT_SCORE'`,
		userID.Value).Scan(&count)
	return count, err
}

// MilestoneJourney gathers every figure the congratulation quotes in one
// round trip: the whole effort behind the number, not just the number.
func (r *MilestoneRepository) MilestoneJourney(ctx context.Context, chatID common.ChatID, userID common.UserID,
	milestone int) (scoring.MilestoneJourney, error) {
	journey := scoring.MilestoneJourney{Milestone: milestone}
	var firstAt, previousAt *time.Time
	err := executor(ctx, r.pool).QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*)
		     FROM prediction_vote v
		     JOIN match_poll p ON p.id = v.poll_id
		     JOIN esport_match m ON m.id = p.match_id
		    WHERE p.chat_id = $1 AND v.user_id = $2 AND m.status = 'FINISHED'),
		  (SELECT COUNT(DISTINCT m.event_id)
		     FROM prediction_vote v
		     JOIN match_poll p ON p.id = v.poll_id
		     JOIN esport_match m ON m.id = p.match_id
		    WHERE p.chat_id = $1 AND v.user_id = $2 AND m.status = 'FINISHED'),
		  (SELECT MIN(v.voted_at)
		     FROM prediction_vote v
		     JOIN match_poll p ON p.id = v.poll_id
		    WHERE p.chat_id = $1 AND v.user_id = $2),
		  (SELECT MAX(reached_at)
		     FROM prediction_milestone
		    WHERE chat_id = $1 AND user_id = $2 AND milestone < $3)`,
		chatID.Value, userID.Value, milestone).
		Scan(&journey.Predictions, &journey.Events, &firstAt, &previousAt)
	if err != nil {
		return scoring.MilestoneJourney{}, err
	}
	if firstAt != nil {
		journey.FirstPredictionAt = *firstAt
	}
	if previousAt != nil {
		journey.PreviousMilestoneAt = *previousAt
	}
	return journey, nil
}

func (r *MilestoneRepository) UserMilestones(ctx context.Context, userID common.UserID) ([]scoring.MilestoneRecord, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT chat_id, milestone, reached_at FROM prediction_milestone
		  WHERE user_id = $1 ORDER BY reached_at DESC, milestone DESC`, userID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scoring.MilestoneRecord
	for rows.Next() {
		record := scoring.MilestoneRecord{UserID: userID}
		if err := rows.Scan(&record.ChatID.Value, &record.Milestone, &record.ReachedAt); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
