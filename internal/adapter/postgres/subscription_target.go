package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// subscription.TargetRepository: tournament targets go through the existing
// event_subscription methods, team/player targets through chat_follow.

func teamUUIDs(ids []common.TeamID) []uuid.UUID {
	out := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		out[i] = id.Value
	}
	return out
}

func (r *SubscriptionRepository) SubscribeTarget(ctx context.Context, chatID common.ChatID, t subscription.Target, at time.Time) error {
	switch t.Kind {
	case subscription.TargetTournament:
		_, err := r.Subscribe(ctx, subscription.EventSubscription{ChatID: chatID, EventID: t.EventID, SubscribedAt: at, Active: true})
		return err
	case subscription.TargetTeam:
		_, err := executor(ctx, r.pool).Exec(ctx,
			`INSERT INTO chat_follow(chat_id, kind, team_id, label, subscribed_at) VALUES ($1, 'TEAM', $2, $3, $4)
			 ON CONFLICT (chat_id, team_id) WHERE kind = 'TEAM'
			 DO UPDATE SET active = true, label = excluded.label, subscribed_at = excluded.subscribed_at`,
			chatID.Value, t.TeamID.Value, t.Label, at)
		return err
	case subscription.TargetPlayer:
		_, err := executor(ctx, r.pool).Exec(ctx,
			`INSERT INTO chat_follow(chat_id, kind, player, label, subscribed_at) VALUES ($1, 'PLAYER', $2, $3, $4)
			 ON CONFLICT (chat_id, player) WHERE kind = 'PLAYER'
			 DO UPDATE SET active = true, label = excluded.label, subscribed_at = excluded.subscribed_at`,
			chatID.Value, subscription.NormalizePlayer(t.Player), t.Label, at)
		return err
	}
	return fmt.Errorf("unknown subscription target kind %q", t.Kind)
}

func (r *SubscriptionRepository) UnsubscribeTarget(ctx context.Context, chatID common.ChatID, t subscription.Target) error {
	switch t.Kind {
	case subscription.TargetTournament:
		return r.Unsubscribe(ctx, chatID, t.EventID)
	case subscription.TargetTeam:
		_, err := executor(ctx, r.pool).Exec(ctx,
			`UPDATE chat_follow SET active = false WHERE chat_id = $1 AND kind = 'TEAM' AND team_id = $2`, chatID.Value, t.TeamID.Value)
		return err
	case subscription.TargetPlayer:
		_, err := executor(ctx, r.pool).Exec(ctx,
			`UPDATE chat_follow SET active = false WHERE chat_id = $1 AND kind = 'PLAYER' AND player = $2`,
			chatID.Value, subscription.NormalizePlayer(t.Player))
		return err
	}
	return fmt.Errorf("unknown subscription target kind %q", t.Kind)
}

func (r *SubscriptionRepository) Targets(ctx context.Context, chatID common.ChatID) ([]subscription.Target, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT 'TOURNAMENT' AS kind, s.event_id, NULL::uuid AS team_id, '' AS player, e.name AS label
		  FROM event_subscription s JOIN tournament_event e ON e.id = s.event_id
		 WHERE s.chat_id = $1 AND s.active
		UNION ALL
		SELECT f.kind, NULL::uuid, f.team_id, COALESCE(f.player, ''),
		       COALESCE(NULLIF(f.label, ''), t.name, f.player, '')
		  FROM chat_follow f LEFT JOIN team t ON t.id = f.team_id
		 WHERE f.chat_id = $1 AND f.active
		 ORDER BY kind, label`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription.Target
	for rows.Next() {
		var t subscription.Target
		var kind string
		var eventID, teamID *uuid.UUID
		if err := rows.Scan(&kind, &eventID, &teamID, &t.Player, &t.Label); err != nil {
			return nil, err
		}
		t.Kind = subscription.TargetKind(kind)
		if eventID != nil {
			t.EventID = common.EventID{Value: *eventID}
		}
		if teamID != nil {
			t.TeamID = common.TeamID{Value: *teamID}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *SubscriptionRepository) ResolveTeams(ctx context.Context, targets []subscription.Target) (map[string][]common.TeamID, error) {
	out := map[string][]common.TeamID{}
	var players []string
	for _, t := range targets {
		switch t.Kind {
		case subscription.TargetTeam:
			out[t.Key()] = []common.TeamID{t.TeamID}
		case subscription.TargetPlayer:
			players = append(players, subscription.NormalizePlayer(t.Player))
		}
	}
	if len(players) == 0 {
		return out, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT DISTINCT lower(player), team_id FROM team_ranking_player WHERE lower(player) = ANY($1)`, players)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var player string
		var team uuid.UUID
		if err := rows.Scan(&player, &team); err != nil {
			return nil, err
		}
		key := subscription.PlayerTarget(player).Key()
		out[key] = append(out[key], common.TeamID{Value: team})
	}
	return out, rows.Err()
}

func (r *SubscriptionRepository) ChatsFollowingTeams(ctx context.Context, teamIDs []common.TeamID) ([]common.ChatID, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT DISTINCT f.chat_id FROM chat_follow f
		 WHERE f.active AND (
		       (f.kind = 'TEAM' AND f.team_id = ANY($1))
		    OR (f.kind = 'PLAYER' AND EXISTS (
		         SELECT 1 FROM team_ranking_player p WHERE p.team_id = ANY($1) AND lower(p.player) = f.player)))
		 ORDER BY f.chat_id`, teamUUIDs(teamIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.ChatID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.ChatID{Value: id})
	}
	return out, rows.Err()
}

// FollowedEventIDs covers live events already known to feature a followed
// team, plus S/A-tier live events as a discovery seed: a match only lands in
// the catalog once its event is synced. The two-day tail lets a
// just-finished event's last result land.
func (r *SubscriptionRepository) FollowedEventIDs(ctx context.Context) ([]common.EventID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		WITH teams AS (
		    SELECT team_id FROM chat_follow WHERE active AND kind = 'TEAM'
		    UNION
		    SELECT p.team_id FROM chat_follow f JOIN team_ranking_player p ON lower(p.player) = f.player
		     WHERE f.active AND f.kind = 'PLAYER'
		)
		SELECT e.id FROM tournament_event e
		 WHERE EXISTS (SELECT 1 FROM teams)
		   AND (e.status IN ('UPCOMING', 'RUNNING') OR e.ends_at > now() - interval '2 days')
		   AND (EXISTS (SELECT 1 FROM esport_match m JOIN match_team mt ON mt.match_id = m.id
		                 WHERE m.event_id = e.id AND mt.team_id IN (SELECT team_id FROM teams))
		        OR (e.tier IN ('s', 'a') AND e.status IN ('UPCOMING', 'RUNNING')))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.EventID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.EventID{Value: id})
	}
	return out, rows.Err()
}

func (r *SubscriptionRepository) SearchTargets(ctx context.Context, query string, limit int) ([]subscription.Target, error) {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	rows, err := executor(ctx, r.pool).Query(ctx, `
		(SELECT 'TEAM', id, name FROM team WHERE name ILIKE $1 ORDER BY length(name), name LIMIT $2)
		UNION ALL
		(SELECT 'PLAYER', NULL::uuid, min(player) FROM team_ranking_player WHERE player ILIKE $1
		  GROUP BY lower(player) ORDER BY min(length(player)), lower(player) LIMIT $2)`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription.Target
	for rows.Next() {
		var kind, name string
		var teamID *uuid.UUID
		if err := rows.Scan(&kind, &teamID, &name); err != nil {
			return nil, err
		}
		if kind == string(subscription.TargetTeam) && teamID != nil {
			out = append(out, subscription.TeamTarget(common.TeamID{Value: *teamID}, name))
			continue
		}
		out = append(out, subscription.PlayerTarget(name))
	}
	return out, rows.Err()
}

func (r *SubscriptionRepository) ClaimCrossSellOffer(ctx context.Context, chatID common.ChatID, eventID common.EventID, at time.Time, cooldown time.Duration) (bool, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO follow_cross_sell_offer(chat_id, event_id, offered_at)
		SELECT $1::bigint, $2::uuid, $3::timestamptz
		 WHERE NOT EXISTS (SELECT 1 FROM follow_cross_sell_offer WHERE chat_id = $1 AND offered_at > $4)
		ON CONFLICT (chat_id, event_id) DO NOTHING`,
		chatID.Value, eventID.Value, at, at.Add(-cooldown))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

var _ subscription.TargetRepository = (*SubscriptionRepository)(nil)
