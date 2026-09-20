package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// EnrichmentRepository implements enrichment.RankingRepository,
// enrichment.IdentityRepository, enrichment.SyncStateRepository, and
// enrichment.TeamLister against team_ranking, team_external_identity,
// team_alias, provider_sync_state, and the pre-existing team table.
type EnrichmentRepository struct {
	pool *pgxpool.Pool
}

func NewEnrichmentRepository(pool *pgxpool.Pool) *EnrichmentRepository {
	return &EnrichmentRepository{pool: pool}
}

var (
	_ enrichment.RankingRepository            = (*EnrichmentRepository)(nil)
	_ enrichment.IdentityRepository           = (*EnrichmentRepository)(nil)
	_ enrichment.SyncStateRepository          = (*EnrichmentRepository)(nil)
	_ enrichment.TeamLister                   = (*EnrichmentRepository)(nil)
	_ enrichment.FormRepository               = (*EnrichmentRepository)(nil)
	_ enrichment.HeadToHeadRepository         = (*EnrichmentRepository)(nil)
	_ enrichment.TournamentMetadataRepository = (*EnrichmentRepository)(nil)
)

// --- enrichment.TeamLister ---

// ListTeams returns the known teams of the given games — the whole catalog
// when none are named. A ranking sync passes the games its feed actually
// covers: "BetBoom Team" exists in both CS2 and Dota 2, and matching the
// Counter-Strike rankings against the whole table attaches a CS2 ranking
// to a Dota 2 roster that shares nothing with it but a sponsor.
func (r *EnrichmentRepository) ListTeams(ctx context.Context, games ...competition.GameCode) ([]competition.Team, error) {
	query := `SELECT t.id, t.name, t.external_id, COALESCE(t.location, '') FROM team t`
	var args []any
	if len(games) > 0 {
		codes := make([]string, len(games))
		for i, g := range games {
			codes[i] = string(g)
		}
		query += ` JOIN game g ON g.id = t.game_id WHERE g.code = ANY($1)`
		args = append(args, codes)
	}
	rows, err := executor(ctx, r.pool).Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []competition.Team
	for rows.Next() {
		var t competition.Team
		if err := rows.Scan(&t.ID.Value, &t.Name, &t.ExternalID, &t.Location); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetRankingLogo implements enrichment.TeamLogoWriter: it stores a ranking
// feed's own crest, in its own column, leaving the match provider's
// untouched — which of the two is shown is a display preference, decided
// per chat, not something a sync job gets to overwrite.
//
// Only HLTV publishes one today; a source that does not is ignored rather
// than quietly writing into a column that is not its own. IS DISTINCT FROM
// keeps the weekly refresh from rewriting rows that already match, and
// treats a NULL logo as different from a real one.
func (r *EnrichmentRepository) SetRankingLogo(ctx context.Context, teamID common.TeamID, source enrichment.Source, logoURL string) error {
	if logoURL == "" || source != enrichment.SourceHLTV {
		return nil
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE team SET hltv_logo_url = $2, updated_at = now()
		  WHERE id = $1 AND hltv_logo_url IS DISTINCT FROM $2`, teamID.Value, logoURL)
	return err
}

// --- enrichment.RankingRepository ---

func (r *EnrichmentRepository) SaveRanking(ctx context.Context, ranking enrichment.TeamRanking) error {
	raw, err := json.Marshal(ranking)
	if err != nil {
		return err
	}
	_, err = executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_ranking(team_id, source, global_rank, regional_rank, region, points, roster, published_at, fetched_at, raw_payload)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, now(), $9)
		ON CONFLICT (team_id, source) DO UPDATE SET
		  global_rank = excluded.global_rank, regional_rank = excluded.regional_rank, region = excluded.region,
		  points = excluded.points, roster = excluded.roster, published_at = excluded.published_at,
		  fetched_at = now(), raw_payload = excluded.raw_payload`,
		ranking.TeamID.Value, string(ranking.Source), ranking.GlobalRank, ranking.RegionalRank, ranking.Region,
		ranking.Points, ranking.Roster, ranking.PublishedAt, raw)
	return err
}

const rankingSelect = `SELECT team_id, global_rank, regional_rank, COALESCE(region, ''), points, roster, published_at FROM team_ranking`

func scanRanking(row interface {
	Scan(dest ...any) error
}, source enrichment.Source) (enrichment.TeamRanking, error) {
	var rk enrichment.TeamRanking
	rk.Source = source
	if err := row.Scan(&rk.TeamID.Value, &rk.GlobalRank, &rk.RegionalRank, &rk.Region, &rk.Points, &rk.Roster, &rk.PublishedAt); err != nil {
		return enrichment.TeamRanking{}, err
	}
	return rk, nil
}

func (r *EnrichmentRepository) FindRanking(ctx context.Context, teamID common.TeamID, source enrichment.Source) (*enrichment.TeamRanking, error) {
	row := executor(ctx, r.pool).QueryRow(ctx, rankingSelect+` WHERE team_id = $1 AND source = $2`, teamID.Value, string(source))
	rk, err := scanRanking(row, source)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rk, nil
}

// FindRankings batch-fetches rankings for several teams in one round trip
// — used to render a match's two sides without two separate lookups.
func (r *EnrichmentRepository) FindRankings(ctx context.Context, teamIDs []common.TeamID, source enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}
	values := make([]uuid.UUID, len(teamIDs))
	for i, id := range teamIDs {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx, rankingSelect+` WHERE team_id = ANY($1) AND source = $2`, values, string(source))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[common.TeamID]enrichment.TeamRanking, len(teamIDs))
	for rows.Next() {
		rk, err := scanRanking(rows, source)
		if err != nil {
			return nil, err
		}
		out[rk.TeamID] = rk
	}
	return out, rows.Err()
}

// --- enrichment.IdentityRepository ---

func (r *EnrichmentRepository) FindTeamByExternalID(ctx context.Context, source enrichment.Source, externalID string) (*common.TeamID, error) {
	var id uuid.UUID
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT team_id FROM team_external_identity WHERE provider = $1 AND external_id = $2`,
		string(source), externalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &common.TeamID{Value: id}, nil
}

func (r *EnrichmentRepository) SaveIdentity(ctx context.Context, teamID common.TeamID, source enrichment.Source, externalID, externalName string, confidence enrichment.MatchConfidence) error {
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		ex := executor(ctx, r.pool)
		if _, err := ex.Exec(ctx, `
			INSERT INTO team_external_identity(team_id, provider, external_id, external_name, confidence, last_verified_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (provider, external_id) DO UPDATE SET
			  team_id = excluded.team_id, external_name = excluded.external_name,
			  confidence = excluded.confidence, last_verified_at = now()`,
			teamID.Value, string(source), externalID, externalName, string(confidence)); err != nil {
			return err
		}
		normalized := enrichment.NormalizeTeamName(externalName)
		_, err := ex.Exec(ctx, `
			INSERT INTO team_alias(team_id, alias, normalized_alias) VALUES ($1, $2, $3)
			ON CONFLICT (team_id, normalized_alias) DO NOTHING`,
			teamID.Value, externalName, normalized)
		return err
	})
}

func (r *EnrichmentRepository) Aliases(ctx context.Context, teamID common.TeamID) ([]string, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT alias FROM team_alias WHERE team_id = $1`, teamID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		out = append(out, alias)
	}
	return out, rows.Err()
}

func (r *EnrichmentRepository) AllAliases(ctx context.Context) (map[common.TeamID][]string, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT team_id, alias FROM team_alias`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[common.TeamID][]string{}
	for rows.Next() {
		var teamID common.TeamID
		var alias string
		if err := rows.Scan(&teamID.Value, &alias); err != nil {
			return nil, err
		}
		out[teamID] = append(out[teamID], alias)
	}
	return out, rows.Err()
}

// --- common.ReleaseAnnouncementStore ---

// Claim records this build as announced and reports whether this caller is
// the one that got there first. Insert-and-check rather than select-then-
// insert: two instances starting at once must not both announce.
func (r *EnrichmentRepository) Claim(ctx context.Context, version, commit string) (bool, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO release_announcement(version, commit_sha) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		version, commit)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// --- enrichment.ProviderRunRepository ---

func (r *EnrichmentRepository) SaveRun(ctx context.Context, run enrichment.ProviderRun) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO provider_run(provider, key, run_id, status, attempts, period_start, started_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (provider, key) DO UPDATE SET
		  run_id = excluded.run_id, status = excluded.status, attempts = excluded.attempts,
		  period_start = excluded.period_start, started_at = excluded.started_at, updated_at = now()`,
		string(run.Provider), run.Key, run.RunID, run.Status, run.Attempts, run.PeriodStart, run.StartedAt)
	return err
}

func (r *EnrichmentRepository) Run(ctx context.Context, provider enrichment.Source, key string) (*enrichment.ProviderRun, error) {
	run := enrichment.ProviderRun{Provider: provider, Key: key}
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT run_id, status, attempts, period_start, started_at FROM provider_run WHERE provider = $1 AND key = $2`,
		string(provider), key).Scan(&run.RunID, &run.Status, &run.Attempts, &run.PeriodStart, &run.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // nothing in flight — not an error
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *EnrichmentRepository) ClearRun(ctx context.Context, provider enrichment.Source, key string) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM provider_run WHERE provider = $1 AND key = $2`, string(provider), key)
	return err
}

// --- enrichment.SyncStateRepository ---

func (r *EnrichmentRepository) RecordSuccess(ctx context.Context, provider enrichment.Source) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO provider_sync_state(provider, last_success_at, consecutive_failures)
		VALUES ($1, now(), 0)
		ON CONFLICT (provider) DO UPDATE SET last_success_at = now(), consecutive_failures = 0`,
		string(provider))
	return err
}

func (r *EnrichmentRepository) RecordFailure(ctx context.Context, provider enrichment.Source, errText string) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO provider_sync_state(provider, last_error_at, last_error, consecutive_failures)
		VALUES ($1, now(), $2, 1)
		ON CONFLICT (provider) DO UPDATE SET
		  last_error_at = now(), last_error = excluded.last_error,
		  consecutive_failures = provider_sync_state.consecutive_failures + 1`,
		string(provider), errText)
	return err
}

func (r *EnrichmentRepository) State(ctx context.Context, provider enrichment.Source) (*enrichment.SyncState, error) {
	var st enrichment.SyncState
	st.Provider = provider
	var lastSuccess, lastError *time.Time
	var lastErrText *string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT last_success_at, last_error_at, last_error, consecutive_failures FROM provider_sync_state WHERE provider = $1`,
		string(provider)).Scan(&lastSuccess, &lastError, &lastErrText, &st.ConsecutiveFailures)
	if errors.Is(err, pgx.ErrNoRows) {
		return &st, nil // never synced yet — zero-value state, not an error
	}
	if err != nil {
		return nil, err
	}
	st.LastSuccessAt = lastSuccess
	st.LastErrorAt = lastError
	if lastErrText != nil {
		st.LastError = *lastErrText
	}
	return &st, nil
}

// --- enrichment.FormRepository ---

func (r *EnrichmentRepository) SaveForm(ctx context.Context, teamID common.TeamID, form enrichment.RecentForm) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_form(team_id, source, wins, losses, sample, fetched_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (team_id, source) DO UPDATE SET
		  wins = excluded.wins, losses = excluded.losses, sample = excluded.sample, fetched_at = now()`,
		teamID.Value, string(form.Source), form.Wins, form.Losses, form.Sample)
	return err
}

func (r *EnrichmentRepository) FindForm(ctx context.Context, teamID common.TeamID, source enrichment.Source) (*enrichment.RecentForm, error) {
	var f enrichment.RecentForm
	f.Source = source
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT wins, losses, sample FROM team_form WHERE team_id = $1 AND source = $2`,
		teamID.Value, string(source)).Scan(&f.Wins, &f.Losses, &f.Sample)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// --- enrichment.HeadToHeadRepository ---

// orderedPair returns (a, b) sorted so the same unordered pair always maps
// to the same storage order — the pair (teamA, teamB) and (teamB, teamA)
// must resolve to one cached row, not two.
func orderedPair(teamA, teamB common.TeamID) (common.TeamID, common.TeamID, bool) {
	if teamA.Value.String() <= teamB.Value.String() {
		return teamA, teamB, false
	}
	return teamB, teamA, true
}

func (r *EnrichmentRepository) SaveHeadToHead(ctx context.Context, teamA, teamB common.TeamID, h2h enrichment.HeadToHead) error {
	low, high, swapped := orderedPair(teamA, teamB)
	aWins, bWins := h2h.TeamAWins, h2h.TeamBWins
	if swapped {
		aWins, bWins = bWins, aWins
	}
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO head_to_head(team_a_id, team_b_id, source, team_a_wins, team_b_wins, sample, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (team_a_id, team_b_id, source) DO UPDATE SET
		  team_a_wins = excluded.team_a_wins, team_b_wins = excluded.team_b_wins,
		  sample = excluded.sample, fetched_at = now()`,
		low.Value, high.Value, string(h2h.Source), aWins, bWins, h2h.Sample)
	return err
}

func (r *EnrichmentRepository) FindHeadToHead(ctx context.Context, teamA, teamB common.TeamID, source enrichment.Source) (*enrichment.HeadToHead, error) {
	low, high, swapped := orderedPair(teamA, teamB)
	var h2h enrichment.HeadToHead
	h2h.Source = source
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT team_a_wins, team_b_wins, sample FROM head_to_head WHERE team_a_id = $1 AND team_b_id = $2 AND source = $3`,
		low.Value, high.Value, string(source)).Scan(&h2h.TeamAWins, &h2h.TeamBWins, &h2h.Sample)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if swapped {
		h2h.TeamAWins, h2h.TeamBWins = h2h.TeamBWins, h2h.TeamAWins
	}
	return &h2h, nil
}

// --- enrichment.TournamentMetadataRepository ---

func (r *EnrichmentRepository) SaveTournamentMetadata(ctx context.Context, eventID common.EventID, meta enrichment.TournamentMetadata, source enrichment.Source) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO tournament_metadata(event_id, source, full_name, series, region, stage, fetched_at)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), now())
		ON CONFLICT (event_id, source) DO UPDATE SET
		  full_name = excluded.full_name, series = excluded.series, region = excluded.region,
		  stage = excluded.stage, fetched_at = now()`,
		eventID.Value, string(source), meta.FullName, meta.Series, meta.Region, meta.Stage)
	return err
}

func (r *EnrichmentRepository) FindTournamentMetadata(ctx context.Context, eventID common.EventID, source enrichment.Source) (*enrichment.TournamentMetadata, error) {
	var meta enrichment.TournamentMetadata
	var fullName, series, region, stage *string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT full_name, series, region, stage FROM tournament_metadata WHERE event_id = $1 AND source = $2`,
		eventID.Value, string(source)).Scan(&fullName, &series, &region, &stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fullName != nil {
		meta.FullName = *fullName
	}
	if series != nil {
		meta.Series = *series
	}
	if region != nil {
		meta.Region = *region
	}
	if stage != nil {
		meta.Stage = *stage
	}
	return &meta, nil
}
