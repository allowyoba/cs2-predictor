package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// ScoringRepository implements scoring.Repository, including
// timezone-aware leaderboard period boundaries (see periodClause).
type ScoringRepository struct {
	pool *pgxpool.Pool
}

func NewScoringRepository(pool *pgxpool.Pool) *ScoringRepository {
	return &ScoringRepository{pool: pool}
}

// scanStatsMonths reads the shared "DISTINCT year, month" row shape used by
// both AvailableMonths and AvailableUserMonths.
func scanStatsMonths(rows pgx.Rows) ([]scoring.StatsMonth, error) {
	defer rows.Close()
	var out []scoring.StatsMonth
	for rows.Next() {
		var year, month int
		if err := rows.Scan(&year, &month); err != nil {
			return nil, err
		}
		out = append(out, scoring.StatsMonth{Year: year, Month: time.Month(month)})
	}
	return out, rows.Err()
}

func (r *ScoringRepository) AvailableMonths(ctx context.Context, chatID common.ChatID) ([]scoring.StatsMonth, error) {
	zone, err := r.chatTimezone(ctx, chatID)
	if err != nil {
		return nil, err
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT DISTINCT
		       EXTRACT(YEAR FROM timezone($2, COALESCE(m.actual_started_at, m.scheduled_at)))::int AS year,
		       EXTRACT(MONTH FROM timezone($2, COALESCE(m.actual_started_at, m.scheduled_at)))::int AS month
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		  JOIN esport_match m ON m.id = p.match_id
		 WHERE p.chat_id = $1
		   AND m.status = 'FINISHED'
		   AND COALESCE(m.actual_started_at, m.scheduled_at) IS NOT NULL
		 ORDER BY year DESC, month DESC`, chatID.Value, zone.String())
	if err != nil {
		return nil, err
	}
	return scanStatsMonths(rows)
}

// AvailableUserMonths returns months in which the user cast at least one vote
// on a finished match in any group chat. Each vote is bucketed using that
// chat's own timezone, so cross-chat personal statistics stay consistent with
// the period users see inside each group.
func (r *ScoringRepository) AvailableUserMonths(ctx context.Context, userID common.UserID) ([]scoring.StatsMonth, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT DISTINCT
		       EXTRACT(YEAR FROM timezone(c.timezone, COALESCE(m.actual_started_at, m.scheduled_at)))::int AS year,
		       EXTRACT(MONTH FROM timezone(c.timezone, COALESCE(m.actual_started_at, m.scheduled_at)))::int AS month
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		  JOIN telegram_chat c ON c.id = p.chat_id
		  JOIN esport_match m ON m.id = p.match_id
		 WHERE v.user_id = $1
		   AND m.status = 'FINISHED'
		   AND COALESCE(m.actual_started_at, m.scheduled_at) IS NOT NULL
		 ORDER BY year DESC, month DESC`, userID.Value)
	if err != nil {
		return nil, err
	}
	return scanStatsMonths(rows)
}

func (r *ScoringRepository) AvailableEventIDs(ctx context.Context, chatID common.ChatID) ([]common.EventID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT m.event_id
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		  JOIN esport_match m ON m.id = p.match_id
		 WHERE p.chat_id = $1
		   AND m.status = 'FINISHED'
		 GROUP BY m.event_id
		 ORDER BY MAX(COALESCE(m.actual_started_at, m.scheduled_at)) DESC NULLS LAST`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []common.EventID
	for rows.Next() {
		var id common.EventID
		if err := rows.Scan(&id.Value); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *ScoringRepository) ReplaceAwards(ctx context.Context, pollID common.PollID, awards []scoring.Award) error {
	ex := executor(ctx, r.pool)
	if _, err := ex.Exec(ctx, `DELETE FROM score_award WHERE poll_id = $1`, pollID.Value); err != nil {
		return err
	}
	for _, a := range awards {
		if _, err := ex.Exec(ctx,
			`INSERT INTO score_award(id, chat_id, event_id, match_id, poll_id, user_id, points, kind, match_started_at, awarded_at)
			 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			a.ChatID.Value, a.EventID.Value, a.MatchID.Value, a.PollID.Value, a.UserID.Value, a.Points, a.Kind, a.MatchStartedAt, a.AwardedAt); err != nil {
			return err
		}
	}
	return nil
}

// chatTimezone resolves a chat's IANA zone, falling back to
// chat.DefaultTimezone ("Europe/Moscow") if the chat is missing or its
// stored zone string doesn't parse. A missing row is expected (the chat may
// not have been seen by this repository yet) and silently takes the
// fallback; any other error — a lost connection, a broken query — is
// propagated instead of being indistinguishable from "chat not found",
// since callers have a logger and this package deliberately doesn't.
func (r *ScoringRepository) chatTimezone(ctx context.Context, chatID common.ChatID) (*time.Location, error) {
	var tz string
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT timezone FROM telegram_chat WHERE id = $1`, chatID.Value).Scan(&tz)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return chat.ZoneOrDefault(tz), nil
}

// periodClause returns the extra SQL predicate and its args for a
// StatsPeriod: date-range periods are anchored to local midnight in the
// chat's own timezone, then compared against
// COALESCE(actual_started_at, scheduled_at).
func periodClause(period scoring.StatsPeriod, zone *time.Location) (string, []any) {
	bounds := func(from, until time.Time) (string, []any) {
		return " AND COALESCE(m.actual_started_at, m.scheduled_at) >= $2 AND COALESCE(m.actual_started_at, m.scheduled_at) < $3",
			[]any{from.In(time.UTC), until.In(time.UTC)}
	}
	switch period.Kind {
	case scoring.PeriodEvent:
		return " AND m.event_id = $2", []any{period.EventID.Value}
	case scoring.PeriodYear:
		from := time.Date(period.Year, time.January, 1, 0, 0, 0, 0, zone)
		return bounds(from, from.AddDate(1, 0, 0))
	case scoring.PeriodMonth:
		from := time.Date(period.Year, period.Month, 1, 0, 0, 0, 0, zone)
		return bounds(from, from.AddDate(0, 1, 0))
	case scoring.PeriodDay:
		from := time.Date(period.Day.Year(), period.Day.Month(), period.Day.Day(), 0, 0, 0, 0, zone)
		return bounds(from, from.AddDate(0, 0, 1))
	default: // AllTime
		return "", nil
	}
}

// scoringAggregateSelect is the aggregation column list shared by
// leaderboardQuery (chat-scoped) and userStatsQuery (user-scoped) — kept as
// one fragment so a column fix/addition only needs to happen once.
const scoringAggregateSelect = `
	SELECT u.id, COALESCE(NULLIF(u.nickname, ''), u.display_name) AS display_name,
	       COALESCE(SUM(a.points), 0) AS points,
	       COUNT(a.id) FILTER (WHERE a.kind = 'EXACT_SCORE') AS exact_count,
	       COUNT(a.id) AS correct_count,
	       COUNT(DISTINCT v.poll_id) AS prediction_count,
	       COUNT(DISTINCT m.event_id) AS tournament_count`

const leaderboardQuery = scoringAggregateSelect + `
	  FROM prediction_vote v
	  JOIN match_poll p ON p.id = v.poll_id
	  JOIN esport_match m ON m.id = p.match_id
	  JOIN telegram_user u ON u.id = v.user_id
	  LEFT JOIN score_award a ON a.poll_id = v.poll_id AND a.user_id = v.user_id
	 WHERE p.chat_id = $1 AND m.status = 'FINISHED'`

func (r *ScoringRepository) Leaderboard(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	zone, err := r.chatTimezone(ctx, chatID)
	if err != nil {
		return nil, err
	}
	clause, extraArgs := periodClause(period, zone)
	args := append([]any{chatID.Value}, extraArgs...)

	rows, err := executor(ctx, r.pool).Query(ctx, leaderboardQuery+clause+` GROUP BY u.id, u.display_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.UserStanding
	for rows.Next() {
		var s scoring.UserStanding
		var userVal int64
		if err := rows.Scan(&userVal, &s.DisplayName, &s.Points, &s.ExactPredictions, &s.CorrectPredictions, &s.Predictions, &s.Tournaments); err != nil {
			return nil, err
		}
		s.UserID = common.UserID{Value: userVal}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return scoring.DenseRank(out), nil
}

const userStatsQuery = scoringAggregateSelect + `
	  FROM prediction_vote v
	  JOIN match_poll p ON p.id = v.poll_id
	  JOIN telegram_chat c ON c.id = p.chat_id
	  JOIN esport_match m ON m.id = p.match_id
	  JOIN telegram_user u ON u.id = v.user_id
	  LEFT JOIN score_award a ON a.poll_id = v.poll_id AND a.user_id = v.user_id
	 WHERE v.user_id = $1 AND m.status = 'FINISHED'`

// userPeriodClause applies a personal-statistics period to each vote in the
// timezone of the group where that vote was cast. This matters near midnight
// when one person participates in multiple chats configured for different
// regions.
func userPeriodClause(period scoring.StatsPeriod) (string, []any) {
	switch period.Kind {
	case scoring.PeriodEvent:
		return " AND m.event_id = $2", []any{period.EventID.Value}
	case scoring.PeriodYear:
		return " AND EXTRACT(YEAR FROM timezone(c.timezone, COALESCE(m.actual_started_at, m.scheduled_at)))::int = $2", []any{period.Year}
	case scoring.PeriodMonth:
		return ` AND EXTRACT(YEAR FROM timezone(c.timezone, COALESCE(m.actual_started_at, m.scheduled_at)))::int = $2
		         AND EXTRACT(MONTH FROM timezone(c.timezone, COALESCE(m.actual_started_at, m.scheduled_at)))::int = $3`, []any{period.Year, int(period.Month)}
	default:
		return "", nil
	}
}

// UserStats aggregates one Telegram user's finished predictions across every
// group chat. A vote in two different chats is intentionally counted twice,
// because those are two separate poll participations; tournament_count stays
// deduplicated by event ID across chats.
func (r *ScoringRepository) UserStats(ctx context.Context, userID common.UserID, period scoring.StatsPeriod) (*scoring.UserStanding, error) {
	clause, extraArgs := userPeriodClause(period)
	args := append([]any{userID.Value}, extraArgs...)

	var s scoring.UserStanding
	var userVal int64
	err := executor(ctx, r.pool).QueryRow(ctx, userStatsQuery+clause+` GROUP BY u.id, u.display_name`, args...).Scan(
		&userVal, &s.DisplayName, &s.Points, &s.ExactPredictions, &s.CorrectPredictions, &s.Predictions, &s.Tournaments,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UserID = common.UserID{Value: userVal}
	return &s, nil
}

// UserChatStats powers the "by chats" screen in a private conversation with
// the bot. Only chats where the user actually voted on a finished match are
// returned, newest activity first.
func (r *ScoringRepository) UserChatStats(ctx context.Context, userID common.UserID) ([]scoring.UserChatStanding, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.id, c.title,
		       COALESCE(SUM(a.points), 0) AS points,
		       COUNT(a.id) AS correct_count,
		       COUNT(DISTINCT v.poll_id) AS prediction_count,
		       COUNT(DISTINCT m.event_id) AS tournament_count
		  FROM prediction_vote v
		  JOIN match_poll p ON p.id = v.poll_id
		  JOIN telegram_chat c ON c.id = p.chat_id
		  JOIN esport_match m ON m.id = p.match_id
		  LEFT JOIN score_award a ON a.poll_id = v.poll_id AND a.user_id = v.user_id
		 WHERE v.user_id = $1
		   AND m.status = 'FINISHED'
		 GROUP BY c.id, c.title
		 ORDER BY MAX(COALESCE(m.actual_started_at, m.scheduled_at)) DESC NULLS LAST, c.title`, userID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.UserChatStanding
	for rows.Next() {
		var row scoring.UserChatStanding
		if err := rows.Scan(&row.ChatID.Value, &row.ChatTitle, &row.Points, &row.CorrectPredictions, &row.Predictions, &row.Tournaments); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UserPredictions returns one person's most recent settled predictions
// across every chat, newest first, as vote-level rows: which team they
// backed and whether they were right. Everything derived from them
// (streaks, form, per-team accuracy, trend) is computed in the domain —
// see scoring.BuildPersonalInsights — so this query stays a plain read.
//
// A vote counts as correct when the winner they backed actually won.
// That is deliberately the "who wins" question rather than the exact
// scoreline: a person reads a team well or badly regardless of how many
// maps it took, and score_award would conflate the two.
func (r *ScoringRepository) UserPredictions(ctx context.Context, userID common.UserID, limit int) ([]scoring.UserPrediction, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
WITH picks AS (
    SELECT COALESCE(m.actual_started_at, m.scheduled_at) AS played_at,
           CASE
             WHEN po.first_score > po.second_score THEN mt1.team_id
             WHEN po.second_score > po.first_score THEN mt2.team_id
           END AS predicted_team_id,
           CASE
             WHEN m.first_score > m.second_score THEN mt1.team_id
             WHEN m.second_score > m.first_score THEN mt2.team_id
           END AS actual_team_id
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN poll_option po ON po.poll_id = v.poll_id AND po.option_index = v.option_index
      JOIN esport_match m ON m.id = p.match_id
      LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
      LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
     WHERE v.user_id = $1
       AND m.status = 'FINISHED'
       AND m.first_score IS NOT NULL
       AND m.second_score IS NOT NULL
)
SELECT p.played_at,
       COALESCE(t.name, ''),
       p.predicted_team_id = p.actual_team_id AS correct
  FROM picks p
  LEFT JOIN team t ON t.id = p.predicted_team_id
 WHERE p.predicted_team_id IS NOT NULL
   AND p.actual_team_id IS NOT NULL
   AND p.played_at IS NOT NULL
 ORDER BY p.played_at DESC
 LIMIT $2`, userID.Value, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.UserPrediction
	for rows.Next() {
		var p scoring.UserPrediction
		if err := rows.Scan(&p.PlayedAt, &p.TeamName, &p.Correct); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *ScoringRepository) MedalCounts(ctx context.Context, chatID common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT user_id, COUNT(*) FILTER (WHERE place = 1) gold, COUNT(*) FILTER (WHERE place = 2) silver,
		        COUNT(*) FILTER (WHERE place = 3) bronze FROM event_medal WHERE chat_id = $1 GROUP BY user_id`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[common.UserID]scoring.MedalCount{}
	for rows.Next() {
		var userVal int64
		var mc scoring.MedalCount
		if err := rows.Scan(&userVal, &mc.Gold, &mc.Silver, &mc.Bronze); err != nil {
			return nil, err
		}
		out[common.UserID{Value: userVal}] = mc
	}
	return out, rows.Err()
}

func (r *ScoringRepository) AwardMedals(ctx context.Context, chatID common.ChatID, eventID common.EventID, standings []scoring.UserStanding, at time.Time) error {
	ex := executor(ctx, r.pool)
	if _, err := ex.Exec(ctx, `DELETE FROM event_medal WHERE chat_id = $1 AND event_id = $2`, chatID.Value, eventID.Value); err != nil {
		return err
	}
	for _, s := range standings {
		if s.Rank < 1 || s.Rank > 3 {
			continue
		}
		if _, err := ex.Exec(ctx,
			`INSERT INTO event_medal(chat_id, event_id, user_id, place, awarded_at) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (chat_id, event_id, user_id) DO UPDATE SET place = excluded.place, awarded_at = excluded.awarded_at`,
			chatID.Value, eventID.Value, s.UserID.Value, s.Rank, at); err != nil {
			return err
		}
	}
	return nil
}

func (r *ScoringRepository) EventCompletionHash(ctx context.Context, chatID common.ChatID, eventID common.EventID) (string, bool, error) {
	var hash string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT result_hash FROM event_chat_completion WHERE chat_id = $1 AND event_id = $2`, chatID.Value, eventID.Value).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func (r *ScoringRepository) MarkEventCompleted(ctx context.Context, chatID common.ChatID, eventID common.EventID, resultHash string, at time.Time) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO event_chat_completion(chat_id, event_id, result_hash, completed_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (chat_id, event_id) DO UPDATE SET result_hash = excluded.result_hash, completed_at = excluded.completed_at`,
		chatID.Value, eventID.Value, resultHash, at)
	return err
}

// SettlementRepository implements scoring.SettlementRepository against
// match_settlement.
type SettlementRepository struct {
	pool *pgxpool.Pool
}

func NewSettlementRepository(pool *pgxpool.Pool) *SettlementRepository {
	return &SettlementRepository{pool: pool}
}

func (r *SettlementRepository) ResultHash(ctx context.Context, pollID common.PollID) (string, bool, error) {
	var hash string
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT result_hash FROM match_settlement WHERE poll_id = $1`, pollID.Value).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func (r *SettlementRepository) MarkSettled(ctx context.Context, pollID common.PollID, resultHash string, at time.Time) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO match_settlement(poll_id, result_hash, settled_at) VALUES ($1, $2, $3)
		 ON CONFLICT (poll_id) DO UPDATE SET result_hash = excluded.result_hash, settled_at = excluded.settled_at`,
		pollID.Value, resultHash, at)
	return err
}

// AnnualSpecials calculates the year-end metrics that require vote chronology
// or team joins. All boundaries use the chat's configured local timezone,
// exactly like the regular yearly leaderboard.
//
// The three sub-queries are independent (none consumes another's result) and
// run concurrently via errgroup against the pool — safe because pgxpool
// hands out a separate connection per concurrent query. This does assume ctx
// does NOT already carry an active transaction (see executor/WithTx in
// db.go): a pgx.Tx is not safe for concurrent use from multiple goroutines,
// so a future caller must not invoke AnnualSpecials from inside RunInTx. The
// current (and only) caller, DigestScheduler.enqueueAnnual, calls it before
// its own claim/enqueue transaction begins, so this holds today.
func (r *ScoringRepository) AnnualSpecials(ctx context.Context, chatID common.ChatID, year int) (scoring.AnnualSpecials, error) {
	zone, err := r.chatTimezone(ctx, chatID)
	if err != nil {
		return scoring.AnnualSpecials{}, err
	}
	from := time.Date(year, time.January, 1, 0, 0, 0, 0, zone).UTC()
	until := time.Date(year+1, time.January, 1, 0, 0, 0, 0, zone).UTC()

	var baselines []scoring.ComebackBaseline
	var streak *scoring.CorrectStreakInsight
	var synergy *scoring.TeamSynergyInsight

	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		baselines, err = r.comebackBaselines(gctx, chatID, from, until)
		return err
	})
	group.Go(func() (err error) {
		streak, err = r.longestCorrectStreak(gctx, chatID, from, until)
		return err
	})
	group.Go(func() (err error) {
		synergy, err = r.bestTeamSynergy(gctx, chatID, from, until)
		return err
	})
	if err := group.Wait(); err != nil {
		return scoring.AnnualSpecials{}, err
	}

	return scoring.AnnualSpecials{
		ComebackBaselines: baselines,
		LongestStreak:     streak,
		BestTeamSynergy:   synergy,
	}, nil
}

func (r *ScoringRepository) comebackBaselines(ctx context.Context, chatID common.ChatID, from, until time.Time) ([]scoring.ComebackBaseline, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
WITH year_votes AS (
    SELECT v.user_id,
           p.id AS poll_id,
           COALESCE(m.actual_started_at, m.scheduled_at) AS played_at
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN esport_match m ON m.id = p.match_id
     WHERE p.chat_id = $1
       AND m.status = 'FINISHED'
       AND COALESCE(m.actual_started_at, m.scheduled_at) >= $2
       AND COALESCE(m.actual_started_at, m.scheduled_at) < $3
), numbered AS (
    SELECT y.*,
           row_number() OVER (PARTITION BY y.user_id ORDER BY y.played_at, y.poll_id) AS rn,
           count(*) OVER (PARTITION BY y.user_id) AS total_predictions
      FROM year_votes y
), baselines AS (
    SELECT user_id, played_at AS baseline_at
      FROM numbered
     WHERE rn = $4
       AND total_predictions >= $5
), points_at_baseline AS (
    SELECT b.user_id AS candidate_id,
           y.user_id,
           COALESCE(SUM(a.points), 0)::int AS points
      FROM baselines b
      JOIN year_votes y ON y.played_at <= b.baseline_at
      LEFT JOIN score_award a ON a.poll_id = y.poll_id AND a.user_id = y.user_id
     GROUP BY b.user_id, y.user_id
), candidate_points AS (
    SELECT b.user_id,
           COALESCE(p.points, 0) AS points
      FROM baselines b
      LEFT JOIN points_at_baseline p
        ON p.candidate_id = b.user_id AND p.user_id = b.user_id
), baseline_ranks AS (
    SELECT cp.user_id,
           (1 + COUNT(DISTINCT p.points) FILTER (WHERE p.points > cp.points))::int AS rank
      FROM candidate_points cp
      JOIN points_at_baseline p ON p.candidate_id = cp.user_id
     GROUP BY cp.user_id, cp.points
)
SELECT r.user_id, COALESCE(NULLIF(u.nickname, ''), u.display_name), r.rank
  FROM baseline_ranks r
  JOIN telegram_user u ON u.id = r.user_id
 ORDER BY r.user_id`, chatID.Value, from, until, scoring.AnnualComebackBaselineVotes, scoring.AnnualComebackMinPredictions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.ComebackBaseline
	for rows.Next() {
		var item scoring.ComebackBaseline
		if err := rows.Scan(&item.UserID.Value, &item.DisplayName, &item.Rank); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ScoringRepository) longestCorrectStreak(ctx context.Context, chatID common.ChatID, from, until time.Time) (*scoring.CorrectStreakInsight, error) {
	var item scoring.CorrectStreakInsight
	err := executor(ctx, r.pool).QueryRow(ctx, `
WITH vote_results AS (
    SELECT v.user_id,
           COALESCE(NULLIF(u.nickname, ''), u.display_name) AS display_name,
           p.id AS poll_id,
           COALESCE(m.actual_started_at, m.scheduled_at) AS played_at,
           (a.id IS NOT NULL) AS correct
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN esport_match m ON m.id = p.match_id
      JOIN telegram_user u ON u.id = v.user_id
      LEFT JOIN score_award a ON a.poll_id = v.poll_id AND a.user_id = v.user_id
     WHERE p.chat_id = $1
       AND m.status = 'FINISHED'
       AND COALESCE(m.actual_started_at, m.scheduled_at) >= $2
       AND COALESCE(m.actual_started_at, m.scheduled_at) < $3
), marked AS (
    SELECT r.*,
           SUM(CASE WHEN correct THEN 0 ELSE 1 END)
             OVER (PARTITION BY user_id ORDER BY played_at, poll_id ROWS UNBOUNDED PRECEDING) AS grp
      FROM vote_results r
), runs AS (
    SELECT user_id, display_name, COUNT(*)::int AS streak
      FROM marked
     WHERE correct
     GROUP BY user_id, display_name, grp
)
SELECT user_id, display_name, streak
  FROM runs
 ORDER BY streak DESC, user_id
 LIMIT 1`, chatID.Value, from, until).Scan(&item.UserID.Value, &item.DisplayName, &item.Streak)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *ScoringRepository) bestTeamSynergy(ctx context.Context, chatID common.ChatID, from, until time.Time) (*scoring.TeamSynergyInsight, error) {
	var item scoring.TeamSynergyInsight
	err := executor(ctx, r.pool).QueryRow(ctx, `
WITH picks AS (
    SELECT v.user_id,
           COALESCE(NULLIF(u.nickname, ''), u.display_name) AS display_name,
           CASE
             WHEN po.first_score > po.second_score THEN mt1.team_id
             WHEN po.second_score > po.first_score THEN mt2.team_id
           END AS predicted_team_id,
           CASE
             WHEN m.first_score > m.second_score THEN mt1.team_id
             WHEN m.second_score > m.first_score THEN mt2.team_id
           END AS actual_team_id
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN poll_option po ON po.poll_id = v.poll_id AND po.option_index = v.option_index
      JOIN esport_match m ON m.id = p.match_id
      JOIN telegram_user u ON u.id = v.user_id
      LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
      LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
     WHERE p.chat_id = $1
       AND m.status = 'FINISHED'
       AND m.first_score IS NOT NULL
       AND m.second_score IS NOT NULL
       AND COALESCE(m.actual_started_at, m.scheduled_at) >= $2
       AND COALESCE(m.actual_started_at, m.scheduled_at) < $3
), grouped AS (
    SELECT p.user_id,
           p.display_name,
           p.predicted_team_id AS team_id,
           COUNT(*)::int AS predictions,
           COUNT(*) FILTER (WHERE p.predicted_team_id = p.actual_team_id)::int AS correct
      FROM picks p
     WHERE p.predicted_team_id IS NOT NULL
     GROUP BY p.user_id, p.display_name, p.predicted_team_id
    HAVING COUNT(*) >= $4
       AND COUNT(*) FILTER (WHERE p.predicted_team_id = p.actual_team_id) >= 3
)
SELECT g.user_id, g.display_name, t.name, g.correct, g.predictions
  FROM grouped g
  JOIN team t ON t.id = g.team_id
 ORDER BY (g.correct::numeric / g.predictions) DESC,
          g.correct DESC,
          g.predictions DESC,
          g.user_id
 LIMIT 1`, chatID.Value, from, until, scoring.AnnualTeamMinPredictions).Scan(
		&item.UserID.Value, &item.DisplayName, &item.TeamName, &item.Correct, &item.Predictions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}
