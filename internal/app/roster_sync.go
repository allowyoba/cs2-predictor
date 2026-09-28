package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// defaultRosterTeamsPerRun bounds how many teams one pass asks the match
// provider about. Rosters change on a transfer, not on a tick, so there is
// nothing to gain from sweeping the whole catalogue every run — and the teams
// that matter are the ones with a match coming up, which is a short list.
const defaultRosterTeamsPerRun = 60

// PlayerStore is the write side of the roster pipeline: the player catalogue,
// each feed's own key for a player, and who is on a team now.
type PlayerStore interface {
	competition.RosterRepository
	// SavePlayerIdentity records another feed's name for a player already on
	// record, reporting false when that feed's key belongs to somebody else.
	SavePlayerIdentity(ctx context.Context, id common.PlayerID, source, externalID, externalName, confidence string) (bool, error)
}

// RosterSync keeps the player catalogue and each team's roster current, and
// pairs the two feeds that report players: PandaScore, which gives them an id
// and a nickname, and HLTV, which gives a nickname on a ranking and nothing
// else.
//
// The pairing is deliberately done inside one team's roster at a time. A
// nickname is not unique across the scene, so matching them globally would
// eventually attach somebody's subscription to a different person with the
// same handle; two players on one roster never share a nickname, so scoped
// that way the nickname is a key. See enrichment.MatchRoster.
type RosterSync struct {
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Provider      competition.RosterProvider
	Players       PlayerStore
	// Rankings is where HLTV's roster comes from — it arrives as a
	// by-product of the ranking sync, not from a feed of its own.
	Rankings enrichment.RankingRepository
	Lock     common.ClusterLock
	Log      *slog.Logger
	// TeamsPerRun overrides defaultRosterTeamsPerRun; zero uses it.
	TeamsPerRun int
}

func (s *RosterSync) teamsPerRun() int {
	if s.TeamsPerRun <= 0 {
		return defaultRosterTeamsPerRun
	}
	return s.TeamsPerRun
}

func (s *RosterSync) Dispatch(ctx context.Context) {
	if s.Provider == nil {
		return
	}
	_, err := s.Lock.Execute(ctx, "cs2predictor:roster-sync", func(ctx context.Context) error {
		return s.sync(ctx)
	})
	if err != nil {
		s.Log.Error("roster sync failed", "error", err)
	}
}

func (s *RosterSync) sync(ctx context.Context) error {
	byGame, err := s.teamsToRefresh(ctx)
	if err != nil {
		return err
	}
	for game, teams := range byGame {
		if err := s.syncGame(ctx, game, teams); err != nil {
			// One game's provider having a bad day must not cost the other
			// game its rosters — the same per-game resilience the event
			// sync applies.
			s.Log.Error("roster sync failed for one game, continuing with the rest", "game", game, "error", err)
		}
	}
	return nil
}

// rosterTeam is one team worth asking about: our id, and the match
// provider's own id for it, which is what the roster endpoint is keyed by.
type rosterTeam struct {
	ID         common.TeamID
	ExternalID string
	Name       string
}

// teamsToRefresh is every team with a match still to play (or in play) across
// the tournaments somebody follows, grouped by game. A team nobody is about
// to watch is not worth a request.
func (s *RosterSync) teamsToRefresh(ctx context.Context) (map[competition.GameCode][]rosterTeam, error) {
	eventIDs, err := s.Subscriptions.ActiveEventIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	events, err := s.Catalog.FindEvents(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	gameOf := make(map[common.EventID]competition.GameCode, len(events))
	for _, event := range events {
		gameOf[event.ID] = event.Game
	}
	matches, err := s.Catalog.FindPlayableMatchesForEvents(ctx, eventIDs)
	if err != nil {
		return nil, err
	}

	byGame := map[competition.GameCode][]rosterTeam{}
	seen := map[common.TeamID]bool{}
	for _, match := range matches {
		game, ok := gameOf[match.EventID]
		if !ok {
			continue
		}
		for _, team := range [2]*competition.Team{match.FirstTeam, match.SecondTeam} {
			if team == nil || team.ExternalID == "" || seen[team.ID] {
				continue
			}
			seen[team.ID] = true
			if len(byGame[game]) >= s.teamsPerRun() {
				continue
			}
			byGame[game] = append(byGame[game], rosterTeam{ID: team.ID, ExternalID: team.ExternalID, Name: team.Name})
		}
	}
	return byGame, nil
}

func (s *RosterSync) syncGame(ctx context.Context, game competition.GameCode, teams []rosterTeam) error {
	if len(teams) == 0 {
		return nil
	}
	externalIDs := make([]string, 0, len(teams))
	byExternalID := make(map[string]rosterTeam, len(teams))
	for _, team := range teams {
		externalIDs = append(externalIDs, team.ExternalID)
		byExternalID[team.ExternalID] = team
	}
	rosters, err := s.Provider.Rosters(ctx, game, externalIDs)
	if err != nil {
		return err
	}

	stored := make([]common.TeamID, 0, len(rosters))
	for _, roster := range rosters {
		team, ok := byExternalID[roster.ExternalTeamID]
		if !ok {
			continue
		}
		if len(roster.Players) == 0 {
			// Nothing published is not the same as nobody on the team, and
			// replacing a stored roster with an empty one would wipe it.
			continue
		}
		if err := s.storeRoster(ctx, game, team, roster); err != nil {
			s.Log.Error("roster store failed for one team, continuing with the rest",
				"game", game, "team", team.Name, "error", err)
			continue
		}
		stored = append(stored, team.ID)
	}
	s.Log.Info("rosters stored", "game", game, "asked", len(teams), "stored", len(stored))
	return s.pairWithHLTV(ctx, game, stored)
}

func (s *RosterSync) storeRoster(ctx context.Context, game competition.GameCode, team rosterTeam,
	roster competition.ProviderRoster) error {
	members := make([]competition.RosterMember, 0, len(roster.Players))
	for i, reported := range roster.Players {
		saved, err := s.Players.SavePlayer(ctx, game, providerSourcePandaScore, reported.ExternalID, competition.Player{
			Nickname: reported.Nickname, FullName: reported.FullName,
			Nationality: reported.Nationality, ImageURL: reported.ImageURL,
		})
		if err != nil {
			return err
		}
		members = append(members, competition.RosterMember{Player: saved, Position: i + 1, Role: reported.Role})
	}
	return s.Players.ReplaceRoster(ctx, team.ID, members)
}

// providerSourcePandaScore is the feed key players are first created under.
// It matches the provider name CompetitionRepository stores for teams and
// matches, so a player's origin reads the same as everything else from that
// feed.
const providerSourcePandaScore = "PANDASCORE"

// pairWithHLTV resolves HLTV's roster nicknames to the players just stored,
// one team at a time. Counter-Strike only: HLTV ranks nothing else, so asking
// it about a Dota 2 roster could only produce a wrong answer — the same rule
// TeamMatchService.EnsureRequests applies to teams.
func (s *RosterSync) pairWithHLTV(ctx context.Context, game competition.GameCode, teamIDs []common.TeamID) error {
	if game != competition.GameCS2 || s.Rankings == nil || len(teamIDs) == 0 {
		return nil
	}
	rankings, err := s.Rankings.FindRankings(ctx, teamIDs, enrichment.SourceHLTV)
	if err != nil {
		return err
	}
	matchedTotal, unmatchedTotal := 0, 0
	for _, teamID := range teamIDs {
		ranking, ok := rankings[teamID]
		if !ok || len(ranking.Roster) == 0 {
			continue // HLTV does not rank this team, so it reports no roster
		}
		roster, err := s.Players.Roster(ctx, teamID)
		if err != nil {
			return err
		}
		candidates := make([]enrichment.RosterCandidate, 0, len(roster))
		for _, member := range roster {
			candidates = append(candidates, enrichment.RosterCandidate{
				PlayerID: member.Player.ID, Nickname: member.Player.Nickname,
			})
		}
		matched, unmatched := enrichment.MatchRoster(candidates, ranking.Roster)
		for _, pair := range matched {
			claimed, err := s.Players.SavePlayerIdentity(ctx, pair.PlayerID, string(enrichment.SourceHLTV),
				enrichment.NormalizeNickname(pair.ExternalName), pair.ExternalName, string(pair.Confidence))
			if err != nil {
				return err
			}
			if !claimed {
				// A namesake elsewhere already holds this nickname; leaving
				// it unresolved is the only answer that cannot be wrong.
				unmatchedTotal++
				continue
			}
			matchedTotal++
		}
		unmatchedTotal += len(unmatched)
	}
	s.Log.Info("hltv rosters paired", "teams", len(teamIDs), "matched", matchedTotal, "unmatched", unmatchedTotal)
	return nil
}
