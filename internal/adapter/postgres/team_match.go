package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

var (
	_ enrichment.TeamMatchRepository         = (*EnrichmentRepository)(nil)
	_ enrichment.TeamMatchHelperRepository   = (*EnrichmentRepository)(nil)
	_ enrichment.TeamMatchOperatorRepository = (*EnrichmentRepository)(nil)
	_ enrichment.SnapshotRepository          = (*EnrichmentRepository)(nil)
)

// --- enrichment.SnapshotRepository ---

func (r *EnrichmentRepository) SaveSnapshot(ctx context.Context, ranked []enrichment.RankedTeam) error {
	ex := executor(ctx, r.pool)
	for _, rt := range ranked {
		normalized := enrichment.NormalizeTeamName(rt.Identity.Name)
		if normalized == "" {
			continue
		}
		if _, err := ex.Exec(ctx, `
			INSERT INTO ranking_snapshot(source, normalized_name, external_name, global_rank, points, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (source, normalized_name) DO UPDATE SET
			  external_name = excluded.external_name, global_rank = excluded.global_rank,
			  points = excluded.points, updated_at = now()`,
			string(rt.Source), normalized, rt.Identity.Name, rt.GlobalRank, rt.Points); err != nil {
			return err
		}
	}
	return nil
}

func (r *EnrichmentRepository) AllSnapshot(ctx context.Context, source enrichment.Source) ([]enrichment.RankedTeam, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT external_name, global_rank, points FROM ranking_snapshot WHERE source = $1`, string(source))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []enrichment.RankedTeam
	for rows.Next() {
		rt := enrichment.RankedTeam{Source: source}
		if err := rows.Scan(&rt.Identity.Name, &rt.GlobalRank, &rt.Points); err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// --- enrichment.TeamMatchRepository ---

func (r *EnrichmentRepository) FindPendingByExternalName(ctx context.Context, source enrichment.Source, externalName string) (*enrichment.TeamMatchRequest, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, `
		SELECT id, external_name, source, status, best_team_id, best_score, crowd_asks_sent, created_at, resolved_at
		  FROM team_match_request WHERE source = $1 AND external_name = $2 AND status = 'pending'`,
		string(source), externalName)
	req, err := scanTeamMatchRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

func scanTeamMatchRequest(row pgx.Row) (*enrichment.TeamMatchRequest, error) {
	var req enrichment.TeamMatchRequest
	var source string
	var bestTeamID *uuid.UUID
	if err := row.Scan(&req.ID.Value, &req.ExternalName, &source, &req.Status, &bestTeamID, &req.BestScore,
		&req.CrowdAsksSent, &req.CreatedAt, &req.ResolvedAt); err != nil {
		return nil, err
	}
	req.Source = enrichment.Source(source)
	if bestTeamID != nil {
		req.BestTeamID = &common.TeamID{Value: *bestTeamID}
	}
	return &req, nil
}

func (r *EnrichmentRepository) CreateRequest(ctx context.Context, req enrichment.TeamMatchRequest, candidates []enrichment.TeamMatchCandidate) error {
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		ex := executor(ctx, r.pool)
		var bestTeamID *uuid.UUID
		if req.BestTeamID != nil {
			bestTeamID = &req.BestTeamID.Value
		}
		if _, err := ex.Exec(ctx, `
			INSERT INTO team_match_request(id, external_name, source, status, best_team_id, best_score, crowd_asks_sent, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			req.ID.Value, req.ExternalName, string(req.Source), string(req.Status), bestTeamID, req.BestScore, req.CrowdAsksSent, req.CreatedAt); err != nil {
			return err
		}
		for _, c := range candidates {
			if _, err := ex.Exec(ctx, `
				INSERT INTO team_match_candidate(request_id, team_id, score, score_kind) VALUES ($1, $2, $3, $4)`,
				req.ID.Value, c.TeamID.Value, c.Score, string(c.Kind)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *EnrichmentRepository) ListPending(ctx context.Context, limit int) ([]enrichment.TeamMatchRequest, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT id, external_name, source, status, best_team_id, best_score, crowd_asks_sent, created_at, resolved_at
		  FROM team_match_request WHERE status = 'pending'
		 ORDER BY best_score DESC, created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []enrichment.TeamMatchRequest
	for rows.Next() {
		req, err := scanTeamMatchRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (r *EnrichmentRepository) FindRequest(ctx context.Context, id common.RequestID) (*enrichment.TeamMatchRequest, []enrichment.TeamMatchCandidate, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, `
		SELECT id, external_name, source, status, best_team_id, best_score, crowd_asks_sent, created_at, resolved_at
		  FROM team_match_request WHERE id = $1`, id.Value)
	req, err := scanTeamMatchRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.team_id, t.name, c.score, c.score_kind,
		       COUNT(*) FILTER (WHERE r.answer = 'yes') AS yes_count,
		       COUNT(*) FILTER (WHERE r.answer = 'no') AS no_count
		  FROM team_match_candidate c
		  JOIN team t ON t.id = c.team_id
		  LEFT JOIN team_match_response r ON r.request_id = c.request_id AND r.candidate_team_id = c.team_id
		 WHERE c.request_id = $1
		 GROUP BY c.team_id, t.name, c.score, c.score_kind
		 ORDER BY c.score DESC`, id.Value)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var candidates []enrichment.TeamMatchCandidate
	for rows.Next() {
		var c enrichment.TeamMatchCandidate
		var kind string
		if err := rows.Scan(&c.TeamID.Value, &c.TeamName, &c.Score, &kind, &c.Yes, &c.No); err != nil {
			return nil, nil, err
		}
		c.Kind = enrichment.TeamMatchCandidateKind(kind)
		candidates = append(candidates, c)
	}
	return req, candidates, rows.Err()
}

func (r *EnrichmentRepository) RecordResponse(ctx context.Context, requestID common.RequestID, userID common.UserID, candidateTeamID common.TeamID, answer enrichment.TeamMatchAnswer) error {
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		ex := executor(ctx, r.pool)
		if _, err := ex.Exec(ctx, `
			INSERT INTO team_match_response(id, request_id, user_id, candidate_team_id, answer, responded_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (request_id, user_id) DO UPDATE SET
			  candidate_team_id = excluded.candidate_team_id, answer = excluded.answer, responded_at = now()`,
			uuid.New(), requestID.Value, userID.Value, candidateTeamID.Value, string(answer)); err != nil {
			return err
		}

		var current int
		if err := ex.QueryRow(ctx,
			`SELECT score FROM team_match_candidate WHERE request_id = $1 AND team_id = $2`,
			requestID.Value, candidateTeamID.Value).Scan(&current); err != nil {
			return err
		}
		adjusted := enrichment.CrowdAdjustedScore(current, answer)
		if _, err := ex.Exec(ctx, `
			UPDATE team_match_candidate SET score = $3, score_kind = 'crowd' WHERE request_id = $1 AND team_id = $2`,
			requestID.Value, candidateTeamID.Value, adjusted); err != nil {
			return err
		}

		var bestTeamID uuid.UUID
		var bestScore int
		if err := ex.QueryRow(ctx,
			`SELECT team_id, score FROM team_match_candidate WHERE request_id = $1 ORDER BY score DESC LIMIT 1`,
			requestID.Value).Scan(&bestTeamID, &bestScore); err != nil {
			return err
		}
		_, err := ex.Exec(ctx,
			`UPDATE team_match_request SET best_team_id = $2, best_score = $3 WHERE id = $1`,
			requestID.Value, bestTeamID, bestScore)
		return err
	})
}

func (r *EnrichmentRepository) HasResponded(ctx context.Context, requestID common.RequestID, userID common.UserID) (bool, error) {
	var exists bool
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_match_response WHERE request_id = $1 AND user_id = $2)`,
		requestID.Value, userID.Value).Scan(&exists)
	return exists, err
}

func (r *EnrichmentRepository) IncrementCrowdAsksSent(ctx context.Context, requestID common.RequestID, n int) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE team_match_request SET crowd_asks_sent = crowd_asks_sent + $2 WHERE id = $1`, requestID.Value, n)
	return err
}

func (r *EnrichmentRepository) Resolve(ctx context.Context, requestID common.RequestID, status enrichment.TeamMatchStatus, teamID *common.TeamID, at time.Time) error {
	var bestTeamID *uuid.UUID
	if teamID != nil {
		bestTeamID = &teamID.Value
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE team_match_request SET status = $2, best_team_id = COALESCE($3, best_team_id), resolved_at = $4 WHERE id = $1`,
		requestID.Value, string(status), bestTeamID, at)
	return err
}

// --- enrichment.TeamMatchHelperRepository ---

func (r *EnrichmentRepository) EligibleHelpers(ctx context.Context, candidates []common.UserID) ([]common.UserID, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT u.id FROM unnest($1::bigint[]) AS u(id)
		 LEFT JOIN team_match_helper_pref p ON p.user_id = u.id
		 WHERE COALESCE(p.opted_out, false) = false
		   AND COALESCE(p.asked_count, 0) < $2`,
		ids, enrichment.MaxLifetimeAsksPerUser)
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

func (r *EnrichmentRepository) RecordAsk(ctx context.Context, userID common.UserID, at time.Time) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_match_helper_pref(user_id, asked_count, last_asked_at) VALUES ($1, 1, $2)
		ON CONFLICT (user_id) DO UPDATE SET asked_count = team_match_helper_pref.asked_count + 1, last_asked_at = excluded.last_asked_at`,
		userID.Value, at)
	return err
}

func (r *EnrichmentRepository) SetOptedOut(ctx context.Context, userID common.UserID, optedOut bool) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_match_helper_pref(user_id, opted_out) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET opted_out = excluded.opted_out`,
		userID.Value, optedOut)
	return err
}

// --- enrichment.TeamMatchOperatorRepository ---

func (r *EnrichmentRepository) IsOperator(ctx context.Context, userID common.UserID) (bool, error) {
	var exists bool
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_match_operator WHERE user_id = $1)`, userID.Value).Scan(&exists)
	return exists, err
}

func (r *EnrichmentRepository) AddOperator(ctx context.Context, userID, appointedBy common.UserID) error {
	// The target may never have started a chat with the bot before being
	// appointed — team_match_operator.user_id references telegram_user, so
	// that row has to exist first (same pattern as AddModerator).
	if err := ensureUser(ctx, executor(ctx, r.pool), userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_match_operator(user_id, appointed_by, appointed_at) VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET appointed_by = excluded.appointed_by, appointed_at = excluded.appointed_at`,
		userID.Value, appointedBy.Value)
	return err
}

func (r *EnrichmentRepository) RemoveOperator(ctx context.Context, userID common.UserID) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `DELETE FROM team_match_operator WHERE user_id = $1`, userID.Value)
	return err
}

func (r *EnrichmentRepository) ListOperators(ctx context.Context) ([]common.UserID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT user_id FROM team_match_operator ORDER BY appointed_at`)
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
