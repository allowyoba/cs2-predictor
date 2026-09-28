package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/platform/common"
)

// searchBackfillBatch bounds one pass. The catalogue is thousands of rows at
// most, and this runs once at startup off the boot path, so the batching is
// about not holding one statement open across the whole table rather than
// about the total being large.
const searchBackfillBatch = 500

// SearchKeyStore fills in the folded search keys of rows that have none.
type SearchKeyStore interface {
	// BackfillSearchKeys computes the key for up to limit rows that are
	// still empty, and reports how many it filled. Zero means there is
	// nothing left to do.
	BackfillSearchKeys(ctx context.Context, limit int) (int, error)
}

// SearchKeyBackfill fills in the search keys migration 0059 deliberately
// left empty.
//
// The fold that makes "ропз" find "r0pz" lives in Go (see
// competition.FoldForSearch), and writing a second, approximate copy of it
// in SQL to backfill with would be worse than briefly having none: two folds
// that disagree are a search that works differently depending on when a row
// was last written. So the column starts empty, the query falls back to the
// stored name while it is, and this fills it in properly once at startup.
type SearchKeyBackfill struct {
	Store SearchKeyStore
	Lock  common.ClusterLock
	Log   *slog.Logger
}

func (b *SearchKeyBackfill) Run(ctx context.Context) {
	if b.Store == nil {
		return
	}
	_, err := b.Lock.Execute(ctx, "cs2predictor:search-key-backfill", func(ctx context.Context) error {
		return b.run(ctx)
	})
	if err != nil {
		// A missing key costs a search result, not a message: the query
		// still falls back to the stored name.
		b.Log.Warn("search key backfill failed, names remain searchable by their spelling", "error", err)
	}
}

func (b *SearchKeyBackfill) run(ctx context.Context) error {
	total := 0
	for {
		filled, err := b.Store.BackfillSearchKeys(ctx, searchBackfillBatch)
		if err != nil {
			return err
		}
		total += filled
		if filled < searchBackfillBatch {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if total > 0 {
		b.Log.Info("search keys backfilled", "rows", total)
	}
	return nil
}
