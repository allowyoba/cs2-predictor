package postgres

import (
	"context"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// UserTeamBias compares how often somebody backs a team with how often
// that team actually won — over the same matches, which is what makes the
// comparison mean anything.
//
// Both sides of every match are unnested, so a team counts whether it was
// picked or played against: a bias is only visible against the matches
// somebody saw and did not back them.
//
// The picked side comes from the poll's own team anchor rather than from
// the match's current one. PandaScore's opponent order is not stable
// across fetches, so a poll settled against the wrong anchor would silently
// swap who was backed — see migration 0030.
func (r *ScoringRepository) UserTeamBias(ctx context.Context, userID common.UserID, limit int) ([]scoring.TeamBias, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
WITH picks AS (
    SELECT g.code AS game,
           COALESCE(p.first_team_id, mt1.team_id)  AS anchor_first,
           COALESCE(p.second_team_id, mt2.team_id) AS anchor_second,
           CASE
             WHEN po.first_score > po.second_score THEN COALESCE(p.first_team_id, mt1.team_id)
             WHEN po.second_score > po.first_score THEN COALESCE(p.second_team_id, mt2.team_id)
           END AS picked,
           CASE
             WHEN m.first_score > m.second_score THEN COALESCE(p.first_team_id, mt1.team_id)
             WHEN m.second_score > m.first_score THEN COALESCE(p.second_team_id, mt2.team_id)
           END AS winner
      FROM prediction_vote v
      JOIN match_poll p ON p.id = v.poll_id
      JOIN poll_option po ON po.poll_id = v.poll_id AND po.option_index = v.option_index
      JOIN esport_match m ON m.id = p.match_id
      JOIN tournament_event e ON e.id = m.event_id
      JOIN game g ON g.id = e.game_id
      LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
      LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
     WHERE v.user_id = $1
       AND m.status = 'FINISHED'
       AND m.first_score IS NOT NULL
       AND m.second_score IS NOT NULL
),
sides AS (
    SELECT game, picked, winner, anchor_first AS team FROM picks
    UNION ALL
    SELECT game, picked, winner, anchor_second FROM picks
)
SELECT s.team, COALESCE(t.name, ''), s.game,
       COUNT(*) AS matches,
       COUNT(*) FILTER (WHERE s.picked = s.team) AS picks,
       COUNT(*) FILTER (WHERE s.winner = s.team) AS wins,
       COUNT(*) FILTER (WHERE s.picked = s.winner) AS correct
  FROM sides s
  LEFT JOIN team t ON t.id = s.team
 WHERE s.team IS NOT NULL
   AND s.picked IS NOT NULL
   AND s.winner IS NOT NULL
 GROUP BY s.team, t.name, s.game
 ORDER BY COUNT(*) DESC, t.name
 LIMIT $2`, userID.Value, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scoring.TeamBias
	for rows.Next() {
		var bias scoring.TeamBias
		if err := rows.Scan(&bias.TeamID.Value, &bias.TeamName, &bias.Game,
			&bias.Matches, &bias.Picks, &bias.Wins, &bias.Correct); err != nil {
			return nil, err
		}
		out = append(out, bias)
	}
	return out, rows.Err()
}

var _ scoring.BiasRepository = (*ScoringRepository)(nil)
