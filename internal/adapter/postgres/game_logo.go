package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
)

var _ enrichment.GameLogoCache = (*EnrichmentRepository)(nil)

// PendingGameLogos compares what is stored with what is configured, so a
// publisher moving their artwork is noticed without re-downloading
// anything that has not moved.
func (r *EnrichmentRepository) PendingGameLogos(ctx context.Context) ([]enrichment.GameLogoNeed, error) {
	stored := map[competition.GameCode]string{}
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT game_code, source_url FROM game_logo_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, url string
		if err := rows.Scan(&code, &url); err != nil {
			return nil, err
		}
		stored[competition.GameCode(code)] = url
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Iterated over the game catalogue rather than over the map, so the
	// order is the same on every pass and a missing logo is fetched in a
	// predictable place.
	var out []enrichment.GameLogoNeed
	for _, game := range competition.Games {
		url, configured := enrichment.GameLogoSources[game]
		if !configured || stored[game] == url {
			continue
		}
		out = append(out, enrichment.GameLogoNeed{Game: game, SourceURL: url})
	}
	return out, nil
}

func (r *EnrichmentRepository) SaveGameLogo(ctx context.Context, logo enrichment.GameLogo) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `
		INSERT INTO game_logo_cache (game_code, source_url, content_type, bytes, digest, etag, last_modified, is_light, fetched_at, checked_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $9)
		ON CONFLICT (game_code) DO UPDATE SET
		  source_url = excluded.source_url, content_type = excluded.content_type,
		  bytes = excluded.bytes, digest = excluded.digest,
		  etag = excluded.etag, last_modified = excluded.last_modified,
		  is_light = excluded.is_light, fetched_at = excluded.fetched_at, checked_at = excluded.checked_at`,
		string(logo.Game), logo.SourceURL, logo.ContentType, logo.Bytes, logo.Digest,
		logo.ETag, logo.LastModified, logo.IsLight, logo.FetchedAt)
	return err
}

func (r *EnrichmentRepository) FindGameLogo(ctx context.Context, game competition.GameCode) (*enrichment.GameLogo, error) {
	var logo enrichment.GameLogo
	logo.Game = game
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT source_url, content_type, bytes, digest, is_light, fetched_at
		   FROM game_logo_cache WHERE game_code = $1`, string(game)).
		Scan(&logo.SourceURL, &logo.ContentType, &logo.Bytes, &logo.Digest, &logo.IsLight, &logo.FetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &logo, nil
}

func (r *EnrichmentRepository) GameLogoDigests(ctx context.Context) (map[competition.GameCode]string, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `SELECT game_code, digest FROM game_logo_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[competition.GameCode]string{}
	for rows.Next() {
		var code, digest string
		if err := rows.Scan(&code, &digest); err != nil {
			return nil, err
		}
		out[competition.GameCode(code)] = digest
	}
	return out, rows.Err()
}
