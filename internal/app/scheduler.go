package app

import (
	"context"
	"time"
)

// RunFixedDelay runs job immediately, then repeatedly after delay has
// elapsed since the PREVIOUS run finished (fixed-delay, not fixed-rate: a
// slow run pushes the next one back, it doesn't get skipped or double up).
// Returns once ctx is cancelled.
func RunFixedDelay(ctx context.Context, delay time.Duration, job func(ctx context.Context)) {
	for {
		job(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
