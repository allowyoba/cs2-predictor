package feedback

import (
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

// The Ideas channel is the one surface anybody on the internet can write
// into, so these cover both halves of it: a person with something to say
// is never obstructed, and a script is stopped and then stops being
// answered at all.

func attempt(outcome Outcome, ago time.Duration, now time.Time) Attempt {
	return Attempt{UserID: common.UserID{Value: 1}, Outcome: outcome, CreatedAt: now.Add(-ago)}
}

func TestPolicy_AcceptsAnOrdinaryFirstIdea(t *testing.T) {
	now := time.Now()
	decision := DefaultPolicy().Evaluate(now, "  Добавьте, пожалуйста, статистику по картам  ", nil, false)

	if decision.Outcome != OutcomeAccepted {
		t.Fatalf("outcome = %s, want accepted", decision.Outcome)
	}
	if decision.Text != "Добавьте, пожалуйста, статистику по картам" {
		t.Fatalf("Text = %q, want it trimmed", decision.Text)
	}
	if decision.Fingerprint == "" {
		t.Fatal("an accepted idea needs a fingerprint, or the duplicate check has nothing to match")
	}
}

// The limit is in runes: a ceiling that allowed 1000 Latin characters and
// 250 Cyrillic ones would be arbitrary in the language most of these
// arrive in.
func TestPolicy_MeasuresLengthInCharactersNotBytes(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()

	atLimit := strings.Repeat("я", policy.MaxRunes)
	if got := policy.Evaluate(now, atLimit, nil, false).Outcome; got != OutcomeAccepted {
		t.Fatalf("a message exactly at the limit must be accepted, got %s", got)
	}
	over := strings.Repeat("я", policy.MaxRunes+1)
	if got := policy.Evaluate(now, over, nil, false).Outcome; got != OutcomeTooLong {
		t.Fatalf("outcome = %s, want too long", got)
	}
}

func TestPolicy_RefusesEmptyAndRepeatedIdeas(t *testing.T) {
	now := time.Now()
	if got := DefaultPolicy().Evaluate(now, "   \n  ", nil, false).Outcome; got != OutcomeEmpty {
		t.Fatalf("outcome = %s, want empty", got)
	}
	if got := DefaultPolicy().Evaluate(now, "same idea", nil, true).Outcome; got != OutcomeDuplicate {
		t.Fatalf("outcome = %s, want duplicate", got)
	}
}

// Two ordinary limits, both of which tell the person when to come back:
// a pause between ideas, and a ceiling per day.
func TestPolicy_PacesAcceptedIdeas(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()

	tooSoon := policy.Evaluate(now, "another idea", []Attempt{attempt(OutcomeAccepted, 30*time.Second, now)}, false)
	if tooSoon.Outcome != OutcomeTooSoon {
		t.Fatalf("outcome = %s, want too soon", tooSoon.Outcome)
	}
	if tooSoon.RetryAfter <= 0 || tooSoon.RetryAfter > policy.Cooldown {
		t.Fatalf("RetryAfter = %s, want what is left of the cooldown", tooSoon.RetryAfter)
	}

	var quotaUsed []Attempt
	for i := 0; i < policy.Quota; i++ {
		quotaUsed = append(quotaUsed, attempt(OutcomeAccepted, time.Duration(i+1)*time.Hour, now))
	}
	full := policy.Evaluate(now, "one more", quotaUsed, false)
	if full.Outcome != OutcomeQuotaReached {
		t.Fatalf("outcome = %s, want quota reached", full.Outcome)
	}

	// Yesterday's ideas do not count against today's quota.
	var old []Attempt
	for i := 0; i < policy.Quota; i++ {
		old = append(old, attempt(OutcomeAccepted, policy.QuotaWindow+time.Duration(i)*time.Hour, now))
	}
	if got := policy.Evaluate(now, "a new day", old, false).Outcome; got != OutcomeAccepted {
		t.Fatalf("outcome = %s, want the window to have rolled over", got)
	}
}

// Rejections count towards the flood threshold. A limiter that counted
// only accepted messages would be defeated by failing on purpose: send
// junk forever, never reach the quota.
func TestPolicy_CountsRejectedAttemptsTowardsTheFloodThreshold(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()
	var junk []Attempt
	for i := 0; i < policy.FloodAttempts; i++ {
		junk = append(junk, attempt(OutcomeTooLong, time.Duration(i)*time.Second, now))
	}

	first := policy.Evaluate(now, "hello", junk, false)
	if first.Outcome != OutcomeFlood {
		t.Fatalf("outcome = %s, want flood", first.Outcome)
	}
	if first.Silent {
		t.Fatal("the person is told once what happened — silence starts after that")
	}

	// Past the threshold nothing is sent back at all: a reply per message
	// is what a flood is actually extracting from the bot.
	beyond := policy.Evaluate(now, "hello", append(junk, attempt(OutcomeFlood, 0, now)), false)
	if beyond.Outcome != OutcomeFlood || !beyond.Silent {
		t.Fatalf("decision = %+v, want a silent refusal", beyond)
	}
}

// The flood window is a window: once it passes, the same person is
// ordinary again.
func TestPolicy_ForgetsAFloodOnceItsWindowPasses(t *testing.T) {
	policy := DefaultPolicy()
	now := time.Now()
	var old []Attempt
	for i := 0; i < policy.FloodAttempts*2; i++ {
		old = append(old, attempt(OutcomeTooLong, policy.FloodWindow+time.Duration(i)*time.Second, now))
	}

	if got := policy.Evaluate(now, "a calm idea", old, false).Outcome; got != OutcomeAccepted {
		t.Fatalf("outcome = %s, want the old flood to be forgotten", got)
	}
}

// The same words with different spacing or case are the same idea — this
// is what makes an impatient re-send land as "already got it" rather than
// as a second entry in somebody's inbox.
func TestFingerprint_IgnoresCaseAndSpacing(t *testing.T) {
	if Fingerprint("Add  MAP stats") != Fingerprint("add map stats") {
		t.Fatal("expected the same fingerprint for the same idea")
	}
	if Fingerprint("add map stats") == Fingerprint("add player stats") {
		t.Fatal("expected different ideas to differ")
	}
}
