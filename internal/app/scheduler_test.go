package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunFixedDelay_RunsImmediatelyThenRespectsDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32

	done := make(chan struct{})
	go func() {
		RunFixedDelay(ctx, 20*time.Millisecond, func(context.Context) {
			runs.Add(1)
		})
		close(done)
	}()

	// The first run should happen immediately, well before one delay period.
	time.Sleep(5 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs after 5ms = %d, want 1 (immediate first run)", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunFixedDelay did not return after context cancellation")
	}
}

func TestRunFixedDelay_StopsOnContextCancelDuringDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32

	done := make(chan struct{})
	go func() {
		RunFixedDelay(ctx, time.Hour, func(context.Context) {
			runs.Add(1)
		})
		close(done)
	}()

	time.Sleep(5 * time.Millisecond) // let the immediate first run happen
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunFixedDelay did not stop promptly on cancellation")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want exactly 1 (cancelled during the long delay, before a second run)", got)
	}
}
