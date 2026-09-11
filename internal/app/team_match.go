package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// teamMatchParticipantWindow bounds "recent" the same way
// screens_moderators.go's participantWindow does for moderator candidates:
// Telegram gives a bot no member list, so a plausible helper is someone who
// has actually voted in the chat lately, not everyone who ever has.
const teamMatchParticipantWindow = 90 * 24 * time.Hour

// teamMatchAskBatchSize bounds how many NEW helpers get asked per single
// trigger (one new poll in one chat) — spread out over however many polls
// a team keeps appearing in, rather than all at once.
const teamMatchAskBatchSize = 2

// TeamMatchService is the identity-resolution pipeline's third tier: for a
// team that has no cached Valve VRS ranking at all, it either accepts a
// near-certain fuzzy name match automatically, or opens a TeamMatchRequest
// that an operator resolves via /team_matches — optionally informed by a
// few chat members' yes/no votes first (RecordResponse; see
// enrichment.CrowdAdjustedScore for why the crowd alone can never confirm
// one). See internal/domain/enrichment/team_match.go for the scoring rules
// this pipeline is built on.
type TeamMatchService struct {
	Requests  enrichment.TeamMatchRepository
	Helpers   enrichment.TeamMatchHelperRepository
	Snapshots enrichment.SnapshotRepository
	Rankings  enrichment.RankingRepository
	Identity  enrichment.IdentityRepository
	// Predictions is the plain repository (not *prediction.Service):
	// ChatParticipants is all this needs, the same minimal dependency
	// PollReminderService takes for the same "who plays in this chat"
	// question.
	Predictions prediction.Repository
	Chats       chat.Repository
	Outbox      common.Outbox
	Clock       common.Clock
	Log         *slog.Logger
	// OperatorChatIDs receive a one-line ping when a new request is
	// created — DEPLOY_NOTIFY_CHAT_IDS, reused rather than introducing a
	// second admin-contact list (see Config.TeamMatchOperatorChatIDs).
	OperatorChatIDs []int64
}

// EnsureRequest is the entry point, called once per team per newly
// discovered match (see CompetitionSynchronization.fanOutNewPolls) — never
// per chat, since the fuzzy search and snapshot read are the same
// regardless of which chat the match is being announced to. Returns the
// pending request id a chat's participants can be asked about
// (AskChatHelpers), or nil if the team was auto-accepted or isn't
// plausibly ranked by Valve at all.
func (s *TeamMatchService) EnsureRequest(ctx context.Context, team competition.Team) (*common.RequestID, error) {
	if existing, err := s.Rankings.FindRanking(ctx, team.ID, enrichment.SourceValveVRS); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, nil // already resolved
	}

	snapshot, err := s.Snapshots.AllSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	best, bestScore := bestSnapshotMatch(team.Name, snapshot)
	if bestScore < enrichment.FuzzyRequestThreshold {
		return nil, nil // not a plausible match — most likely just unranked
	}
	if bestScore >= enrichment.FuzzyAutoAcceptThreshold {
		return nil, s.autoAccept(ctx, team.ID, best)
	}

	existing, err := s.Requests.FindPendingByExternalName(ctx, enrichment.SourceValveVRS, best.Identity.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &existing.ID, nil
	}

	req := enrichment.TeamMatchRequest{
		ID: common.NewRequestID(), ExternalName: best.Identity.Name, Source: enrichment.SourceValveVRS,
		Status: enrichment.TeamMatchPending, BestTeamID: &team.ID, BestScore: bestScore, CreatedAt: s.Clock.Now(),
	}
	candidate := enrichment.TeamMatchCandidate{TeamID: team.ID, TeamName: team.Name, Score: bestScore, Kind: enrichment.CandidateKindFuzzy}
	if err := s.Requests.CreateRequest(ctx, req, []enrichment.TeamMatchCandidate{candidate}); err != nil {
		return nil, err
	}
	s.notifyOperators(ctx, req)
	return &req.ID, nil
}

// autoAccept saves both the identity mapping and the ranking itself
// immediately, from the snapshot row already in hand — a team resolved
// this way shows up with its VRS rank starting with the very poll that
// triggered the match, rather than waiting for the next scheduled sync.
func (s *TeamMatchService) autoAccept(ctx context.Context, teamID common.TeamID, best enrichment.RankedTeam) error {
	externalID := enrichment.NormalizeTeamName(best.Identity.Name)
	if err := s.Identity.SaveIdentity(ctx, teamID, enrichment.SourceValveVRS, externalID, best.Identity.Name, enrichment.ConfidenceFuzzyName); err != nil {
		return err
	}
	return s.Rankings.SaveRanking(ctx, enrichment.TeamRanking{
		TeamID: teamID, GlobalRank: best.GlobalRank, RegionalRank: best.RegionalRank,
		Region: best.Region, Points: best.Points, Roster: best.Identity.Roster,
		PublishedAt: best.PublishedAt, Source: enrichment.SourceValveVRS,
	})
}

// bestSnapshotMatch scores name against every cached Valve entry and
// returns the highest-scoring one. The snapshot is at most a few hundred
// rows (see enrichment.SnapshotRepository's doc comment), so scoring all of
// them in Go on an occasional lookup like this is simpler than any SQL-side
// fuzzy-search scheme would be.
func bestSnapshotMatch(name string, snapshot []enrichment.RankedTeam) (enrichment.RankedTeam, int) {
	var best enrichment.RankedTeam
	bestScore := -1
	for _, rt := range snapshot {
		if score := enrichment.FuzzyNameScore(name, rt.Identity.Name); score > bestScore {
			best, bestScore = rt, score
		}
	}
	if bestScore < 0 {
		return enrichment.RankedTeam{}, 0
	}
	return best, bestScore
}

// notifyOperators pings every configured operator once when a new request
// is created — not on every crowd response or every chat it comes up in
// again, which is the spam this whole design otherwise avoids. Best
// effort: a failed enqueue is logged, never propagated (this runs inline
// in the poll-creation path, and a notification failure must not stop a
// poll from going out).
func (s *TeamMatchService) notifyOperators(ctx context.Context, req enrichment.TeamMatchRequest) {
	for _, chatID := range s.OperatorChatIDs {
		n := common.TeamMatchOperatorPingNotification{ChatID: chatID, ExternalName: req.ExternalName}
		payload, err := json.Marshal(n)
		if err != nil {
			s.Log.Error("team match operator notify marshal failed", "error", err)
			continue
		}
		if _, err := s.Outbox.Enqueue(ctx, "TEAM_MATCH_REQUEST", req.ID.String(), "telegram.team-match-operator-ping", string(payload)); err != nil {
			s.Log.Error("team match operator notify enqueue failed", "chatId", chatID, "error", err)
		}
	}
}

// AskChatHelpers asks up to teamMatchAskBatchSize eligible participants of
// chatID about requestID's current best candidate, bounded overall by
// enrichment.MaxCrowdAsksPerRequest — called once per subscribed chat a
// still-unresolved team's new poll goes out to (see
// CompetitionSynchronization.fanOutNewPolls), so a team that keeps
// appearing gets more chances to be resolved without ever exceeding the
// per-request or per-person caps.
func (s *TeamMatchService) AskChatHelpers(ctx context.Context, requestID common.RequestID, chatID common.ChatID, locale common.LocaleCode) error {
	req, candidates, err := s.Requests.FindRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if req == nil || req.Status != enrichment.TeamMatchPending || len(candidates) == 0 {
		return nil
	}
	remaining := min(enrichment.MaxCrowdAsksPerRequest-req.CrowdAsksSent, teamMatchAskBatchSize)
	if remaining <= 0 {
		return nil
	}

	eligible, err := s.eligibleHelpersFor(ctx, chatID)
	if err != nil {
		return err
	}

	top := candidates[0] // FindRequest orders by score DESC
	asked := 0
	for _, userID := range eligible {
		if asked >= remaining {
			break
		}
		answered, err := s.Requests.HasResponded(ctx, requestID, userID)
		if err != nil {
			return err
		}
		if answered {
			continue
		}
		if err := s.ask(ctx, userID, *req, top, locale); err != nil {
			s.Log.Error("team match ask enqueue failed", "userId", userID.Value, "requestId", requestID.String(), "error", err)
			continue
		}
		asked++
	}
	if asked == 0 {
		return nil
	}
	return s.Requests.IncrementCrowdAsksSent(ctx, requestID, asked)
}

// eligibleHelpersFor narrows chatID's recent voters down to people the bot
// can actually DM and who haven't opted out or exhausted their lifetime
// quota — the three independent filters AskChatHelpers needs before it can
// pick anyone to ask.
func (s *TeamMatchService) eligibleHelpersFor(ctx context.Context, chatID common.ChatID) ([]common.UserID, error) {
	participants, err := s.Predictions.ChatParticipants(ctx, chatID, s.Clock.Now().Add(-teamMatchParticipantWindow))
	if err != nil {
		return nil, err
	}
	reachable, err := s.Chats.FilterDMReachable(ctx, participants)
	if err != nil {
		return nil, err
	}
	return s.Helpers.EligibleHelpers(ctx, reachable)
}

func (s *TeamMatchService) ask(ctx context.Context, userID common.UserID, req enrichment.TeamMatchRequest, candidate enrichment.TeamMatchCandidate, locale common.LocaleCode) error {
	n := common.TeamMatchAskNotification{
		UserID: userID.Value, RequestID: req.ID.String(), ExternalName: req.ExternalName,
		CandidateTeamID: candidate.TeamID.Value.String(), CandidateName: candidate.TeamName,
		Locale: string(locale),
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if _, err := s.Outbox.Enqueue(ctx, "TEAM_MATCH_REQUEST", req.ID.String(), "telegram.team-match-ask", string(payload)); err != nil {
		return err
	}
	return s.Helpers.RecordAsk(ctx, userID, s.Clock.Now())
}
