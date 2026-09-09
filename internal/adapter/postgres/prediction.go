package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// PredictionRepository implements prediction.Repository against match_poll,
// poll_option, and prediction_vote. Poll options are write-once: SavePoll
// only inserts them the first time a poll is saved (if none exist yet for
// that poll id), never updates them on subsequent saves.
type PredictionRepository struct {
	pool *pgxpool.Pool
}

func NewPredictionRepository(pool *pgxpool.Pool) *PredictionRepository {
	return &PredictionRepository{pool: pool}
}

func (r *PredictionRepository) scanPoll(ctx context.Context, row pgx.Row) (*prediction.Poll, error) {
	var p prediction.Poll
	var chatVal int64
	if err := row.Scan(&p.ID.Value, &chatVal, &p.MatchID.Value, &p.TopicID, &p.TelegramPollID, &p.TelegramMessageID, &p.Status, &p.ClosesAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.ChatID = common.ChatID{Value: chatVal}

	optRows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT option_index, first_score, second_score FROM poll_option WHERE poll_id = $1 ORDER BY option_index`, p.ID.Value)
	if err != nil {
		return nil, err
	}
	defer optRows.Close()
	for optRows.Next() {
		var o prediction.Option
		if err := optRows.Scan(&o.Index, &o.Score.First, &o.Score.Second); err != nil {
			return nil, err
		}
		p.Options = append(p.Options, o)
	}
	return &p, optRows.Err()
}

const pollSelect = `SELECT id, chat_id, match_id, topic_id, telegram_poll_id, telegram_message_id, status, closes_at FROM match_poll`

func (r *PredictionRepository) FindPoll(ctx context.Context, id common.PollID) (*prediction.Poll, error) {
	return r.scanPoll(ctx, executor(ctx, r.pool).QueryRow(ctx, pollSelect+` WHERE id = $1`, id.Value))
}

func (r *PredictionRepository) FindByTelegramPollID(ctx context.Context, telegramPollID string) (*prediction.Poll, error) {
	return r.scanPoll(ctx, executor(ctx, r.pool).QueryRow(ctx, pollSelect+` WHERE telegram_poll_id = $1`, telegramPollID))
}

func (r *PredictionRepository) FindByMatchAndChat(ctx context.Context, matchID common.MatchID, chatID common.ChatID) (*prediction.Poll, error) {
	return r.scanPoll(ctx, executor(ctx, r.pool).QueryRow(ctx, pollSelect+` WHERE match_id = $1 AND chat_id = $2`, matchID.Value, chatID.Value))
}

// queryPolls fetches every matching poll row in one query, then every
// option for all of them in a second query (WHERE poll_id = ANY(...)) and
// merges them in Go: two round trips total regardless of how many polls
// match. This runs on the hottest scheduled path in the app (OpenPollsDue,
// every few seconds), so the query count matters.
func (r *PredictionRepository) queryPolls(ctx context.Context, query string, args ...any) ([]prediction.Poll, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, pollSelect+query, args...)
	if err != nil {
		return nil, err
	}
	polls := make([]prediction.Poll, 0)
	byID := map[uuid.UUID]*prediction.Poll{}
	for rows.Next() {
		var p prediction.Poll
		var chatVal int64
		if err := rows.Scan(&p.ID.Value, &chatVal, &p.MatchID.Value, &p.TopicID, &p.TelegramPollID, &p.TelegramMessageID, &p.Status, &p.ClosesAt); err != nil {
			rows.Close()
			return nil, err
		}
		p.ChatID = common.ChatID{Value: chatVal}
		polls = append(polls, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(polls) == 0 {
		return polls, nil
	}

	ids := make([]uuid.UUID, len(polls))
	for i := range polls {
		ids[i] = polls[i].ID.Value
		byID[polls[i].ID.Value] = &polls[i]
	}

	optRows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT poll_id, option_index, first_score, second_score FROM poll_option
		 WHERE poll_id = ANY($1) ORDER BY poll_id, option_index`, ids)
	if err != nil {
		return nil, err
	}
	defer optRows.Close()
	for optRows.Next() {
		var pollID uuid.UUID
		var o prediction.Option
		if err := optRows.Scan(&pollID, &o.Index, &o.Score.First, &o.Score.Second); err != nil {
			return nil, err
		}
		if p, ok := byID[pollID]; ok {
			p.Options = append(p.Options, o)
		}
	}
	if err := optRows.Err(); err != nil {
		return nil, err
	}
	return polls, nil
}

func (r *PredictionRepository) OpenPollsForMatch(ctx context.Context, matchID common.MatchID) ([]prediction.Poll, error) {
	return r.queryPolls(ctx, ` WHERE match_id = $1 AND status = $2`, matchID.Value, prediction.PollOpen)
}

func (r *PredictionRepository) PollsForMatch(ctx context.Context, matchID common.MatchID) ([]prediction.Poll, error) {
	return r.queryPolls(ctx, ` WHERE match_id = $1`, matchID.Value)
}

func (r *PredictionRepository) OpenPollsDue(ctx context.Context, at time.Time) ([]prediction.Poll, error) {
	return r.queryPolls(ctx, ` WHERE status = $1 AND closes_at <= $2`, prediction.PollOpen, at)
}

// PollsAwaitingReminder finds open polls closing within the caller's
// window that have not been reminded about. reminded_at is the marker, so
// a poll is only ever picked up once however often the job runs.
func (r *PredictionRepository) PollsAwaitingReminder(ctx context.Context, closingBefore time.Time, limit int) ([]prediction.Poll, error) {
	if limit <= 0 {
		return nil, nil
	}
	return r.queryPolls(ctx,
		` WHERE status = $1 AND reminded_at IS NULL AND closes_at <= $2 ORDER BY closes_at LIMIT $3`,
		prediction.PollOpen, closingBefore, limit)
}

func (r *PredictionRepository) MarkReminded(ctx context.Context, pollID common.PollID, at time.Time) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE match_poll SET reminded_at = $2 WHERE id = $1 AND reminded_at IS NULL`, pollID.Value, at)
	return err
}

// ChatParticipants lists everyone who has voted in this chat since the
// given time. Telegram gives a bot no member list, so recent participation
// is the only available notion of "the people who play here".
func (r *PredictionRepository) ChatParticipants(ctx context.Context, chatID common.ChatID, since time.Time) ([]common.UserID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT DISTINCT v.user_id
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		 WHERE p.chat_id = $1 AND v.voted_at >= $2`, chatID.Value, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.UserID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.UserID{Value: id})
	}
	return out, rows.Err()
}

func (r *PredictionRepository) SavePoll(ctx context.Context, p prediction.Poll) (prediction.Poll, error) {
	ex := executor(ctx, r.pool)
	_, err := ex.Exec(ctx,
		`INSERT INTO match_poll(id, chat_id, match_id, topic_id, telegram_poll_id, telegram_message_id, status, closes_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), now())
		 ON CONFLICT (id) DO UPDATE SET topic_id=excluded.topic_id, telegram_poll_id=excluded.telegram_poll_id,
		   telegram_message_id=excluded.telegram_message_id, status=excluded.status, closes_at=excluded.closes_at, updated_at=now()`,
		p.ID.Value, p.ChatID.Value, p.MatchID.Value, p.TopicID, p.TelegramPollID, p.TelegramMessageID, p.Status, p.ClosesAt)
	if err != nil {
		return prediction.Poll{}, err
	}

	var existingOptions int
	if err := ex.QueryRow(ctx, `SELECT count(*) FROM poll_option WHERE poll_id = $1`, p.ID.Value).Scan(&existingOptions); err != nil {
		return prediction.Poll{}, err
	}
	if existingOptions == 0 {
		for _, o := range p.Options {
			if _, err := ex.Exec(ctx,
				`INSERT INTO poll_option(poll_id, option_index, first_score, second_score) VALUES ($1, $2, $3, $4)`,
				p.ID.Value, o.Index, o.Score.First, o.Score.Second); err != nil {
				return prediction.Poll{}, err
			}
		}
	}
	return p, nil
}

func (r *PredictionRepository) Votes(ctx context.Context, pollID common.PollID) ([]prediction.Vote, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT v.user_id, v.option_index, v.voted_at, u.username, u.display_name
		 FROM prediction_vote v LEFT JOIN telegram_user u ON u.id = v.user_id WHERE v.poll_id = $1`, pollID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []prediction.Vote
	for rows.Next() {
		var v prediction.Vote
		var userVal int64
		var displayName *string
		if err := rows.Scan(&userVal, &v.OptionIndex, &v.VotedAt, &v.Username, &displayName); err != nil {
			return nil, err
		}
		v.PollID = pollID
		v.UserID = common.UserID{Value: userVal}
		if displayName != nil {
			v.DisplayName = *displayName
		} else {
			// Synthesized fallback: user row missing, use a default display
			// name WITHOUT persisting it.
			v.DisplayName = "Telegram user " + v.UserID.String()
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *PredictionRepository) SaveVote(ctx context.Context, v prediction.Vote) error {
	ex := executor(ctx, r.pool)
	_, err := ex.Exec(ctx,
		`INSERT INTO telegram_user(id, username, display_name, created_at, updated_at) VALUES ($1, $2, $3, now(), now())
		 ON CONFLICT (id) DO UPDATE SET username = excluded.username, display_name = excluded.display_name, updated_at = now()`,
		v.UserID.Value, v.Username, v.DisplayName)
	if err != nil {
		return err
	}
	_, err = ex.Exec(ctx,
		`INSERT INTO prediction_vote(poll_id, user_id, option_index, voted_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (poll_id, user_id) DO UPDATE SET option_index = excluded.option_index, voted_at = excluded.voted_at`,
		v.PollID.Value, v.UserID.Value, v.OptionIndex, v.VotedAt)
	return err
}

func (r *PredictionRepository) RemoveVote(ctx context.Context, pollID common.PollID, userID common.UserID) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `DELETE FROM prediction_vote WHERE poll_id = $1 AND user_id = $2`, pollID.Value, userID.Value)
	return err
}
