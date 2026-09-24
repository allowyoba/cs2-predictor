package subscription

import (
	"context"
	"sort"
	"strings"
	"time"

	"cs2predictor/internal/platform/common"
)

// TargetKind is what a chat follows: a whole tournament, a team wherever it
// plays, or a player wherever their team plays.
type TargetKind string

const (
	TargetTournament TargetKind = "TOURNAMENT"
	TargetTeam       TargetKind = "TEAM"
	TargetPlayer     TargetKind = "PLAYER"
)

// Target is one followed thing. Exactly one of EventID/TeamID/Player is
// meaningful, per Kind. Label is display-only.
type Target struct {
	Kind    TargetKind
	EventID common.EventID
	TeamID  common.TeamID
	// Player is normalized (see NormalizePlayer); players are only known by
	// nickname, from ranking rosters.
	Player string
	Label  string
}

func TournamentTarget(id common.EventID) Target {
	return Target{Kind: TargetTournament, EventID: id}
}

func TeamTarget(id common.TeamID, name string) Target {
	return Target{Kind: TargetTeam, TeamID: id, Label: name}
}

func PlayerTarget(nickname string) Target {
	return Target{Kind: TargetPlayer, Player: NormalizePlayer(nickname), Label: strings.TrimSpace(nickname)}
}

// NormalizePlayer folds a nickname the way roster lookups compare it.
func NormalizePlayer(nickname string) string {
	return strings.ToLower(strings.TrimSpace(nickname))
}

// Key identifies a target within one chat, independent of Label.
func (t Target) Key() string {
	switch t.Kind {
	case TargetTournament:
		return "tournament:" + t.EventID.Value.String()
	case TargetTeam:
		return "team:" + t.TeamID.Value.String()
	default:
		return "player:" + t.Player
	}
}

// Target lifts a tournament subscription onto the general form.
func (s EventSubscription) Target() Target { return TournamentTarget(s.EventID) }

// TargetRepository is the generalized persistence port: every kind of
// target is subscribed, unsubscribed and listed through the same calls,
// with tournament targets backed by the existing event subscriptions.
type TargetRepository interface {
	SubscribeTarget(ctx context.Context, chatID common.ChatID, target Target, at time.Time) error
	UnsubscribeTarget(ctx context.Context, chatID common.ChatID, target Target) error
	// Targets lists every active target of every kind. Unlike
	// Repository.Subscriptions it keeps finished tournaments: scoping
	// historical stats needs them.
	Targets(ctx context.Context, chatID common.ChatID) ([]Target, error)
	// ResolveTeams maps each team/player target's Key to the teams it
	// covers today; a player covers the teams whose ranking roster lists them.
	ResolveTeams(ctx context.Context, targets []Target) (map[string][]common.TeamID, error)
	// ChatsFollowingTeams lists chats with a team or player target covering
	// any of teamIDs.
	ChatsFollowingTeams(ctx context.Context, teamIDs []common.TeamID) ([]common.ChatID, error)
	// FollowedEventIDs lists live events the match sync must fetch so
	// team/player followers get their polls.
	FollowedEventIDs(ctx context.Context) ([]common.EventID, error)
	SearchTargets(ctx context.Context, query string, limit int) ([]Target, error)
	// ClaimCrossSellOffer records a tournament offer to chatID and reports
	// whether it may be sent: never twice for one event, and not within
	// cooldown of the chat's previous offer.
	ClaimCrossSellOffer(ctx context.Context, chatID common.ChatID, eventID common.EventID, at time.Time, cooldown time.Duration) (bool, error)
}

// Scope is how much of one tournament a chat may see.
type Scope struct {
	// Full means the whole tournament, every match.
	Full bool
	// Teams, when not Full, is the only slice visible: matches involving
	// any of them. Empty and not Full means nothing is visible.
	Teams []common.TeamID
}

// ScopeFor decides a chat's view of eventID. A tournament subscription
// always wins; otherwise team/player targets restrict the view to their
// teams. A chat with neither keeps the full historical view it always had.
func ScopeFor(eventID common.EventID, targets []Target, resolved map[string][]common.TeamID) Scope {
	restricted := false
	seen := map[common.TeamID]bool{}
	var teams []common.TeamID
	for _, t := range targets {
		switch t.Kind {
		case TargetTournament:
			if t.EventID == eventID {
				return Scope{Full: true}
			}
		case TargetTeam, TargetPlayer:
			restricted = true
			for _, id := range resolved[t.Key()] {
				if !seen[id] {
					seen[id] = true
					teams = append(teams, id)
				}
			}
		}
	}
	if !restricted {
		return Scope{Full: true}
	}
	sort.Slice(teams, func(a, b int) bool { return teams[a].Value.String() < teams[b].Value.String() })
	if teams == nil {
		teams = []common.TeamID{}
	}
	return Scope{Teams: teams}
}

// TeamsOf flattens resolved teams for the given targets, deduplicated.
func TeamsOf(targets []Target, resolved map[string][]common.TeamID) []common.TeamID {
	seen := map[common.TeamID]bool{}
	out := []common.TeamID{}
	for _, t := range targets {
		for _, id := range resolved[t.Key()] {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
