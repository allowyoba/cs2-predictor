package competition

import (
	"context"

	"cs2predictor/internal/platform/common"
)

// Player is one person on a team's roster. The nickname is the identity as
// far as this product is concerned — it is what the scene calls them, what a
// chat searches for, and the only thing HLTV publishes — with the real name
// kept alongside because two nicknames occasionally collide and a full name
// is what tells them apart.
type Player struct {
	ID          common.PlayerID
	Game        GameCode
	Nickname    string
	FullName    string
	Nationality string
	ImageURL    string
}

// RosterMember is a player in the context of one team: the position the feed
// listed them at and the role it reported, neither of which belongs on the
// player (both change with the team).
type RosterMember struct {
	Player   Player
	Position int
	Role     string
}

// ProviderRoster is one team's roster exactly as a match provider reported
// it, keyed by that provider's own team id — the shape DataProvider hands
// back before any of it has been resolved to local ids.
type ProviderRoster struct {
	ExternalTeamID string
	Players        []ProviderPlayer
}

// ProviderPlayer is one roster entry as the provider reported it.
type ProviderPlayer struct {
	ExternalID  string
	Nickname    string
	FullName    string
	Nationality string
	ImageURL    string
	Role        string
}

// RosterProvider is the optional half of DataProvider that can report who
// plays for a team. Checked with a type assertion like MatchFactsCatalog, so
// a provider that has no roster data (or a fake in a test) need not pretend
// to: a game whose provider cannot answer this simply has no rosters, which
// is the honest outcome rather than an empty one presented as complete.
type RosterProvider interface {
	// Rosters reports the current roster for each of externalTeamIDs, in one
	// batched call rather than one per team. A team the provider knows
	// nothing about is omitted rather than returned empty — "no roster
	// published" and "an empty roster" are different answers.
	Rosters(ctx context.Context, game GameCode, externalTeamIDs []string) ([]ProviderRoster, error)
}

// RosterRepository is the persistence port for players and team rosters.
type RosterRepository interface {
	// SavePlayer upserts a player by the source's own key, returning the
	// canonical record — the id is ours and stable across renames, so a
	// player whose nickname changed keeps every subscription pointed at
	// them.
	SavePlayer(ctx context.Context, game GameCode, source string, externalID string, player Player) (Player, error)
	// ReplaceRoster makes members the team's whole roster, dropping whoever
	// is no longer on it.
	ReplaceRoster(ctx context.Context, teamID common.TeamID, members []RosterMember) error
	// Roster reads a team's current roster, ordered by position.
	Roster(ctx context.Context, teamID common.TeamID) ([]RosterMember, error)
	// FindPlayer resolves one player by id; (nil, nil) when there is none,
	// the same contract FindEvent follows.
	FindPlayer(ctx context.Context, id common.PlayerID) (*Player, error)
	// SearchPlayers finds players by a case-insensitive substring of their
	// nickname, restricted to games (same "no games enabled, no results"
	// rule as SearchEvents and SearchTeams).
	SearchPlayers(ctx context.Context, query string, limit int, games []GameCode) ([]Player, error)
	// TeamsOfPlayer lists the teams currently rostering this player — the
	// fan-out question behind "a player you follow is playing here", asked
	// of the roster rather than of the subscription.
	TeamsOfPlayer(ctx context.Context, id common.PlayerID) ([]common.TeamID, error)
}
