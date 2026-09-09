// Package competition holds the tournament/match domain: events, teams,
// matches, and the series-format scoring math that also drives Telegram
// poll-option generation — no adapter or app package may be imported here.
package competition

import (
	"fmt"
	"time"

	"cs2predictor/internal/platform/common"
)

type GameCode string

const GameCS2 GameCode = "CS2"

type EventStatus string

const (
	EventUpcoming  EventStatus = "UPCOMING"
	EventRunning   EventStatus = "RUNNING"
	EventFinished  EventStatus = "FINISHED"
	EventCancelled EventStatus = "CANCELLED"
)

type MatchStatus string

const (
	MatchNotStarted MatchStatus = "NOT_STARTED"
	MatchRunning    MatchStatus = "RUNNING"
	MatchFinished   MatchStatus = "FINISHED"
	MatchPostponed  MatchStatus = "POSTPONED"
	MatchCancelled  MatchStatus = "CANCELLED"
	MatchForfeit    MatchStatus = "FORFEIT"
)

type SeriesKind string

const (
	BestOf    SeriesKind = "BEST_OF"
	FixedMaps SeriesKind = "FIXED_MAPS"
	FirstTo   SeriesKind = "FIRST_TO"
)

type Outcome string

const (
	OutcomeFirst  Outcome = "FIRST"
	OutcomeSecond Outcome = "SECOND"
	OutcomeDraw   Outcome = "DRAW"
)

// SeriesFormat describes how a match's series is scored: best-of-N,
// fixed-maps-N, or first-to-N. Size must be > 0 — construct with
// NewSeriesFormat, which validates it.
type SeriesFormat struct {
	Kind SeriesKind
	Size int
}

// NewSeriesFormat validates size > 0.
func NewSeriesFormat(kind SeriesKind, size int) (SeriesFormat, error) {
	if size <= 0 {
		return SeriesFormat{}, fmt.Errorf("series size must be positive")
	}
	return SeriesFormat{Kind: kind, Size: size}, nil
}

// PossibleScores enumerates every final score this format can produce.
// The order is load-bearing, not cosmetic — it becomes the Telegram poll
// option index.
func (f SeriesFormat) PossibleScores() []MatchScore {
	switch f.Kind {
	case BestOf:
		target := f.Size/2 + 1
		scores := make([]MatchScore, 0, target*2)
		for i := 0; i < target; i++ {
			scores = append(scores, MatchScore{First: target, Second: i})
		}
		for i := target - 1; i >= 0; i-- {
			scores = append(scores, MatchScore{First: i, Second: target})
		}
		return scores
	case FirstTo:
		n := f.Size
		scores := make([]MatchScore, 0, n*2)
		for i := 0; i < n; i++ {
			scores = append(scores, MatchScore{First: n, Second: i})
		}
		for i := n - 1; i >= 0; i-- {
			scores = append(scores, MatchScore{First: i, Second: n})
		}
		return scores
	case FixedMaps:
		n := f.Size
		scores := make([]MatchScore, 0, n+1)
		for first := n; first >= 0; first-- {
			scores = append(scores, MatchScore{First: first, Second: n - first})
		}
		return scores
	default:
		return nil
	}
}

// Label renders the short format tag shown in polls and notifications:
// "FT<n>" for FIRST_TO, "BO<n>" for both BEST_OF and FIXED_MAPS (FIXED_MAPS
// is intentionally labeled "BO<n>" too, not "FM<n>").
func (f SeriesFormat) Label() string {
	if f.Kind == FirstTo {
		return fmt.Sprintf("FT%d", f.Size)
	}
	return fmt.Sprintf("BO%d", f.Size)
}

// ExactPoints is the number of points awarded for guessing the exact final
// score with this format.
func (f SeriesFormat) ExactPoints() int {
	switch f.Kind {
	case BestOf:
		return f.Size/2 + 1
	default: // FirstTo, FixedMaps
		return f.Size
	}
}

// MatchScore is a final (or predicted) score. Both values must be
// non-negative — construct with NewMatchScore to get that validated, or use
// the struct literal directly when values are already known-non-negative
// (e.g. decoded from the DB).
type MatchScore struct {
	First  int
	Second int
}

func NewMatchScore(first, second int) (MatchScore, error) {
	if first < 0 || second < 0 {
		return MatchScore{}, fmt.Errorf("match score components must be non-negative")
	}
	return MatchScore{First: first, Second: second}, nil
}

// String formats as "first:second" — used both for poll option labels and
// as the settlement result-hash string, so this exact format is
// load-bearing.
func (s MatchScore) String() string {
	return fmt.Sprintf("%d:%d", s.First, s.Second)
}

func (s MatchScore) Outcome() Outcome {
	switch {
	case s.First > s.Second:
		return OutcomeFirst
	case s.Second > s.First:
		return OutcomeSecond
	default:
		return OutcomeDraw
	}
}

type Team struct {
	ID         common.TeamID
	Name       string
	ExternalID string
	// Location is the provider's ISO-3166 alpha-2 country code when known
	// (PandaScore exposes it on team/opponent objects, e.g. "DE", "RU").
	Location string
}

// EventTier is the provider-reported prestige tier of a tournament
// ("s" being the highest, e.g. Majors and top international LANs). An
// empty/unrecognized value means the provider didn't report one.
type EventTier string

const (
	TierS        EventTier = "s"
	TierA        EventTier = "a"
	TierB        EventTier = "b"
	TierC        EventTier = "c"
	TierD        EventTier = "d"
	TierUnranked EventTier = "unranked"
	TierUnknown  EventTier = ""
)

// IsTopTier is true for S and A tier events — the ones the "/events top"
// search filter (Catalog.SearchEvents's topTierOnly) restricts results to.
func (t EventTier) IsTopTier() bool {
	return t == TierS || t == TierA
}

// Badge is the emoji prefix (including trailing space) shown next to an
// event's name in Telegram listings — distinct per tier so S doesn't read
// identically to A, with lower tiers left unbadged since they're not what a
// chat is trying to pick out of a search result. Returns "" for
// unrecognized/unranked/unknown tiers.
func (t EventTier) Badge() string {
	switch t {
	case TierS:
		return "🌟 "
	case TierA:
		return "⭐ "
	case TierB:
		return "🔹 "
	default:
		return ""
	}
}

type Event struct {
	ID         common.EventID
	Game       GameCode
	Name       string
	ExternalID string
	Status     EventStatus
	StartsAt   *time.Time
	EndsAt     *time.Time
	Provider   string // defaults to "PANDASCORE" at construction sites
	Tier       EventTier
}

// IsSubscribable reports whether the event should be offered for new
// subscriptions. EndsAt is checked as a defensive guard against a stale
// provider status between catalog refreshes.
func (e Event) IsSubscribable(now time.Time) bool {
	if e.Status == EventFinished || e.Status == EventCancelled {
		return false
	}
	if e.EndsAt != nil && !e.EndsAt.After(now) {
		return false
	}
	return true
}

type Match struct {
	ID              common.MatchID
	EventID         common.EventID
	ExternalID      string
	FirstTeam       *Team
	SecondTeam      *Team
	Stage           *string
	ScheduledAt     *time.Time
	ActualStartedAt *time.Time
	Status          MatchStatus
	Format          SeriesFormat
	Score           *MatchScore
	StageExternalID *string
}

// ParticipantsKnown is true only once both teams are resolved (qualifier
// slots are nil until known).
func (m Match) ParticipantsKnown() bool {
	return m.FirstTeam != nil && m.SecondTeam != nil
}

// ShouldCancelPrediction is true only for CANCELLED or FORFEIT — POSTPONED
// matches are rescheduled instead, not cancelled.
func (m Match) ShouldCancelPrediction() bool {
	return m.Status == MatchCancelled || m.Status == MatchForfeit
}
