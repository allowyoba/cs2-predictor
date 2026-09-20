package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// Access to the Mini App.
//
// The app shows one person their whole prediction history, so it is closed
// by default and opened per person. Root operators are exempt in code
// rather than by a stored grant: they are who approves the others, and a
// bootstrap that has to grant itself cannot start.

// MiniAppStatus is where a request stands. Persisted, so the values are
// stable.
type MiniAppStatus string

const (
	// MiniAppPending is asked for and not yet decided.
	MiniAppPending MiniAppStatus = "PENDING"
	MiniAppGranted MiniAppStatus = "GRANTED"
	// MiniAppDenied is a decision, not an absence of one — it is recorded
	// so a refusal is not silently re-asked on the next tap.
	MiniAppDenied MiniAppStatus = "DENIED"
)

// MiniAppAccess is one person's standing with the app.
type MiniAppAccess struct {
	UserID      common.UserID
	Status      MiniAppStatus
	RequestedAt time.Time
	DecidedAt   *time.Time
	DecidedBy   *common.UserID
}

// MiniAppAccessRepository persists those decisions.
type MiniAppAccessRepository interface {
	// MiniAppAccess returns the person's standing, or nil when they have
	// never asked.
	MiniAppAccess(ctx context.Context, userID common.UserID) (*MiniAppAccess, error)
	// RequestMiniAppAccess records a request. Asking again while a
	// request is pending is not an error and does not reset it — a second
	// tap must not push somebody back down a queue.
	RequestMiniAppAccess(ctx context.Context, userID common.UserID, at time.Time) error
	// DecideMiniAppAccess records an approval or a refusal, naming who
	// made it.
	DecideMiniAppAccess(ctx context.Context, userID common.UserID, status MiniAppStatus, decidedBy common.UserID, at time.Time) error
	// PendingMiniAppRequests lists the undecided ones, oldest first.
	PendingMiniAppRequests(ctx context.Context, limit int) ([]MiniAppAccess, error)
}
