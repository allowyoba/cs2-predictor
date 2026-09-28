package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// RosterRepository implements competition.RosterRepository against player,
// player_identity and team_roster.
type RosterRepository struct {
	pool *pgxpool.Pool
}

func NewRosterRepository(pool *pgxpool.Pool) *RosterRepository {
	return &RosterRepository{pool: pool}
}

var _ competition.RosterRepository = (*RosterRepository)(nil)

const playerSelect = `SELECT p.id, g.code, p.nickname, p.full_name, p.nationality, p.image_url
                        FROM player p JOIN game g ON g.id = p.game_id`

// SavePlayer resolves the source's own key to a canonical player, inserting
// one the first time and updating the details after.
//
// Keyed on (source, external_id) rather than on the nickname: a player who
// changes nickname is the same person, and every subscription pointed at them
// has to survive that. The reverse — two people sharing a nickname — is why
// the nickname was never allowed to be the key in the first place.
func (r *RosterRepository) SavePlayer(ctx context.Context, game competition.GameCode, source, externalID string,
	player competition.Player) (competition.Player, error) {
	ex := executor(ctx, r.pool)
	var id common.PlayerID
	err := ex.QueryRow(ctx,
		`SELECT player_id FROM player_identity WHERE source = $1 AND external_id = $2`,
		source, externalID).Scan(&id.Value)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		id = common.NewPlayerID()
		if _, err := ex.Exec(ctx,
			`INSERT INTO player(id, game_id, nickname, full_name, nationality, image_url)
			 VALUES ($1, (SELECT id FROM game WHERE code = $2), $3, $4, $5, $6)`,
			id.Value, string(game), player.Nickname, player.FullName, player.Nationality, player.ImageURL); err != nil {
			return competition.Player{}, err
		}
		if _, err := ex.Exec(ctx,
			`INSERT INTO player_identity(player_id, source, external_id, external_name, confidence)
			 VALUES ($1, $2, $3, $4, 'exact_id')
			 ON CONFLICT (source, external_id) DO NOTHING`,
			id.Value, source, externalID, player.Nickname); err != nil {
			return competition.Player{}, err
		}
	case err != nil:
		return competition.Player{}, err
	default:
		if _, err := ex.Exec(ctx,
			`UPDATE player SET nickname = $2, full_name = $3, nationality = $4, image_url = $5, updated_at = now()
			  WHERE id = $1`,
			id.Value, player.Nickname, player.FullName, player.Nationality, player.ImageURL); err != nil {
			return competition.Player{}, err
		}
	}
	player.ID, player.Game = id, game
	return player, nil
}

// SavePlayerIdentity records a second feed's name for a player already on
// record — HLTV's nickname next to PandaScore's id. Separate from SavePlayer
// because this feed contributes no player of its own: it only ever recognises
// one the match provider already reported.
// A feed's key must name one person for "follow this nickname" to mean
// anything, so a key already held by somebody else is refused (claimed=false)
// rather than reassigned. That happens with genuine namesakes on two
// different teams, and handing the nickname to whichever roster synced last
// would silently move every subscription behind it to a stranger.
func (r *RosterRepository) SavePlayerIdentity(ctx context.Context, id common.PlayerID, source, externalID,
	externalName, confidence string) (claimed bool, err error) {
	ex := executor(ctx, r.pool)
	var owner common.PlayerID
	err = ex.QueryRow(ctx, `SELECT player_id FROM player_identity WHERE source = $1 AND external_id = $2`,
		source, externalID).Scan(&owner.Value)
	switch {
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return false, err
	case err == nil && owner != id:
		return false, nil
	}
	if _, err := ex.Exec(ctx,
		`INSERT INTO player_identity(player_id, source, external_id, external_name, confidence, matched_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (player_id, source) DO UPDATE SET
		   external_id = excluded.external_id, external_name = excluded.external_name,
		   confidence = excluded.confidence, matched_at = now()`,
		id.Value, source, externalID, externalName, confidence); err != nil {
		return false, err
	}
	return true, nil
}

// ReplaceRoster makes members the team's whole roster. Replace-the-set, for
// the reason SaveRanking replaces team_ranking_player: somebody who left is
// not on it any more, and a row nothing overwrote is not the same as a row
// that is true.
func (r *RosterRepository) ReplaceRoster(ctx context.Context, teamID common.TeamID, members []competition.RosterMember) error {
	ex := executor(ctx, r.pool)
	if _, err := ex.Exec(ctx, `DELETE FROM team_roster WHERE team_id = $1`, teamID.Value); err != nil {
		return err
	}
	for i, member := range members {
		if _, err := ex.Exec(ctx,
			`INSERT INTO team_roster(team_id, player_id, position, role, seen_at)
			 VALUES ($1, $2, $3, $4, now())
			 ON CONFLICT (team_id, player_id) DO UPDATE SET
			   position = excluded.position, role = excluded.role, seen_at = now()`,
			teamID.Value, member.Player.ID.Value, i+1, member.Role); err != nil {
			return err
		}
	}
	return nil
}

func (r *RosterRepository) Roster(ctx context.Context, teamID common.TeamID) ([]competition.RosterMember, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT p.id, g.code, p.nickname, p.full_name, p.nationality, p.image_url, tr.position, tr.role
		   FROM team_roster tr
		   JOIN player p ON p.id = tr.player_id
		   JOIN game g ON g.id = p.game_id
		  WHERE tr.team_id = $1
		  ORDER BY tr.position, p.nickname`, teamID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []competition.RosterMember
	for rows.Next() {
		var m competition.RosterMember
		var code string
		if err := rows.Scan(&m.Player.ID.Value, &code, &m.Player.Nickname, &m.Player.FullName,
			&m.Player.Nationality, &m.Player.ImageURL, &m.Position, &m.Role); err != nil {
			return nil, err
		}
		m.Player.Game = competition.GameCode(code)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *RosterRepository) FindPlayer(ctx context.Context, id common.PlayerID) (*competition.Player, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, playerSelect+` WHERE p.id = $1`, id.Value)
	player, err := scanPlayer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &player, nil
}

// SearchPlayers matches a case-insensitive substring of the nickname, the
// same shape SearchTeams uses over team names, and the same "no games
// enabled, no results" rule.
func (r *RosterRepository) SearchPlayers(ctx context.Context, query string, limit int,
	games []competition.GameCode) ([]competition.Player, error) {
	if len(games) == 0 {
		return nil, nil
	}
	if limit < 1 {
		limit = 1
	} else if limit > 1000 {
		limit = 1000
	}
	codes := make([]string, len(games))
	for i, g := range games {
		codes[i] = string(g)
	}
	rows, err := executor(ctx, r.pool).Query(ctx, playerSelect+`
		 WHERE p.nickname ILIKE ('%' || $1 || '%') ESCAPE '\'
		   AND g.code = ANY($3)
		 ORDER BY length(p.nickname), p.nickname LIMIT $2`,
		escapeLikePattern(query), limit, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []competition.Player
	for rows.Next() {
		player, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, player)
	}
	return out, rows.Err()
}

func (r *RosterRepository) TeamsOfPlayer(ctx context.Context, id common.PlayerID) ([]common.TeamID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT team_id FROM team_roster WHERE player_id = $1`, id.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.TeamID
	for rows.Next() {
		var teamID common.TeamID
		if err := rows.Scan(&teamID.Value); err != nil {
			return nil, err
		}
		out = append(out, teamID)
	}
	return out, rows.Err()
}

// PlayerIdentities lists the feeds already resolved for a player, so a match
// pass can tell a player HLTV has been paired with from one it has not.
func (r *RosterRepository) PlayerIdentities(ctx context.Context, source string, ids []common.PlayerID) (map[common.PlayerID]string, error) {
	if len(ids) == 0 {
		return map[common.PlayerID]string{}, nil
	}
	raw := make([][16]byte, len(ids))
	for i, id := range ids {
		raw[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT player_id, external_id FROM player_identity WHERE source = $1 AND player_id = ANY($2::uuid[])`,
		source, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[common.PlayerID]string{}
	for rows.Next() {
		var id common.PlayerID
		var externalID string
		if err := rows.Scan(&id.Value, &externalID); err != nil {
			return nil, err
		}
		out[id] = externalID
	}
	return out, rows.Err()
}

func scanPlayer(row interface{ Scan(dest ...any) error }) (competition.Player, error) {
	var player competition.Player
	var code string
	if err := row.Scan(&player.ID.Value, &code, &player.Nickname, &player.FullName,
		&player.Nationality, &player.ImageURL); err != nil {
		return competition.Player{}, err
	}
	player.Game = competition.GameCode(code)
	return player, nil
}
