// Package feedback holds the rules for unsolicited input from people using
// the bot: an idea, a complaint, a "why does it do that". The bot has one
// such channel — the Ideas screen in a private chat — and everything that
// makes it safe to leave open to the public internet lives here rather
// than in the Telegram adapter.
package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"cs2predictor/internal/platform/common"
)

// Suggestion is one accepted idea, kept for the people who have to act on
// it and for the duplicate check below.
type Suggestion struct {
	ID     common.RequestID
	UserID common.UserID
	// Text is the sanitized message: whitespace collapsed at the edges,
	// never longer than Policy.MaxRunes.
	Text string
	// Fingerprint identifies the same idea sent twice. Stored rather than
	// recomputed so the check is one indexed lookup instead of a scan.
	Fingerprint string
	CreatedAt   time.Time
}

// Attempt is one try at sending something, accepted or not. Rejections are
// recorded too: a flood is made of attempts, and a limiter that only
// counted successes would be trivially defeated by failing on purpose.
type Attempt struct {
	UserID    common.UserID
	Outcome   Outcome
	CreatedAt time.Time
}

// Outcome is why an attempt ended the way it did. Persisted, so the values
// have to stay stable.
type Outcome string

const (
	OutcomeAccepted Outcome = "ACCEPTED"
	// OutcomeTooLong/OutcomeEmpty are ordinary user errors: the person is
	// told what to fix and can try again immediately.
	OutcomeTooLong Outcome = "TOO_LONG"
	OutcomeEmpty   Outcome = "EMPTY"
	// OutcomeDuplicate is the same idea sent again — usually an impatient
	// re-send rather than abuse, and answered as such.
	OutcomeDuplicate Outcome = "DUPLICATE"
	// OutcomeTooSoon/OutcomeQuotaReached are the two ordinary rate limits.
	OutcomeTooSoon      Outcome = "TOO_SOON"
	OutcomeQuotaReached Outcome = "QUOTA_REACHED"
	// OutcomeFlood is the abuse case: so many attempts in so little time
	// that this is no longer somebody with an idea. Answered once and then
	// silently, because a reply per message is exactly what a flood is
	// trying to extract.
	OutcomeFlood Outcome = "FLOOD"
)

// Decision is what Policy made of an attempt.
type Decision struct {
	Outcome Outcome
	// RetryAfter is how long until this person may try again, for the
	// outcomes where waiting is the answer. Zero otherwise.
	RetryAfter time.Duration
	// Silent is true when the caller must not answer at all: past the
	// flood threshold, every reply is free amplification for whoever is
	// sending them.
	Silent bool
	// Text is the sanitized message, set only when Outcome is
	// OutcomeAccepted.
	Text string
	// Fingerprint identifies the idea, set alongside Text.
	Fingerprint string
}

// Policy bounds what one person may send and how often.
//
// The numbers are deliberately generous for a person and immediately
// restrictive for a script: somebody with three ideas in an evening never
// notices these, while anything sending continuously is stopped within
// seconds and stops being answered at all.
type Policy struct {
	// MaxRunes bounds one message. Counted in runes, not bytes: a limit
	// that lets through 1000 Latin characters and 250 Cyrillic ones would
	// be arbitrary in exactly the language most of these arrive in.
	MaxRunes int
	// Cooldown is the minimum gap between two accepted ideas from one
	// person — the pause that makes a stream of ideas a conversation
	// rather than a pipe.
	Cooldown time.Duration
	// Quota and QuotaWindow bound how many ideas one person may have
	// accepted in a rolling window.
	Quota       int
	QuotaWindow time.Duration
	// FloodAttempts and FloodWindow describe what stops looking like a
	// person: this many attempts of any kind inside this window, after
	// which the person is answered once and then not at all until the
	// window clears.
	FloodAttempts int
	FloodWindow   time.Duration
}

// DefaultPolicy is what the bot runs with unless a deployment says
// otherwise.
func DefaultPolicy() Policy {
	return Policy{
		MaxRunes:      1000,
		Cooldown:      2 * time.Minute,
		Quota:         5,
		QuotaWindow:   24 * time.Hour,
		FloodAttempts: 10,
		FloodWindow:   10 * time.Minute,
	}
}

// Evaluate decides what to do with one attempt, given everything that
// person has recently tried. The order matters: the flood check comes
// first, because the whole point of it is that an abusive sender stops
// getting individual, informative answers.
func (p Policy) Evaluate(now time.Time, raw string, recent []Attempt, duplicate bool) Decision {
	attempts, accepted, lastAccepted := p.summarize(now, recent)

	if p.FloodAttempts > 0 && attempts >= p.FloodAttempts {
		// Answered once, at the threshold, and silently from then on.
		return Decision{Outcome: OutcomeFlood, RetryAfter: p.FloodWindow, Silent: attempts > p.FloodAttempts}
	}

	text := strings.TrimSpace(raw)
	if text == "" {
		return Decision{Outcome: OutcomeEmpty}
	}
	if utf8.RuneCountInString(text) > p.MaxRunes {
		return Decision{Outcome: OutcomeTooLong}
	}
	if duplicate {
		return Decision{Outcome: OutcomeDuplicate}
	}
	if p.Quota > 0 && accepted >= p.Quota {
		return Decision{Outcome: OutcomeQuotaReached, RetryAfter: p.QuotaWindow}
	}
	if p.Cooldown > 0 && !lastAccepted.IsZero() {
		if elapsed := now.Sub(lastAccepted); elapsed < p.Cooldown {
			return Decision{Outcome: OutcomeTooSoon, RetryAfter: p.Cooldown - elapsed}
		}
	}
	return Decision{Outcome: OutcomeAccepted, Text: text, Fingerprint: Fingerprint(text)}
}

// summarize reduces the recent attempts to the three numbers Evaluate
// needs: how many of anything inside the flood window, how many accepted
// inside the quota window, and when the last accepted one was.
func (p Policy) summarize(now time.Time, recent []Attempt) (attempts, accepted int, lastAccepted time.Time) {
	floodFrom := now.Add(-p.FloodWindow)
	quotaFrom := now.Add(-p.QuotaWindow)
	for _, a := range recent {
		if a.CreatedAt.After(floodFrom) {
			attempts++
		}
		if a.Outcome != OutcomeAccepted {
			continue
		}
		if a.CreatedAt.After(quotaFrom) {
			accepted++
		}
		if a.CreatedAt.After(lastAccepted) {
			lastAccepted = a.CreatedAt
		}
	}
	return attempts, accepted, lastAccepted
}

// Fingerprint identifies the same idea sent twice, ignoring the
// differences a person makes by accident: case, and how much whitespace
// ended up between the words.
func Fingerprint(text string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(text), " "))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// Repository persists suggestions and the attempts that produced them.
type Repository interface {
	// RecentAttempts returns this person's attempts since the given time,
	// newest first — the input to Policy.Evaluate.
	RecentAttempts(ctx context.Context, userID common.UserID, since time.Time) ([]Attempt, error)
	// HasFingerprint reports whether this person already sent the same
	// idea inside the window.
	HasFingerprint(ctx context.Context, userID common.UserID, fingerprint string, since time.Time) (bool, error)
	// RecordAttempt stores the outcome of one attempt, and the suggestion
	// itself when there is one to store.
	RecordAttempt(ctx context.Context, attempt Attempt, suggestion *Suggestion) error
}
