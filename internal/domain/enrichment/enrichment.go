// Package enrichment holds optional match/team context sourced from
// providers other than PandaScore: Valve Regional Standings, plus ports
// reserved for GRID and Liquipedia. PandaScore stays the sole source of
// truth for events, tournaments, matches, schedule and results; nothing
// here may gate or block match sync, poll creation, or any Telegram
// interaction. Every consumer treats a missing or failed lookup as
// "omit this data", never as an error to surface to the user. No
// adapter or app package may be imported here.
package enrichment

import (
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Source identifies which external provider produced a piece of
// enrichment data. Cache rows and provider health are tracked per
// Source, so one provider's absence or failure never touches another's
// data.
type Source string

const (
	SourceValveVRS   Source = "VALVE_VRS"
	SourceGRID       Source = "GRID"
	SourceLiquipedia Source = "LIQUIPEDIA"
	// SourceHLTV is HLTV.org's own weekly world ranking (distinct from
	// SourceValveVRS, which is Valve's own official standings — HLTV
	// happens to mirror both on its site, but they are separate rankings
	// with separate update cadences and must never be merged into one
	// cache row).
	SourceHLTV Source = "HLTV"
)

// RanksGame reports whether this source's team ranking applies to game at
// all.
//
// Valve's VRS and HLTV's world ranking rank Counter-Strike teams and
// nothing else. The organisations behind them are not confined to one
// game — BetBoom, Spirit, Falcons and many others field a CS2 roster and a
// Dota 2 roster under the same name — but a ranking earned by one of those
// rosters says nothing whatsoever about the other. Matching a ranking feed
// against team names alone therefore has to be told which game it is
// allowed to touch, or it will confidently attach a Counter-Strike
// ranking to a Dota 2 team that merely shares its organisation's name.
func RanksGame(source Source, game competition.GameCode) bool {
	switch source {
	case SourceValveVRS, SourceHLTV:
		return game == competition.GameCS2
	default:
		// No other source produces team rankings today; a new one has to
		// answer this question for itself rather than inherit a default
		// that happens to be permissive.
		return false
	}
}

// RankedGames lists the games a ranking source covers — the filter its
// sync applies when loading the teams it may match against.
func RankedGames(source Source) []competition.GameCode {
	var games []competition.GameCode
	for _, game := range competition.Games {
		if RanksGame(source, game) {
			games = append(games, game)
		}
	}
	return games
}

// TeamIdentity is how a provider refers to a team before it has been
// resolved to our own common.TeamID: a reported name plus whatever
// roster the provider published alongside it (roster may be empty).
type TeamIdentity struct {
	Name   string
	Roster []string
	// LogoURL is the crest the ranking feed published alongside the name.
	// HLTV's ranking carries one for every Counter-Strike team it lists,
	// which makes it the best CS2 source there is: the same feed that
	// decides the ranking also decides what the team looks like.
	LogoURL string
}

// RankedTeam is one row of a provider's ranking feed, still in the
// provider's own external identity. FetchRankings returns these;
// matching them to a common.TeamID, or discarding unmatched ones, is
// the sync job's responsibility, not the provider's.
type RankedTeam struct {
	Identity     TeamIdentity
	GlobalRank   *int
	RegionalRank *int
	Region       string // "europe" | "americas" | "asia", empty if unknown
	Points       *int
	PublishedAt  time.Time // the ranking snapshot's own date, not fetch time
	Source       Source
}

// TeamRanking is a RankedTeam after identity resolution — the cached,
// display-ready form keyed by our own team.
type TeamRanking struct {
	TeamID       common.TeamID
	GlobalRank   *int
	RegionalRank *int
	Region       string
	Points       *int
	Roster       []string
	PublishedAt  time.Time
	Source       Source
}

// NewTeamRanking builds a TeamRanking from an already identity-resolved
// RankedTeam — the exact same mapping whether the resolution came from the
// scheduled sync (RankingSync) or from an operator/crowd confirming a
// fuzzy match (TeamMatchService), so both call this instead of repeating
// the struct literal.
func NewTeamRanking(teamID common.TeamID, rt RankedTeam) TeamRanking {
	return TeamRanking{
		TeamID: teamID, GlobalRank: rt.GlobalRank, RegionalRank: rt.RegionalRank,
		Region: rt.Region, Points: rt.Points, Roster: rt.Identity.Roster,
		PublishedAt: rt.PublishedAt, Source: rt.Source,
	}
}

// RecentForm is a team's win/loss record over its last sampled matches.
type RecentForm struct {
	Wins   int
	Losses int
	Sample int // matches actually included; may be less than the requested window
	Source Source
}

// HeadToHead is two teams' record against each other, TeamA/TeamB matching
// whichever order the caller requested them in.
type HeadToHead struct {
	TeamAWins int
	TeamBWins int
	Sample    int
	Source    Source
}

// TeamInsight is everything assembled for one side of a match — every
// field is optional; nil means "no enrichment available", never "zero".
type TeamInsight struct {
	TeamID  common.TeamID
	Ranking *TeamRanking
	Form    *RecentForm
}

// MatchInsight is the aggregated enrichment for a single match — the only
// shape Telegram ever sees, never a specific provider's own model.
type MatchInsight struct {
	Home TeamInsight
	Away TeamInsight
	H2H  *HeadToHead
}

// IsEmpty is true when there is nothing at all to show — callers use this
// to skip enrichment entirely rather than render an empty block.
func (m *MatchInsight) IsEmpty() bool {
	return m == nil || (m.Home.Ranking == nil && m.Home.Form == nil &&
		m.Away.Ranking == nil && m.Away.Form == nil && m.H2H == nil)
}

// TournamentMetadata is what a TournamentMetadataProvider (Liquipedia, a
// later phase) can add to an event's own PandaScore-sourced name.
type TournamentMetadata struct {
	FullName string
	Series   string
	Region   string
	Stage    string
}
