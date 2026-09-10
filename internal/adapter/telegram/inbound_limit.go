package telegram

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DefaultInboundPerSecond/DefaultInboundBurst bound how often a single
// Telegram user may trigger the bot to do any work at all — independent of
// rateLimiter, which paces this bot's own outbound Bot API calls.
// InboundLimiter instead protects the bot (and the database behind it) from
// one account mashing buttons or scripting a flood of commands: 2 actions
// per second with a burst of 4 comfortably covers fast manual use (rapid
// menu navigation, double-taps) while capping a sustained flood at a small,
// cheap-to-absorb rate.
const (
	DefaultInboundPerSecond = 2
	DefaultInboundBurst     = 4

	// inboundLimiterTTL bounds how long an idle user's limiter is kept —
	// without it a long-running process accumulates one limiter per
	// distinct user it has ever heard from.
	inboundLimiterTTL = 10 * time.Minute
)

// InboundLimiter throttles incoming updates per Telegram user id. Same
// "one token bucket per key, garbage-collected when idle" shape as
// rateLimiter (see ratelimit.go) — kept as a separate type because the two
// solve different problems (outbound pacing vs. inbound abuse) and are
// configured independently.
type InboundLimiter struct {
	rate  rate.Limit
	burst int

	mu     sync.Mutex
	perKey map[int64]*inboundLimiterEntry
	lastGC time.Time
	now    func() time.Time // overridable in tests
}

type inboundLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// NewInboundLimiter returns nil — meaning "no throttling" — when perSecond
// is zero or negative, so a handler built without one configured (every
// test today) behaves exactly as before.
func NewInboundLimiter(perSecond float64, burst int) *InboundLimiter {
	if perSecond <= 0 {
		return nil
	}
	return &InboundLimiter{rate: rate.Limit(perSecond), burst: burst, perKey: map[int64]*inboundLimiterEntry{}, now: time.Now}
}

// Allow reports whether userID may act right now, consuming one token from
// their bucket if so. A nil receiver always allows — the "disabled" case.
func (l *InboundLimiter) Allow(userID int64) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.gcLocked(now)

	entry, ok := l.perKey[userID]
	if !ok {
		entry = &inboundLimiterEntry{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.perKey[userID] = entry
	}
	entry.lastUsed = now
	return entry.limiter.AllowN(now, 1)
}

// gcLocked drops limiters for users idle longer than the TTL. Swept at most
// once per TTL so the common path stays a single map lookup.
func (l *InboundLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < inboundLimiterTTL {
		return
	}
	l.lastGC = now
	for id, entry := range l.perKey {
		if now.Sub(entry.lastUsed) > inboundLimiterTTL {
			delete(l.perKey, id)
		}
	}
}

// updateActor names whose bucket an update counts against: a message
// sender, a callback tapper, or a poll voter. ok is false for an update
// with no attributable person (shouldn't happen for the three kinds this
// bot subscribes to, but left explicit rather than assumed).
func updateActor(update Update) (userID int64, ok bool) {
	switch {
	case update.Message != nil && update.Message.From != nil:
		return update.Message.From.ID, true
	case update.CallbackQuery != nil:
		return update.CallbackQuery.From.ID, true
	case update.PollAnswer != nil:
		return update.PollAnswer.User.ID, true
	default:
		return 0, false
	}
}
