package enrichment

import (
	"context"
	"errors"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// RankingProvider is implemented by a source of official/algorithmic team
// rankings — Valve VRS today. FetchRankings returns every currently ranked
// team in one call; enrichment sync matches them against the local catalog
// and caches the result, so nothing here is ever called per-team or from
// the Telegram request path.
type RankingProvider interface {
	FetchRankings(ctx context.Context) ([]RankedTeam, error)
}

// TeamStatsProvider is an optional extension (GRID) for recent-form/team
// statistics beyond what a ranking snapshot carries.
type TeamStatsProvider interface {
	GetTeamStats(ctx context.Context, team TeamIdentity) (*RecentForm, error)
}

// MatchStatsProvider is an optional extension (GRID) for match-level
// statistics, including head-to-head.
type MatchStatsProvider interface {
	GetHeadToHead(ctx context.Context, teamA, teamB TeamIdentity) (*HeadToHead, error)
}

// TournamentMetadataProvider is an optional extension (Liquipedia) for
// richer tournament naming/context than PandaScore provides.
type TournamentMetadataProvider interface {
	EnrichTournament(ctx context.Context, externalName string) (*TournamentMetadata, error)
}

// OddsProvider has no implementation wired up: no free odds source has
// been found stable enough to commit to. Declared now so MatchInsight
// and the aggregation layer can grow into it without another
// interface-shaped change later.
type OddsProvider interface {
	GetMatchOdds(ctx context.Context, matchID common.MatchID) (*MatchOdds, error)
}

type MatchOdds struct {
	Home float64
	Away float64
}

// TeamLister is an optional Catalog extension: enrichment sync needs every
// known team to match a ranking feed against (Valve's team list exists
// independently of any single match), which the regular per-match Catalog
// lookups don't provide. Kept separate from competition.Catalog itself so
// the many existing Catalog fakes across the test suite don't all need to
// implement it — the same "optional capability via type assertion" pattern
// already used for MatchFactsCatalog/AdministratorLister/ModeratorLister.
type TeamLister interface {
	// games narrows the result to those games. It is not an optimization:
	// a ranking feed matched against a team from a game it does not cover
	// produces a confidently wrong answer whenever two rosters share an
	// organisation's name — see RanksGame. An empty games list returns
	// every team, for the callers that genuinely want all of them.
	ListTeams(ctx context.Context, games ...competition.GameCode) ([]competition.Team, error)
}

// RankingRepository persists/reads cached TeamRanking rows.
type RankingRepository interface {
	SaveRanking(ctx context.Context, ranking TeamRanking) error
	FindRanking(ctx context.Context, teamID common.TeamID, source Source) (*TeamRanking, error)
	// FindRankings batch-fetches rankings for several teams at once, for
	// rendering a match's two sides without two separate lookups.
	FindRankings(ctx context.Context, teamIDs []common.TeamID, source Source) (map[common.TeamID]TeamRanking, error)
}

// FormRepository persists/reads cached RecentForm rows, keyed by team.
type FormRepository interface {
	SaveForm(ctx context.Context, teamID common.TeamID, form RecentForm) error
	FindForm(ctx context.Context, teamID common.TeamID, source Source) (*RecentForm, error)
}

// HeadToHeadRepository persists/reads cached HeadToHead rows, keyed by an
// order-independent team pair — FindHeadToHead must return the same row
// regardless of which of the two teams is passed as teamA/teamB, remapping
// TeamAWins/TeamBWins to match the order the caller asked for.
type HeadToHeadRepository interface {
	SaveHeadToHead(ctx context.Context, teamA, teamB common.TeamID, h2h HeadToHead) error
	FindHeadToHead(ctx context.Context, teamA, teamB common.TeamID, source Source) (*HeadToHead, error)
}

// TournamentMetadataRepository persists/reads cached TournamentMetadata,
// keyed by our own EventID (resolved once per event via a provider lookup
// by name, then cached — an event's metadata doesn't change match to
// match).
type TournamentMetadataRepository interface {
	SaveTournamentMetadata(ctx context.Context, eventID common.EventID, meta TournamentMetadata, source Source) error
	FindTournamentMetadata(ctx context.Context, eventID common.EventID, source Source) (*TournamentMetadata, error)
}

// MatchConfidence records which matching step actually resolved a team
// identity — kept alongside the mapping for audit/debugging, never used to
// gate display (an accepted match is an accepted match).
type MatchConfidence string

const (
	ConfidenceExternalID MatchConfidence = "exact_id"
	ConfidenceExactName  MatchConfidence = "exact_name"
	ConfidenceAlias      MatchConfidence = "alias"
	ConfidenceRoster     MatchConfidence = "roster"
	// ConfidenceFuzzyName is an automatic match on name similarity alone
	// (team_match.go's FuzzyNameScore, at or above FuzzyAutoAcceptThreshold)
	// — high enough to trust without a human, but recorded distinctly so
	// it's visible in an audit that no exact/alias/roster step actually
	// fired.
	ConfidenceFuzzyName MatchConfidence = "fuzzy_name"
	// ConfidenceManual is an operator's explicit decision via the
	// /team_matches review queue — the only confidence level a human
	// chose directly, as opposed to one a matching step computed.
	ConfidenceManual MatchConfidence = "manual"
)

// IdentityRepository resolves/records the mapping between our internal team
// catalog and an external provider's own team identifiers/names.
type IdentityRepository interface {
	// FindTeamByExternalID looks up an already-confirmed mapping, the first
	// and cheapest step of the matching pipeline.
	FindTeamByExternalID(ctx context.Context, source Source, externalID string) (*common.TeamID, error)
	// SaveIdentity records/refreshes a confirmed mapping (an upsert keyed on
	// (source, externalID)) plus the externally-observed name as an alias,
	// so future runs can skip straight to FindTeamByExternalID.
	SaveIdentity(ctx context.Context, teamID common.TeamID, source Source, externalID, externalName string, confidence MatchConfidence) error
	// Aliases returns every known alternate name for a team (curated plus
	// externally-observed), for the exact-name/alias matching steps.
	Aliases(ctx context.Context, teamID common.TeamID) ([]string, error)
	// AllAliases batch-fetches every team's aliases in one round trip — a
	// sync job matching a whole ranking feed (hundreds of teams) against
	// the local catalog needs all of them anyway, so this avoids an
	// Aliases-per-team N+1 there.
	AllAliases(ctx context.Context) (map[common.TeamID][]string, error)
}

// SyncState is one provider's scheduled-sync health — backs both the
// circuit breaker and /healthz/ready's per-provider status.
type SyncState struct {
	Provider            Source
	LastSuccessAt       *time.Time
	LastErrorAt         *time.Time
	LastError           string
	ConsecutiveFailures int
}

// SyncStateRepository persists SyncState across restarts, so a fresh
// deployment doesn't forget an ongoing outage and immediately hammer a
// provider that was mid-cooldown.
type SyncStateRepository interface {
	RecordSuccess(ctx context.Context, provider Source) error
	RecordFailure(ctx context.Context, provider Source, errText string) error
	State(ctx context.Context, provider Source) (*SyncState, error)
}

// ErrFetchPending is returned by a RankingProvider whose fetch is a remote
// job that hasn't finished yet — the work is under way and will be
// collected on a later tick. It is not a failure: RankingSync neither
// records it against the source's health nor counts it as a success, so a
// slow provider never looks like a broken one.
var ErrFetchPending = errors.New("provider fetch still running")

// CachedRankingProvider is an optional extension for a provider whose
// results already exist somewhere outside this application — an Apify run
// that finished while the bot was down, say. FetchLatestCached reads the
// freshest such result without starting (or paying for) new work, which is
// what makes it safe to call on every startup.
type CachedRankingProvider interface {
	// FetchLatestCached returns the newest result the provider already
	// holds, or nil when it holds none. Never starts remote work.
	FetchLatestCached(ctx context.Context) ([]RankedTeam, error)
}

// ProviderRun tracks one long-running fetch a provider started remotely and
// will collect later, across ticks and restarts. It exists so a fetch that
// outlives the request that started it is never started twice — decisive
// for a provider billed per run.
type ProviderRun struct {
	Provider Source
	// Key separates several kinds of run under one Source — the Apify
	// actor's rankingType, say, where one actor produces two rankings.
	Key    string
	RunID  string
	Status string
	// Attempts counts the runs started since PeriodStart, so a provider
	// that keeps failing costs a bounded number of runs per period instead
	// of one per tick.
	Attempts    int
	PeriodStart time.Time
	StartedAt   time.Time
}

// RunStatusCollected marks a ProviderRun whose result has been read and
// stored. The row is kept, rather than deleted, because "this period's run
// is already done" is what a weekly gate has to check — and it cannot ask
// SyncState for it when two jobs write the same Source (a paid weekly feed
// alongside a free frequent one, where the free one's successes would
// otherwise permanently convince the paid one it has already run).
const RunStatusCollected = "COLLECTED"

// ProviderRunRepository persists ProviderRun. Keyed on (Provider, Key).
type ProviderRunRepository interface {
	SaveRun(ctx context.Context, run ProviderRun) error
	Run(ctx context.Context, provider Source, key string) (*ProviderRun, error)
	ClearRun(ctx context.Context, provider Source, key string) error
}
