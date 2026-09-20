package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// LogoMirror copies team crests to this bot's own storage, one at a time,
// slowly, and never again once it has them.
//
// The whole point is to be unnoticeable to the people we are copying from.
// Everything here is shaped by that:
//
//   - one request at a time, never concurrent, with a deliberate pause
//     between them — a burst of thirty parallel fetches is what makes a
//     scraper look like an attack even when it is thirty small files;
//   - a hard cap on how many are fetched per pass, so a catalogue that
//     suddenly grows does not turn into a flood;
//   - a size cap and a short timeout, so one hostile or broken response
//     cannot hold the job open or fill the disk;
//   - a User-Agent that says who this is and how to reach us, because an
//     operator who wants it stopped should not have to guess;
//   - only ever fetching a URL the catalogue already holds, which the
//     provider published for this team, and only when we do not already
//     have that exact URL's bytes.
//
// A failure is logged and left for the next pass. There is no retry loop:
// a crest is decoration, and the fallback — a team's initials — is already
// what the app draws for teams nobody published a picture for.
type LogoMirror struct {
	Cache  enrichment.TeamLogoCache
	Client *http.Client
	Clock  common.Clock
	Lock   common.ClusterLock
	Log    *slog.Logger

	// MaxPerRun bounds one pass; zero uses LogoMirrorMaxPerRun.
	MaxPerRun int
	// Pause is the gap between two fetches; zero uses LogoMirrorPause.
	// Never set this to zero meaning "no pause" — pass a tiny duration in
	// tests instead, so the shape of the code stays honest about waiting.
	Pause time.Duration
	// RecheckAfter is how long a crest is trusted before it is worth
	// asking about again; zero uses LogoMirrorRecheckAfter.
	RecheckAfter time.Duration
}

const (
	// LogoMirrorMaxPerRun is how many crests one pass will fetch. Thirty
	// teams is a full HLTV ranking, so a first run covers everything and
	// every later run does nothing at all.
	LogoMirrorMaxPerRun = 30
	// LogoMirrorPause is the gap between requests. Two seconds is far
	// slower than anything a person browsing the same site would produce,
	// which is the bar worth clearing.
	LogoMirrorPause = 2 * time.Second
	// LogoMirrorMaxBytes rejects anything larger than a crest could
	// reasonably be. These are 50-pixel PNGs.
	LogoMirrorMaxBytes = 512 * 1024
	// LogoMirrorTimeout bounds one fetch.
	LogoMirrorTimeout = 15 * time.Second
	// LogoMirrorUserAgent identifies the bot to whoever is being asked.
	LogoMirrorUserAgent = "cs2predictor-logo-mirror/1.0 (+https://github.com/allowyoba/cs2-predictor)"
	// LogoMirrorRecheckAfter is how long a stored crest is trusted.
	//
	// A team rebrands every few years, so thirty days is already far more
	// often than the picture actually changes. It is a floor, not a
	// schedule: nothing is re-checked before this, and even after it a
	// crest is only looked at when that team is about to play.
	LogoMirrorRecheckAfter = 30 * 24 * time.Hour
	// LogoMirrorMatchWindow is how far ahead a match counts as "about to
	// be played" for the purpose of re-checking a crest.
	LogoMirrorMatchWindow = 48 * time.Hour
	// LogoMirrorRecheckPerRun bounds the revalidation half of a pass,
	// separately from and smaller than the first-fetch half: these are
	// requests for something we already have, so they should never crowd
	// out the ones for something we do not.
	LogoMirrorRecheckPerRun = 5
)

// allowedLogoTypes is what we are willing to store and serve back. An
// allow-list rather than a block-list: this is somebody else's bytes going
// out under our own origin, and "not obviously bad" is not good enough.
var allowedLogoTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/webp":    true,
	"image/gif":     true,
	"image/svg+xml": true,
}

// Dispatch runs one pass under the cluster lock, so two instances never
// fetch the same crest twice.
func (m *LogoMirror) Dispatch(ctx context.Context) {
	if m.Cache == nil || m.Client == nil {
		return
	}
	if _, err := m.Lock.Execute(ctx, "cs2predictor:logo-mirror", m.run); err != nil {
		m.Log.Error("logo mirror pass failed", "error", err)
	}
}

func (m *LogoMirror) run(ctx context.Context) error {
	pending, err := m.Cache.PendingLogos(ctx, m.maxPerRun())
	if err != nil {
		return err
	}
	// Crests we already have, for teams about to play, that nobody has
	// asked about in a long time. Appended after the missing ones so a
	// pass always spends its budget on what is absent before what is
	// merely old — and these are conditional requests, so the usual answer
	// is a 304 with no image attached.
	stale, err := m.Cache.StaleLogos(ctx, m.Clock.Now().Add(-m.recheckAfter()), LogoMirrorMatchWindow, LogoMirrorRecheckPerRun)
	if err != nil {
		m.Log.Warn("crest revalidation skipped", "error", err)
	}
	pending = append(pending, stale...)
	if len(pending) == 0 {
		return nil
	}

	var stored, unchanged int
	for i, need := range pending {
		// The pause goes before every request but the first: a pass that
		// has one crest to fetch should not sit still for no reason.
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(m.pause()):
			}
		}
		logo, err := m.fetch(ctx, need)
		if err != nil {
			m.Log.Warn("crest fetch skipped", "team", need.TeamID.Value, "source", need.Source, "error", err)
			continue
		}
		if logo == nil {
			// Unchanged. Nothing came over the wire but the headers, and
			// all that is recorded is that we asked.
			unchanged++
			if err := m.Cache.TouchLogo(ctx, need.TeamID, need.Source, m.Clock.Now().UTC()); err != nil {
				m.Log.Warn("crest check timestamp not recorded", "team", need.TeamID.Value, "error", err)
			}
			continue
		}
		if err := m.Cache.SaveLogo(ctx, *logo); err != nil {
			m.Log.Error("crest save failed", "team", need.TeamID.Value, "error", err)
			continue
		}
		stored++
	}
	if stored > 0 || unchanged > 0 {
		m.Log.Info("team crests mirrored", "stored", stored, "unchanged", unchanged, "considered", len(pending))
	}
	return nil
}

// fetch performs exactly one request, and refuses anything that does not
// look like the small image it asked for.
//
// Returns (nil, nil) when the far end says the picture has not changed —
// the politest possible outcome, and the usual one for a revalidation.
func (m *LogoMirror) fetch(ctx context.Context, need enrichment.TeamLogoNeed) (*enrichment.TeamLogo, error) {
	if !strings.HasPrefix(need.SourceURL, "https://") {
		// Only ever https, and only ever a URL a provider published. A
		// plain-http crest is not worth the downgrade.
		return nil, fmt.Errorf("refusing non-https crest url")
	}
	ctx, cancel := context.WithTimeout(ctx, LogoMirrorTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, need.SourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", LogoMirrorUserAgent)
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/svg+xml,image/*")
	// Conditional whenever there is something to be conditional about: the
	// CDN then answers 304 and sends no image at all.
	if need.ETag != "" {
		req.Header.Set("If-None-Match", need.ETag)
	}
	if need.LastModified != "" {
		req.Header.Set("If-Modified-Since", need.LastModified)
	}

	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Drained so the connection can be reused instead of torn down and
		// redialled on the next crest — fewer connections is also politer.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, LogoMirrorMaxBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crest request returned %d", resp.StatusCode)
	}

	contentType := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	if !allowedLogoTypes[strings.ToLower(contentType)] {
		return nil, fmt.Errorf("refusing content type %q", contentType)
	}

	// One byte over the cap is a refusal, not a truncation: half an image
	// stored as a whole one is worse than no image.
	body, err := io.ReadAll(io.LimitReader(resp.Body, LogoMirrorMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("crest response was empty")
	}
	if len(body) > LogoMirrorMaxBytes {
		return nil, fmt.Errorf("crest larger than %d bytes", LogoMirrorMaxBytes)
	}

	sum := sha256.Sum256(body)
	return &enrichment.TeamLogo{
		TeamID: need.TeamID, Source: need.Source, SourceURL: need.SourceURL,
		ContentType: contentType, Bytes: body,
		ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		Digest: hex.EncodeToString(sum[:])[:16], FetchedAt: m.Clock.Now().UTC(),
	}, nil
}

func (m *LogoMirror) maxPerRun() int {
	if m.MaxPerRun > 0 {
		return m.MaxPerRun
	}
	return LogoMirrorMaxPerRun
}

func (m *LogoMirror) recheckAfter() time.Duration {
	if m.RecheckAfter > 0 {
		return m.RecheckAfter
	}
	return LogoMirrorRecheckAfter
}

func (m *LogoMirror) pause() time.Duration {
	if m.Pause > 0 {
		return m.Pause
	}
	return LogoMirrorPause
}
