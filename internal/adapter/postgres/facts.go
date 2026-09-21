package postgres

import (
	"context"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// UserPredictionFacts reads every settled prediction with the match around
// it: tier, stage, format, both teams and their ranks.
//
// One query for four screens — see scoring.PredictionFact.
//
// Both ranks always come from the SAME feed. That is the whole subtlety
// here: Valve's standings run to about 390 places and HLTV's to about 100,
// so a team's position means a different thing in each. Taking whichever
// number is smaller — as this did at first — compares a top-ten place on
// one scale against a hundred-and-twentieth on the other and calls the
// difference a gap, which is arithmetic on two different units. On
// production data it mis-sorted a quarter of the matches.
//
// A match neither feed ranks on both sides is simply not readable as
// favourite against underdog, and says so by leaving both ranks null.
func (r *ScoringRepository) UserPredictionFacts(ctx context.Context, userID common.UserID, limit int) ([]scoring.PredictionFact, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
WITH ranked AS (
    SELECT team_id, source, global_rank
      FROM team_ranking
     WHERE global_rank IS NOT NULL
),
picks AS (
    SELECT COALESCE(m.actual_started_at, m.scheduled_at) AS played_at,
           g.code AS game,
           p.chat_id,
           COALESCE(e.tier, '') AS tier,
           COALESCE(es.name, '') AS stage,
           m.series_kind, m.series_size,
           CASE
             WHEN po.first_score > po.second_score THEN COALESCE(p.first_team_id, mt1.team_id)
             WHEN po.second_score > po.first_score THEN COALESCE(p.second_team_id, mt2.team_id)
           END AS picked_id,
           CASE
             WHEN po.first_score > po.second_score THEN COALESCE(p.second_team_id, mt2.team_id)
             WHEN po.second_score > po.first_score THEN COALESCE(p.first_team_id, mt1.team_id)
           END AS other_id,
           CASE
             WHEN m.first_score > m.second_score THEN COALESCE(p.first_team_id, mt1.team_id)
             WHEN m.second_score > m.first_score THEN COALESCE(p.second_team_id, mt2.team_id)
           END AS winner_id
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN poll_option po ON po.poll_id = v.poll_id AND po.option_index = v.option_index
      JOIN esport_match m ON m.id = p.match_id
      JOIN tournament_event e ON e.id = m.event_id
      JOIN game g ON g.id = e.game_id
      LEFT JOIN event_stage es ON es.id = m.stage_id
      LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
      LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
     WHERE v.user_id = $1
       AND m.status = 'FINISHED'
       AND m.first_score IS NOT NULL
       AND m.second_score IS NOT NULL
)
SELECT k.played_at, k.game, k.chat_id, k.tier, k.stage, k.series_kind, k.series_size,
       COALESCE(pt.name, ''), COALESCE(ot.name, ''),
       pair.picked_rank, pair.other_rank,
       k.picked_id = k.winner_id AS correct
  FROM picks k
  LEFT JOIN team pt ON pt.id = k.picked_id
  LEFT JOIN team ot ON ot.id = k.other_id
  -- One feed, both sides. HLTV first where it has an opinion: it is the
  -- Counter-Strike authority and its scale is the tighter of the two.
  LEFT JOIN LATERAL (
      SELECT p.global_rank AS picked_rank, o.global_rank AS other_rank
        FROM ranked p
        JOIN ranked o ON o.source = p.source AND o.team_id = k.other_id
       WHERE p.team_id = k.picked_id
       ORDER BY CASE p.source WHEN 'HLTV' THEN 0 ELSE 1 END
       LIMIT 1
  ) pair ON true
 WHERE k.picked_id IS NOT NULL
   AND k.winner_id IS NOT NULL
   AND k.played_at IS NOT NULL
 ORDER BY k.played_at DESC
 LIMIT $2`, userID.Value, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.PredictionFact
	for rows.Next() {
		var fact scoring.PredictionFact
		if err := rows.Scan(&fact.PlayedAt, &fact.Game, &fact.ChatID.Value, &fact.EventTier, &fact.Stage,
			&fact.Format.Kind, &fact.Format.Size,
			&fact.PickedTeam, &fact.OpponentTeam,
			&fact.PickedRank, &fact.OpponentRank, &fact.Correct); err != nil {
			return nil, err
		}
		out = append(out, fact)
	}
	return out, rows.Err()
}

var _ scoring.FactsRepository = (*ScoringRepository)(nil)
