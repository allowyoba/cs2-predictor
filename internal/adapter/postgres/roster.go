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
			`INSERT INTO player(id, game_id, nickname, full_name, nationality, image_url, search_key)
			 VALUES ($1, (SELECT id FROM game WHERE code = $2), $3, $4, $5, $6, $7)`,
			id.Value, string(game), player.Nickname, player.FullName, player.Nationality, player.ImageURL,
			playerSearchKey(player)); err != nil {
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
			`UPDATE player SET nickname = $2, full_name = $3, nationality = $4, image_url = $5,
			        search_key = $6, updated_at = now()
			  WHERE id = $1`,
			id.Value, player.Nickname, player.FullName, player.Nationality, player.ImageURL,
			playerSearchKey(player)); err != nil {
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
	// Same fold as the team search: a nickname written "r0pz" has to be
	// found by "ропз", and the real name is searchable too, because half the
	// time people know the person and not the handle.
	folded := competition.FoldForSearch(query)
	rows, err := executor(ctx, r.pool).Query(ctx, playerSelect+`
		 WHERE (p.search_key LIKE ('%' || $1 || '%') ESCAPE '\'
		        OR p.nickname ILIKE ('%' || $4 || '%') ESCAPE '\')
		   AND g.code = ANY($3)
		 ORDER BY length(p.nickname), p.nickname LIMIT $2`,
		escapeLikePattern(folded), searchFetchLimit(limit), codes, escapeLikePattern(query))
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rankSearchResults(out, folded, func(p competition.Player) string { return p.Nickname })
	return trimTo(out, limit), nil
}

// playerSearchKey covers both the handle and the real name: "ЗиуОо" should
// find ZywOo, and so should "Mathieu Herbaut".
func playerSearchKey(player competition.Player) string {
	key := competition.SearchKeyBlob(player.Nickname)
	if player.FullName == "" {
		return key
	}
	return key + competition.SearchKeyBlob(player.FullName)
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

// BackfillSearchKeys fills the folded key for rows that still have none —
// teams first, then players, so one call makes progress on whichever still
// needs it. See app.SearchKeyBackfill for why this is not a SQL expression.
func (r *RosterRepository) BackfillSearchKeys(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	filled, err := r.backfillTeams(ctx, limit)
	if err != nil || filled >= limit {
		return filled, err
	}
	players, err := r.backfillPlayers(ctx, limit-filled)
	return filled + players, err
}

func (r *RosterRepository) backfillTeams(ctx context.Context, limit int) (int, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id, name FROM team WHERE search_key = '' LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type row struct {
		id   [16]byte
		name string
	}
	var pending []row
	for rows.Next() {
		var t row
		if err := rows.Scan(&t.id, &t.name); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, t := range pending {
		// COALESCE to a single space for a nameless row: leaving it empty
		// would make this pass pick it up again on every run, forever.
		key := competition.SearchKeyBlob(t.name)
		if key == "" {
			key = " "
		}
		if _, err := executor(ctx, r.pool).Exec(ctx,
			`UPDATE team SET search_key = $2 WHERE id = $1`, t.id, key); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

func (r *RosterRepository) backfillPlayers(ctx context.Context, limit int) (int, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id, nickname, full_name FROM player WHERE search_key = '' LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type row struct {
		id       [16]byte
		nickname string
		fullName string
	}
	var pending []row
	for rows.Next() {
		var p row
		if err := rows.Scan(&p.id, &p.nickname, &p.fullName); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range pending {
		key := playerSearchKey(competition.Player{Nickname: p.nickname, FullName: p.fullName})
		if key == "" {
			key = " "
		}
		if _, err := executor(ctx, r.pool).Exec(ctx,
			`UPDATE player SET search_key = $2 WHERE id = $1`, p.id, key); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}
