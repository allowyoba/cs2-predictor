package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// CompetitionRepository implements competition.Catalog against
// tournament_event, event_stage, team, esport_match, and match_team.
// Providers/stages are auto-created on demand, and event_stage's id is a
// deterministic MD5 UUID
// (NameUUID("stage:<eventID>:<stageExternalID-or-name>")).
type CompetitionRepository struct {
	pool *pgxpool.Pool
}

func NewCompetitionRepository(pool *pgxpool.Pool) *CompetitionRepository {
	return &CompetitionRepository{pool: pool}
}

// gameID resolves a GameCode to game.id. Unlike providerID it never
// auto-inserts: game rows are seeded by migration (see 0001, 0032), so an
// unresolvable code means a GameCode the schema doesn't know about yet —
// a real bug, not a first-sight-of-a-new-provider situation.
func (r *CompetitionRepository) gameID(ctx context.Context, code competition.GameCode) (int16, error) {
	var id int16
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT id FROM game WHERE code = $1`, string(code)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("game code %q is not seeded in the game table", code)
	}
	return id, err
}

// gameCodes batch-resolves game ids to their codes in one round trip, for
// scanEvents — see providerCodes's doc comment for why batching matters here.
func (r *CompetitionRepository) gameCodes(ctx context.Context, ids []int16) (map[int16]competition.GameCode, error) {
	out := map[int16]competition.GameCode{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT id, code FROM game WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int16
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		out[id] = competition.GameCode(code)
	}
	return out, rows.Err()
}

func (r *CompetitionRepository) providerID(ctx context.Context, code string) (int16, error) {
	code = upper(code)
	var id int16
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT id FROM data_provider WHERE code = $1`, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = executor(ctx, r.pool).QueryRow(ctx,
			`INSERT INTO data_provider(code, display_name) VALUES ($1, $1) RETURNING id`, code).Scan(&id)
	}
	return id, err
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func (r *CompetitionRepository) providerCode(ctx context.Context, id int16) (string, error) {
	var code string
	err := executor(ctx, r.pool).QueryRow(ctx, `SELECT code FROM data_provider WHERE id = $1`, id).Scan(&code)
	return code, err
}

const eventSelect = `SELECT id, game_id, provider_id, external_id, name, status, starts_at, ends_at, tier FROM tournament_event`

// escapeLikePattern escapes the three characters that are special inside a
// SQL LIKE/ILIKE pattern (the wildcards '%' and '_', plus the escape
// character itself) so a search term is matched literally instead of as a
// pattern — pairs with the "ESCAPE '\'" clause in SearchEvents.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (r *CompetitionRepository) SearchEvents(ctx context.Context, query string, limit int, topTierOnly bool, games []competition.GameCode) ([]competition.Event, error) {
	if limit < 1 {
		limit = 1
	} else if limit > 1000 {
		limit = 1000
	}
	if len(games) == 0 {
		// A chat with no games enabled sees nothing — not an error, just
		// an empty result, same as "no events matched the search".
		return nil, nil
	}
	codes := make([]string, len(games))
	for i, g := range games {
		codes[i] = string(g)
	}
	// Event discovery is served entirely from the Postgres catalog: no
	// provider call happens while a user is browsing. ILIKE makes the search
	// explicitly case-insensitive, while the status/end-date predicates keep
	// finished or stale events out of the subscription picker.
	sql := eventSelect + `
		 WHERE name ILIKE ('%' || $1 || '%') ESCAPE '\'
		   AND status IN ('UPCOMING', 'RUNNING')
		   AND (ends_at IS NULL OR ends_at > now())
		   AND game_id IN (SELECT id FROM game WHERE code = ANY($3))`
	if topTierOnly {
		sql += ` AND tier IN ('s', 'a')`
	}
	sql += ` ORDER BY CASE status WHEN 'RUNNING' THEN 0 ELSE 1 END, starts_at ASC NULLS LAST, name ASC LIMIT $2`
	rows, err := executor(ctx, r.pool).Query(ctx, sql, escapeLikePattern(query), limit, codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return r.scanEvents(ctx, rows)
}

// scanEvents reads every row first (deferring the provider lookup) and then
// resolves all distinct provider ids in one batched query — not one
// providerCode call per row, which would be an N+1 query against
// data_provider on every multi-row event read (SearchEvents, FindEvents).
func (r *CompetitionRepository) scanEvents(ctx context.Context, rows pgx.Rows) ([]competition.Event, error) {
	var out []competition.Event
	var providerIDs, gameIDs []int16
	for rows.Next() {
		e, gameID, providerID, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		gameIDs = append(gameIDs, gameID)
		providerIDs = append(providerIDs, providerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	codes, err := r.providerCodes(ctx, providerIDs)
	if err != nil {
		return nil, err
	}
	games, err := r.gameCodes(ctx, gameIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Provider = codes[providerIDs[i]]
		out[i].Game = games[gameIDs[i]]
	}
	return out, nil
}

// providerCodes batch-resolves data_provider ids to their codes in one
// round trip, for scanEvents (see its doc comment).
func (r *CompetitionRepository) providerCodes(ctx context.Context, ids []int16) (map[int16]string, error) {
	out := map[int16]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT id, code FROM data_provider WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int16
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		out[id] = code
	}
	return out, rows.Err()
}

func scanEvent(row interface {
	Scan(dest ...any) error
}) (competition.Event, int16, int16, error) {
	var e competition.Event
	var gameID, providerID int16
	var tier *string
	if err := row.Scan(&e.ID.Value, &gameID, &providerID, &e.ExternalID, &e.Name, &e.Status, &e.StartsAt, &e.EndsAt, &tier); err != nil {
		return competition.Event{}, 0, 0, err
	}
	if tier != nil {
		e.Tier = competition.EventTier(*tier)
	}
	return e, gameID, providerID, nil
}

func (r *CompetitionRepository) FindEvent(ctx context.Context, id common.EventID) (*competition.Event, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, eventSelect+` WHERE id = $1`, id.Value)
	e, gameID, providerID, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	code, err := r.providerCode(ctx, providerID)
	if err != nil {
		return nil, err
	}
	e.Provider = code
	games, err := r.gameCodes(ctx, []int16{gameID})
	if err != nil {
		return nil, err
	}
	e.Game = games[gameID]
	return &e, nil
}

// FindEvents batch-fetches events by id in one round trip (see the Catalog
// doc comment for why: avoids an N+1 query pattern in list renderers).
func (r *CompetitionRepository) FindEvents(ctx context.Context, ids []common.EventID) ([]competition.Event, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	values := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx, eventSelect+` WHERE id = ANY($1)`, values)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return r.scanEvents(ctx, rows)
}

func (r *CompetitionRepository) SaveEvent(ctx context.Context, e competition.Event) (competition.Event, error) {
	providerID, err := r.providerID(ctx, e.Provider)
	if err != nil {
		return competition.Event{}, err
	}
	gameID, err := r.gameID(ctx, e.Game)
	if err != nil {
		return competition.Event{}, err
	}
	var tier *string
	if e.Tier != competition.TierUnknown {
		t := string(e.Tier)
		tier = &t
	}
	_, err = executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO tournament_event(id, game_id, provider_id, external_id, name, status, starts_at, ends_at, tier, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())
		 ON CONFLICT (id) DO UPDATE SET provider_id = excluded.provider_id, external_id = excluded.external_id,
		   name = excluded.name, status = excluded.status, starts_at = excluded.starts_at, ends_at = excluded.ends_at,
		   tier = excluded.tier, updated_at = now()`,
		e.ID.Value, gameID, providerID, e.ExternalID, e.Name, e.Status, e.StartsAt, e.EndsAt, tier)
	return e, err
}

// matchSelect fetches everything a domain Match needs — the match row, its
// stage (if any), and both participant teams — in a single round trip via
// LEFT JOINs, so listing N matches costs one query rather than N.
const matchSelect = `
	SELECT m.id, m.event_id, m.external_id, m.status, m.series_kind, m.series_size,
	       m.scheduled_at, m.actual_started_at, m.first_score, m.second_score, m.streams,
	       es.name, es.external_id,
	       t1.id, t1.name, t1.external_id, t1.location,
	       t2.id, t2.name, t2.external_id, t2.location
	  FROM esport_match m
	  LEFT JOIN event_stage es ON es.id = m.stage_id
	  LEFT JOIN match_team mt1 ON mt1.match_id = m.id AND mt1.position = 1
	  LEFT JOIN team t1 ON t1.id = mt1.team_id
	  LEFT JOIN match_team mt2 ON mt2.match_id = m.id AND mt2.position = 2
	  LEFT JOIN team t2 ON t2.id = mt2.team_id`

func scanMatch(row interface {
	Scan(dest ...any) error
}) (*competition.Match, error) {
	var m competition.Match
	var firstScore, secondScore *int
	var streamsRaw []byte
	var stageName, stageExternalID *string
	var t1ID, t2ID *[16]byte
	var t1Name, t1ExternalID, t1Location, t2Name, t2ExternalID, t2Location *string

	if err := row.Scan(&m.ID.Value, &m.EventID.Value, &m.ExternalID, &m.Status, &m.Format.Kind, &m.Format.Size,
		&m.ScheduledAt, &m.ActualStartedAt, &firstScore, &secondScore, &streamsRaw,
		&stageName, &stageExternalID,
		&t1ID, &t1Name, &t1ExternalID, &t1Location,
		&t2ID, &t2Name, &t2ExternalID, &t2Location); err != nil {
		return nil, err
	}
	if firstScore != nil && secondScore != nil {
		m.Score = &competition.MatchScore{First: *firstScore, Second: *secondScore}
	}
	if len(streamsRaw) > 0 {
		if err := json.Unmarshal(streamsRaw, &m.Streams); err != nil {
			return nil, fmt.Errorf("decode match streams: %w", err)
		}
	}
	if stageName != nil {
		m.Stage = stageName
		m.StageExternalID = stageExternalID
	}
	if t1ID != nil {
		m.FirstTeam = &competition.Team{ID: common.TeamID{Value: *t1ID}, Name: *t1Name, ExternalID: *t1ExternalID}
		if t1Location != nil {
			m.FirstTeam.Location = *t1Location
		}
	}
	if t2ID != nil {
		m.SecondTeam = &competition.Team{ID: common.TeamID{Value: *t2ID}, Name: *t2Name, ExternalID: *t2ExternalID}
		if t2Location != nil {
			m.SecondTeam.Location = *t2Location
		}
	}
	return &m, nil
}

func (r *CompetitionRepository) FindMatch(ctx context.Context, id common.MatchID) (*competition.Match, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, matchSelect+` WHERE m.id = $1`, id.Value)
	m, err := scanMatch(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

func (r *CompetitionRepository) findMatches(ctx context.Context, query string, args ...any) ([]competition.Match, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, matchSelect+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]competition.Match, 0)
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *CompetitionRepository) FindUnstartedMatches(ctx context.Context, eventID common.EventID) ([]competition.Match, error) {
	return r.findMatches(ctx, ` WHERE m.event_id = $1 AND m.status = $2`, eventID.Value, competition.MatchNotStarted)
}

// FindUnstartedMatchesForEvents batch-fetches not-started matches across
// several events in one round trip (see the Catalog doc comment for why).
func (r *CompetitionRepository) FindUnstartedMatchesForEvents(ctx context.Context, eventIDs []common.EventID) ([]competition.Match, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	values := make([]uuid.UUID, len(eventIDs))
	for i, id := range eventIDs {
		values[i] = id.Value
	}
	return r.findMatches(ctx, ` WHERE m.event_id = ANY($1) AND m.status = $2`, values, competition.MatchNotStarted)
}

// EventsWithLiveMatches implements competition.LiveMatchCatalog: the ids of
// whichever supplied events have a match in play. One narrow query rather
// than loading every match of every event, because the caller only needs
// the yes/no per event.
func (r *CompetitionRepository) EventsWithLiveMatches(ctx context.Context, eventIDs []common.EventID) ([]common.EventID, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	values := make([]uuid.UUID, len(eventIDs))
	for i, id := range eventIDs {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT DISTINCT event_id FROM match WHERE event_id = ANY($1) AND status = $2`,
		values, competition.MatchRunning)
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

func (r *CompetitionRepository) FindMatches(ctx context.Context, eventID common.EventID) ([]competition.Match, error) {
	return r.findMatches(ctx, ` WHERE m.event_id = $1`, eventID.Value)
}

// SaveMatch upserts the match's teams, its stage (deterministic UUID,
// auto-created on demand), and the match row itself (preserving version and
// created_at from any existing row), then fully replaces match_team — all
// inside one transaction, so a failure partway through never leaves the
// match row and its participant rows inconsistent (e.g. match_team deleted
// but not yet re-inserted).
//
//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (r *CompetitionRepository) SaveMatch(ctx context.Context, m competition.Match) (competition.Match, error) {
	err := RunInTx(ctx, r.pool, func(ctx context.Context) error {
		event, err := r.FindEvent(ctx, m.EventID)
		if err != nil {
			return err
		}
		if event == nil {
			return fmt.Errorf("%w: %s", competition.ErrEventNotFound, m.EventID.Value)
		}
		providerID, err := r.providerID(ctx, event.Provider)
		if err != nil {
			return err
		}
		gameID, err := r.gameID(ctx, event.Game)
		if err != nil {
			return err
		}

		ex := executor(ctx, r.pool)

		if m.FirstTeam != nil {
			if err := r.saveTeam(ctx, gameID, providerID, *m.FirstTeam); err != nil {
				return err
			}
		}
		if m.SecondTeam != nil {
			if err := r.saveTeam(ctx, gameID, providerID, *m.SecondTeam); err != nil {
				return err
			}
		}

		var stageUUID *[16]byte
		if m.Stage != nil {
			key := *m.Stage
			if m.StageExternalID != nil {
				key = *m.StageExternalID
			}
			id := common.NameUUID("stage:" + m.EventID.Value.String() + ":" + key)
			externalID := key
			if m.StageExternalID != nil {
				externalID = *m.StageExternalID
			}
			if _, err := ex.Exec(ctx,
				`INSERT INTO event_stage(id, event_id, provider_id, external_id, name) VALUES ($1, $2, $3, $4, $5)
				 ON CONFLICT (provider_id, external_id) DO UPDATE SET name = excluded.name`,
				id, m.EventID.Value, providerID, externalID, *m.Stage); err != nil {
				return err
			}
			var b [16]byte
			copy(b[:], id[:])
			stageUUID = &b
		}

		// No existing row (ErrNoRows) is the expected case for a brand-new
		// match and simply starts version at 0 -> 1; any other error (a
		// lost connection, a broken query) must fail the save rather than
		// silently proceeding as if the match were new, which would let a
		// concurrent update's version get overwritten unnoticed.
		var version int64
		if err := ex.QueryRow(ctx, `SELECT version FROM esport_match WHERE id = $1`, m.ID.Value).Scan(&version); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		version++

		var firstScore, secondScore *int
		if m.Score != nil {
			firstScore = &m.Score.First
			secondScore = &m.Score.Second
		}
		var streamsRaw []byte
		if len(m.Streams) > 0 {
			var err error
			streamsRaw, err = json.Marshal(m.Streams)
			if err != nil {
				return err
			}
		}

		if _, err := ex.Exec(ctx,
			`INSERT INTO esport_match(id, event_id, stage_id, provider_id, external_id, status, series_kind, series_size,
			    scheduled_at, actual_started_at, first_score, second_score, streams, version, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, now(), now())
			 ON CONFLICT (id) DO UPDATE SET event_id=excluded.event_id, stage_id=excluded.stage_id,
			   provider_id=excluded.provider_id, external_id=excluded.external_id, status=excluded.status,
			   series_kind=excluded.series_kind, series_size=excluded.series_size, scheduled_at=excluded.scheduled_at,
			   actual_started_at=excluded.actual_started_at, first_score=excluded.first_score, second_score=excluded.second_score,
			   streams=excluded.streams, version=excluded.version, updated_at=now()`,
			m.ID.Value, m.EventID.Value, stageUUID, providerID, m.ExternalID, m.Status, m.Format.Kind, m.Format.Size,
			m.ScheduledAt, m.ActualStartedAt, firstScore, secondScore, streamsRaw, version); err != nil {
			return err
		}

		if _, err := ex.Exec(ctx, `DELETE FROM match_team WHERE match_id = $1`, m.ID.Value); err != nil {
			return err
		}
		if m.FirstTeam != nil {
			if _, err := ex.Exec(ctx, `INSERT INTO match_team(match_id, position, team_id) VALUES ($1, 1, $2)`, m.ID.Value, m.FirstTeam.ID.Value); err != nil {
				return err
			}
		}
		if m.SecondTeam != nil {
			if _, err := ex.Exec(ctx, `INSERT INTO match_team(match_id, position, team_id) VALUES ($1, 2, $2)`, m.ID.Value, m.SecondTeam.ID.Value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return competition.Match{}, err
	}
	return m, nil
}

func (r *CompetitionRepository) saveTeam(ctx context.Context, gameID, providerID int16, t competition.Team) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO team(id, game_id, provider_id, external_id, name, location, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), now(), now())
		 ON CONFLICT (id) DO UPDATE SET name = excluded.name, location = COALESCE(excluded.location, team.location), updated_at = now()`,
		t.ID.Value, gameID, providerID, t.ExternalID, t.Name, t.Location)
	return err
}

// MatchFacts derives compact pre-match facts from the local PostgreSQL cache,
// scoped to the already-loaded match (the caller is expected to have fetched
// it already, e.g. via FindMatch, so this doesn't repeat that lookup). The
// balance is scoped to the current event/series, which is complete for a
// subscribed event and therefore does not require extra PandaScore requests.
func (r *CompetitionRepository) MatchFacts(ctx context.Context, m *competition.Match) (competition.MatchFacts, error) {
	var facts competition.MatchFacts
	var err error
	if m.FirstTeam != nil {
		facts.FirstEventBalance, err = r.eventTeamBalance(ctx, m.EventID, m.FirstTeam.ID, m.ID)
		if err != nil {
			return competition.MatchFacts{}, err
		}
	}
	if m.SecondTeam != nil {
		facts.SecondEventBalance, err = r.eventTeamBalance(ctx, m.EventID, m.SecondTeam.ID, m.ID)
		if err != nil {
			return competition.MatchFacts{}, err
		}
	}
	return facts, nil
}

func (r *CompetitionRepository) eventTeamBalance(ctx context.Context, eventID common.EventID, teamID common.TeamID, beforeMatch common.MatchID) (competition.TeamBalance, error) {
	var b competition.TeamBalance
	err := executor(ctx, r.pool).QueryRow(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE (mt.position = 1 AND m.first_score > m.second_score) OR (mt.position = 2 AND m.second_score > m.first_score)) AS wins,
		  COUNT(*) FILTER (WHERE (mt.position = 1 AND m.first_score < m.second_score) OR (mt.position = 2 AND m.second_score < m.first_score)) AS losses,
		  COUNT(*) FILTER (WHERE m.first_score = m.second_score) AS draws
		FROM esport_match m
		JOIN match_team mt ON mt.match_id = m.id
		JOIN esport_match target ON target.id = $3
		WHERE m.event_id = $1
		  AND mt.team_id = $2
		  AND m.status = 'FINISHED'
		  AND m.first_score IS NOT NULL AND m.second_score IS NOT NULL
		  AND (target.scheduled_at IS NULL OR m.scheduled_at IS NULL OR m.scheduled_at < target.scheduled_at)`,
		eventID.Value, teamID.Value, beforeMatch.Value).Scan(&b.Wins, &b.Losses, &b.Draws)
	return b, err
}
