package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// The local copy of every team crest — see enrichment.TeamLogoCache for
// why the bot serves them itself instead of pointing browsers at somebody
// else's CDN.

var _ enrichment.TeamLogoCache = (*EnrichmentRepository)(nil)

// PendingLogos lists the crests the mirror does not have, or has from a
// URL the catalogue has since replaced.
//
// The two sources are unioned rather than queried separately so the
// per-pass cap means "this many requests", not "this many per source" —
// the cap exists to bound outbound traffic, and a cap that quietly doubles
// is not a cap.
func (r *EnrichmentRepository) PendingLogos(ctx context.Context, limit int) ([]enrichment.TeamLogoNeed, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		WITH wanted AS (
		    SELECT id AS team_id, 'PANDASCORE' AS source, logo_url AS url, updated_at
		      FROM team WHERE COALESCE(logo_url, '') <> ''
		    UNION ALL
		    SELECT id, 'HLTV', hltv_logo_url, updated_at
		      FROM team WHERE COALESCE(hltv_logo_url, '') <> ''
		)
		SELECT w.team_id, w.source, w.url, '', ''
		  FROM wanted w
		  LEFT JOIN team_logo_cache c ON c.team_id = w.team_id AND c.source = w.source
		 WHERE c.team_id IS NULL OR c.source_url IS DISTINCT FROM w.url
		 ORDER BY w.updated_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []enrichment.TeamLogoNeed
	for rows.Next() {
		var need enrichment.TeamLogoNeed
		var source string
		if err := rows.Scan(&need.TeamID.Value, &source, &need.SourceURL, &need.ETag, &need.LastModified); err != nil {
			return nil, err
		}
		need.Source = enrichment.Source(source)
		out = append(out, need)
	}
	return out, rows.Err()
}

func (r *EnrichmentRepository) SaveLogo(ctx context.Context, logo enrichment.TeamLogo) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO team_logo_cache (team_id, source, source_url, content_type, bytes, digest, etag, last_modified, is_light, fetched_at, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $10)
		ON CONFLICT (team_id, source) DO UPDATE SET
		  source_url = excluded.source_url, content_type = excluded.content_type,
		  bytes = excluded.bytes, digest = excluded.digest,
		  etag = excluded.etag, last_modified = excluded.last_modified,
		  is_light = excluded.is_light,
		  fetched_at = excluded.fetched_at, checked_at = excluded.checked_at`,
		logo.TeamID.Value, string(logo.Source), logo.SourceURL, logo.ContentType,
		logo.Bytes, logo.Digest, logo.ETag, logo.LastModified, logo.IsLight, logo.FetchedAt)
	return err
}

// StaleLogos finds crests worth re-asking about: a team with a match
// starting inside the window whose picture has not been checked lately.
//
// Ordered by how long it has been, so a pass always spends its budget on
// whatever is most out of date rather than re-checking the same few rows.
func (r *EnrichmentRepository) StaleLogos(ctx context.Context, notBefore time.Time, within time.Duration, limit int) ([]enrichment.TeamLogoNeed, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.team_id, c.source, c.source_url, COALESCE(c.etag, ''), COALESCE(c.last_modified, '')
		  FROM team_logo_cache c
		 WHERE c.checked_at < $1
		   AND EXISTS (
		       SELECT 1
		         FROM match_team mt
		         JOIN esport_match m ON m.id = mt.match_id
		        WHERE mt.team_id = c.team_id
		          AND m.status IN ('NOT_STARTED', 'RUNNING')
		          AND m.scheduled_at IS NOT NULL
		          AND m.scheduled_at <= now() + $2::interval)
		 ORDER BY c.checked_at
		 LIMIT $3`, notBefore, within.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []enrichment.TeamLogoNeed
	for rows.Next() {
		var need enrichment.TeamLogoNeed
		var source string
		if err := rows.Scan(&need.TeamID.Value, &source, &need.SourceURL, &need.ETag, &need.LastModified); err != nil {
			return nil, err
		}
		need.Source = enrichment.Source(source)
		out = append(out, need)
	}
	return out, rows.Err()
}

// TouchLogo records an unchanged answer.
func (r *EnrichmentRepository) TouchLogo(ctx context.Context, teamID common.TeamID, source enrichment.Source, at time.Time) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE team_logo_cache SET checked_at = $3 WHERE team_id = $1 AND source = $2`,
		teamID.Value, string(source), at)
	return err
}

func (r *EnrichmentRepository) FindLogo(ctx context.Context, teamID common.TeamID, source enrichment.Source) (*enrichment.TeamLogo, error) {
	var logo enrichment.TeamLogo
	logo.TeamID, logo.Source = teamID, source
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT source_url, content_type, bytes, digest, is_light, fetched_at
		   FROM team_logo_cache WHERE team_id = $1 AND source = $2`,
		teamID.Value, string(source)).
		Scan(&logo.SourceURL, &logo.ContentType, &logo.Bytes, &logo.Digest, &logo.IsLight, &logo.FetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &logo, nil
}

// LogoChips reports which marks are light, for the chip behind them. Only
// the measured ones appear: an absent entry means "not measured", which
// the app renders as it always did rather than as a guess.
func (r *EnrichmentRepository) LogoChips(ctx context.Context) (map[common.TeamID]map[enrichment.Source]bool, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT team_id, source, is_light FROM team_logo_cache WHERE is_light IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[common.TeamID]map[enrichment.Source]bool{}
	for rows.Next() {
		var teamID common.TeamID
		var source string
		var light bool
		if err := rows.Scan(&teamID.Value, &source, &light); err != nil {
			return nil, err
		}
		if out[teamID] == nil {
			out[teamID] = map[enrichment.Source]bool{}
		}
		out[teamID][enrichment.Source(source)] = light
	}
	return out, rows.Err()
}

// LogoDigests reads every digest without the bytes behind them: the teams
// endpoint needs to build one URL per team, not to serve the images.
func (r *EnrichmentRepository) LogoDigests(ctx context.Context) (map[common.TeamID]map[enrichment.Source]string, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT team_id, source, digest FROM team_logo_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[common.TeamID]map[enrichment.Source]string{}
	for rows.Next() {
		var teamID common.TeamID
		var source, digest string
		if err := rows.Scan(&teamID.Value, &source, &digest); err != nil {
			return nil, err
		}
		if out[teamID] == nil {
			out[teamID] = map[enrichment.Source]string{}
		}
		out[teamID][enrichment.Source(source)] = digest
	}
	return out, rows.Err()
}
