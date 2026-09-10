package telegram

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Telegram's documented ceilings for a bot: roughly 30 messages per second
// overall, about one per second into any single chat, and — a separate,
// tighter ceiling straight from Telegram's own FAQ — no more than 20
// messages per minute into any one group. That per-minute cap binds first
// on any burst longer than ~20 seconds even while the flat 1/sec cap is
// respected instant-to-instant (a poll plus several admin-panel edits and
// confirmations landing in the same group in quick succession is exactly
// that kind of burst), so both budgets are enforced, not just the
// per-second one. Applied to every chat uniformly, private chats included,
// even though Telegram's 20/min wording is group-specific — DMs are
// low-volume enough that the extra caution costs nothing observable.
// Handling 429s after the fact (which Call already does) recovers from
// crossing these; pacing keeps the bot from crossing them in the first
// place, which matters most exactly when it's busiest.
const (
	DefaultGlobalMessagesPerSecond  = 25 // a little under 30, deliberately
	DefaultPerChatMessagesPerSecond = 1
	// perChatMessagesPerMinute mirrors Telegram's documented per-group
	// ceiling exactly, so it isn't a tunable like the two rates above.
	perChatMessagesPerMinute = 20

	globalBurst           = 5
	perChatBurst          = 1
	perChatPerMinuteBurst = perChatMessagesPerMinute

	// perChatLimiterTTL bounds how long an idle chat's limiter is kept.
	// Without it a long-running process accumulates one limiter per chat it
	// has ever messaged.
	perChatLimiterTTL = 30 * time.Minute
)

// rateLimiter paces outbound Bot API calls: one global bucket plus two per
// destination chat (per-second and per-minute — see the doc comment
// above). Waiting happens before the request is sent, so a burst is spread
// out rather than rejected and retried.
type rateLimiter struct {
	global      *rate.Limiter
	perChatRate rate.Limit

	mu      sync.Mutex
	perChat map[int64]*chatLimiter
	lastGC  time.Time
	now     func() time.Time // overridable in tests
}

type chatLimiter struct {
	perSecond *rate.Limiter
	perMinute *rate.Limiter
	lastUsed  time.Time
}

// newRateLimiter returns nil — meaning "no pacing" — when either rate is
// zero or negative. That is the default for a Client built without limits
// configured, which keeps tests running at full speed; production sets real
// values (see Config.GlobalMessagesPerSecond).
func newRateLimiter(globalPerSecond, perChatPerSecond float64) *rateLimiter {
	if globalPerSecond <= 0 || perChatPerSecond <= 0 {
		return nil
	}
	return &rateLimiter{
		global:      rate.NewLimiter(rate.Limit(globalPerSecond), globalBurst),
		perChatRate: rate.Limit(perChatPerSecond),
		perChat:     map[int64]*chatLimiter{},
		now:         time.Now,
	}
}

// wait blocks until this call may go out, or until ctx is done. chatID 0
// means "no particular chat" (getMe, answerCallbackQuery and friends): those
// still count against the global budget but need no per-chat pacing.
func (l *rateLimiter) wait(ctx context.Context, chatID int64) error {
	if l == nil {
		return nil
	}
	if err := l.global.Wait(ctx); err != nil {
		return err
	}
	if chatID == 0 {
		return nil
	}
	entry := l.forChat(chatID)
	if err := entry.perSecond.Wait(ctx); err != nil {
		return err
	}
	return entry.perMinute.Wait(ctx)
}

func (l *rateLimiter) forChat(chatID int64) *chatLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.gcLocked(now)

	entry, ok := l.perChat[chatID]
	if !ok {
		entry = &chatLimiter{
			perSecond: rate.NewLimiter(l.perChatRate, perChatBurst),
			perMinute: rate.NewLimiter(rate.Limit(perChatMessagesPerMinute)/60, perChatPerMinuteBurst),
		}
		l.perChat[chatID] = entry
	}
	entry.lastUsed = now
	return entry
}

// gcLocked drops limiters for chats that have been idle longer than the
// TTL. Swept at most once per TTL so the common path stays a map lookup.
func (l *rateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < perChatLimiterTTL {
		return
	}
	l.lastGC = now
	for id, entry := range l.perChat {
		if now.Sub(entry.lastUsed) > perChatLimiterTTL {
			delete(l.perChat, id)
		}
	}
}

// chatIDFromPayload digs the destination chat out of a Bot API payload, so
// pacing works without every call site having to declare it. Telegram's own
// field is always named chat_id, and is either the numeric id or a
// "@channelusername" string (which has no numeric id to pace by).
func chatIDFromPayload(payload any) int64 {
	m, ok := payload.(map[string]any)
	if !ok {
		return 0
	}
	switch v := m["chat_id"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
