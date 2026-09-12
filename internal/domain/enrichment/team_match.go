package enrichment

import (
	"context"
	"strings"
	"time"

	"cs2predictor/internal/platform/common"
)

// This file is the identity-resolution pipeline's third and last tier,
// after MatchTeam's exact-name/alias/roster steps (identity.go): a fuzzy
// name-similarity score, used two ways. A very high score (>=
// FuzzyAutoAcceptThreshold) is trusted automatically, on the same "no
// guessing below a hard bar" principle as the roster step. Everything else
// that still clears a much lower floor (>= FuzzyRequestThreshold — enough
// to say "this probably IS in Valve's list, just under a name we don't
// recognize" rather than "this team simply isn't ranked") becomes a
// TeamMatchRequest: an operator decides via /team_matches, optionally
// informed by a few chat members' yes/no votes first. Nothing here ever
// writes an identity mapping on the crowd's word alone — see
// CrowdAdjustedScore's cap.

const (
	// FuzzyAutoAcceptThreshold is how similar an external name must be to a
	// local team's own name (after stripping common organizational words)
	// before it is trusted without any human involvement — high enough
	// that in practice it only ever fires on the "Team X" vs "X" style of
	// difference, never on two genuinely different teams.
	FuzzyAutoAcceptThreshold = 90
	// FuzzyRequestThreshold is the floor below which a name is treated as
	// "not a plausible match at all" rather than "ambiguous" — below it, no
	// TeamMatchRequest is created, because the far more likely explanation
	// is that Valve simply doesn't rank this team, not that its name
	// diverges from ours. Creating a request anyway would just be noise an
	// operator or helper has no real way to resolve.
	FuzzyRequestThreshold = 40
	// MaxCandidatesPerRequest bounds how many local teams are offered as
	// candidates on one request — enough to cover a genuine ambiguity
	// (two similarly-named teams) without turning the review card into a
	// wall of unlikely options.
	MaxCandidatesPerRequest = 3

	// CrowdYesBoost/CrowdNoPenalty adjust a candidate's score per response.
	// Asymmetric on purpose: wrongly raising a bad candidate is worse than
	// wrongly lowering a good one (a lowered score just means an operator
	// looks at it sooner), so a "no" moves the score further than a "yes".
	CrowdYesBoost  = 8
	CrowdNoPenalty = 12
	// CrowdScoreCap is the highest a crowd-adjusted score can ever reach —
	// strictly below FuzzyAutoAcceptThreshold, so crowd answers alone can
	// never cross the auto-accept bar; they can only make a candidate the
	// obvious pick for whoever reviews the request. Confirming a request
	// where the answer is genuinely available (i.e. this bar) still
	// belongs to an operator.
	CrowdScoreCap = 85

	// MaxCrowdAsksPerRequest bounds how many different people are ever
	// asked about the same request, across however many chats it keeps
	// coming up in — past this, it just waits for an operator.
	MaxCrowdAsksPerRequest = 5
	// MaxLifetimeAsksPerUser bounds how many of these questions any one
	// person is ever sent, so helping the bot once doesn't turn into a
	// standing chore nobody agreed to.
	MaxLifetimeAsksPerUser = 5
)

// orgWords are stripped from both sides before fuzzy comparison — the
// single most common reason an otherwise-identical name fails exact match
// (Valve's own "Team Vitality" vs a provider's "Vitality"). Unlike
// NormalizeTeamName (identity.go), which deliberately never guesses at
// distinct spellings, this list is a fixed, curated set of generic
// organizational words — stripping them can't merge two different teams,
// only two spellings of the same one.
var orgWords = []string{"team", "esports", "esport", "gaming", "club", "e-sports"}

// FuzzyNameScore rates how likely a and b name the same team, 0-100: 100
// only for an exact match after normalization/org-word stripping, 0 for
// names sharing nothing in common. Based on Levenshtein edit distance
// relative to the longer name's length — simple, dependency-free, and
// tuned only to catch near-identical spellings (see the two thresholds
// above), never to guess between genuinely different names.
func FuzzyNameScore(a, b string) int {
	na, nb := stripOrgWords(NormalizeTeamName(a)), stripOrgWords(NormalizeTeamName(b))
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 100
	}
	dist := levenshtein(na, nb)
	maxLen := len(na)
	if len(nb) > maxLen {
		maxLen = len(nb)
	}
	if maxLen == 0 {
		return 0
	}
	score := 100 - (dist*100)/maxLen
	if score < 0 {
		return 0
	}
	return score
}

func stripOrgWords(normalized string) string {
	fields := strings.Fields(normalized)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		skip := false
		for _, w := range orgWords {
			if f == w {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		// Every word was an org word (e.g. the name is literally "Team") —
		// stripping everything would make unrelated teams compare equal,
		// so fall back to the un-stripped name instead.
		return normalized
	}
	return strings.Join(out, " ")
}

// levenshtein computes the classic single-character edit distance between
// two strings, iterative two-row form (O(len(a)*len(b)) time, O(min) space).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(ra)+1)
	for i := range prev {
		prev[i] = i
	}
	curr := make([]int, len(ra)+1)
	for j := 1; j <= len(rb); j++ {
		curr[0] = j
		for i := 1; i <= len(ra); i++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[i] + 1
			ins := curr[i-1] + 1
			sub := prev[i-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[i] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(ra)]
}

// TeamMatchStatus is a TeamMatchRequest's lifecycle state.
type TeamMatchStatus string

const (
	TeamMatchPending   TeamMatchStatus = "pending"
	TeamMatchConfirmed TeamMatchStatus = "confirmed"
	TeamMatchRejected  TeamMatchStatus = "rejected"
)

// TeamMatchAnswer is one helper's response to "is this the same team?".
type TeamMatchAnswer string

const (
	TeamMatchAnswerYes TeamMatchAnswer = "yes"
	TeamMatchAnswerNo  TeamMatchAnswer = "no"
)

// TeamMatchCandidateKind records what produced a candidate's score, purely
// for operator-facing transparency (never used to gate anything itself).
type TeamMatchCandidateKind string

const (
	CandidateKindFuzzy TeamMatchCandidateKind = "fuzzy"
	CandidateKindCrowd TeamMatchCandidateKind = "crowd"
)

// TeamMatchRequest is one externally-reported team name awaiting
// resolution — see this file's package-level doc comment for the pipeline
// it sits in.
type TeamMatchRequest struct {
	ID            common.RequestID
	ExternalName  string
	Source        Source
	Status        TeamMatchStatus
	BestTeamID    *common.TeamID
	BestScore     int
	CrowdAsksSent int
	CreatedAt     time.Time
	ResolvedAt    *time.Time
}

// TeamMatchCandidate is one local team offered against a request, with the
// team's own display name resolved for rendering (never stored redundantly
// — see the repository's join).
type TeamMatchCandidate struct {
	TeamID   common.TeamID
	TeamName string
	Score    int
	Kind     TeamMatchCandidateKind
	// Yes/No are this candidate's crowd response tally, for the operator
	// card ("3 yes / 1 no") — not used in Score's own computation directly
	// beyond what RecordResponse already folded in.
	Yes, No int
}

// CrowdAdjustedScore applies one more response to a candidate's current
// score, capped at CrowdScoreCap and floored at zero — see this file's
// const doc comments for why the adjustment is asymmetric and capped.
func CrowdAdjustedScore(current int, answer TeamMatchAnswer) int {
	next := current
	switch answer {
	case TeamMatchAnswerYes:
		next += CrowdYesBoost
	case TeamMatchAnswerNo:
		next -= CrowdNoPenalty
	}
	if next > CrowdScoreCap {
		next = CrowdScoreCap
	}
	if next < 0 {
		next = 0
	}
	return next
}

// TeamMatchRepository persists the review queue: requests, their
// candidates, and the crowd's responses to them.
type TeamMatchRepository interface {
	// FindPendingByExternalName looks up an existing, still-open request
	// for (source, externalName) — the idempotency check that stops the
	// same unmatched team from spawning a duplicate request every time it
	// shows up in another new poll.
	FindPendingByExternalName(ctx context.Context, source Source, externalName string) (*TeamMatchRequest, error)
	// CreateRequest records a new request plus its initial candidates in
	// one call — never called for a (source, externalName) that already
	// has a request; see the (source, external_name) UNIQUE constraint.
	CreateRequest(ctx context.Context, req TeamMatchRequest, candidates []TeamMatchCandidate) error
	// AddCandidate appends one more competing team to an already-existing
	// request — used when a second local team also fuzzy-matches the same
	// external name FindPendingByExternalName already found a request for,
	// so that team isn't silently dropped from consideration. A no-op if
	// (requestID, candidate.TeamID) already exists (ON CONFLICT DO NOTHING
	// in the postgres implementation) — callers are expected to also
	// enforce MaxCandidatesPerRequest before calling this.
	AddCandidate(ctx context.Context, requestID common.RequestID, candidate TeamMatchCandidate) error
	// ListPending returns open requests, best-score first (the ones most
	// likely to be a quick, confident tap come first).
	ListPending(ctx context.Context, limit int) ([]TeamMatchRequest, error)
	// FindRequest returns one request with its candidates (including each
	// candidate's crowd tally), or nil if id doesn't exist.
	FindRequest(ctx context.Context, id common.RequestID) (*TeamMatchRequest, []TeamMatchCandidate, error)
	// RecordResponse upserts one helper's answer (a person can only answer
	// a given request once — a repeat call updates their previous answer)
	// and applies CrowdAdjustedScore to that candidate's stored score.
	RecordResponse(ctx context.Context, requestID common.RequestID, userID common.UserID, candidateTeamID common.TeamID, answer TeamMatchAnswer) error
	// HasResponded reports whether userID has already answered requestID —
	// checked before ever asking someone, so nobody is asked twice about
	// the same request even across several shared chats.
	HasResponded(ctx context.Context, requestID common.RequestID, userID common.UserID) (bool, error)
	// IncrementCrowdAsksSent records that n more people were just asked
	// about this request, for the MaxCrowdAsksPerRequest cap.
	IncrementCrowdAsksSent(ctx context.Context, requestID common.RequestID, n int) error
	// Resolve marks a request confirmed (teamID non-nil, saving the
	// identity mapping is the caller's job — see IdentityRepository) or
	// rejected (teamID nil, "no local team matches this").
	Resolve(ctx context.Context, requestID common.RequestID, status TeamMatchStatus, teamID *common.TeamID, at time.Time) error
}

// TeamMatchHelperRepository tracks each person's participation in the
// crowd-review flow: whether they've opted out entirely, and how many
// questions they've been sent in total (MaxLifetimeAsksPerUser).
type TeamMatchHelperRepository interface {
	// EligibleHelpers filters candidates down to those who haven't opted
	// out and are still under MaxLifetimeAsksPerUser, in one round trip —
	// avoids an is-eligible query per candidate when picking who to ask.
	EligibleHelpers(ctx context.Context, candidates []common.UserID) ([]common.UserID, error)
	// RecordAsk increments a person's lifetime asked count.
	RecordAsk(ctx context.Context, userID common.UserID, at time.Time) error
	// SetOptedOut records a person's answer to "keep asking me these?" —
	// true means they've opted out (stop asking), matching the wording
	// people actually see on the follow-up prompt.
	SetOptedOut(ctx context.Context, userID common.UserID, optedOut bool) error
}

// TeamMatchOperatorRepository is the delegated tier of /team_matches
// access: Config.TeamMatchOperatorChatIDs (root operators, fixed at deploy
// time) are always allowed regardless of this repository's contents — this
// only holds people a root operator appointed at runtime via
// /team_match_admin, so access can be delegated without an environment
// variable change.
type TeamMatchOperatorRepository interface {
	IsOperator(ctx context.Context, userID common.UserID) (bool, error)
	AddOperator(ctx context.Context, userID, appointedBy common.UserID) error
	RemoveOperator(ctx context.Context, userID common.UserID) error
	ListOperators(ctx context.Context) ([]common.UserID, error)
}

// SnapshotRepository caches every team a ranking feed (Valve VRS, HLTV, ...)
// currently reports (ranking_snapshot), independent of whether any of them
// have been matched to a local team — see this file's doc comment for why
// the fuzzy-review pipeline needs this separate from RankingRepository.
// Rows are scoped by Source throughout: two different feeds happening to
// rank a similarly-named team must never be compared against each other.
type SnapshotRepository interface {
	// SaveSnapshot upserts ranked, keyed by (source, normalized name) —
	// each entry's own Source field says which feed's cache it belongs to,
	// so a single call can in principle mix sources, though callers today
	// always pass one provider's full result at a time.
	SaveSnapshot(ctx context.Context, ranked []RankedTeam) error
	// AllSnapshot returns every currently-cached entry for one source, for
	// scoring a new unmatched team's name against all of them in Go (the
	// feed is at most a few hundred rows — small enough that this beats
	// maintaining a SQL-side fuzzy-search index for what is, in practice,
	// an occasional lookup).
	AllSnapshot(ctx context.Context, source Source) ([]RankedTeam, error)
}
