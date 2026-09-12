package pandascore

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

func mapEvent(dto seriesDTO, now time.Time) competition.Event {
	name := seriesDisplayName(dto)

	var status competition.EventStatus
	switch {
	case dto.Status != nil && strings.EqualFold(*dto.Status, "finished"):
		status = competition.EventFinished
	case dto.Status != nil && strings.EqualFold(*dto.Status, "running"):
		status = competition.EventRunning
	case dto.Status != nil && (strings.EqualFold(*dto.Status, "canceled") || strings.EqualFold(*dto.Status, "cancelled")):
		status = competition.EventCancelled
	case dto.EndAt != nil && dto.EndAt.Before(now):
		status = competition.EventFinished
	case dto.BeginAt != nil && dto.BeginAt.Before(now):
		status = competition.EventRunning
	default:
		status = competition.EventUpcoming
	}

	return competition.Event{
		ID:         common.EventID{Value: common.NameUUID("pandascore:event:" + strconv.FormatInt(dto.ID, 10))},
		Game:       competition.GameCS2,
		Name:       name,
		ExternalID: strconv.FormatInt(dto.ID, 10),
		Status:     status,
		StartsAt:   dto.BeginAt,
		EndsAt:     dto.EndAt,
		Provider:   "PANDASCORE",
		Tier:       mapTier(dto.Tier),
	}
}

func seriesDisplayName(dto seriesDTO) string {
	core := strings.TrimSpace(dto.FullName)
	if core == "" {
		core = strings.TrimSpace(dto.Name)
	}
	if core == "" {
		core = "PandaScore series " + strconv.FormatInt(dto.ID, 10)
	}

	var yearPart string
	if dto.Year != nil {
		if year := strconv.Itoa(*dto.Year); !strings.Contains(core, year) {
			yearPart = year
		}
	}

	// A distinct league (not already folded into core, e.g. core="Cologne"
	// vs league="IEM") reads naturally as a prefix: "IEM 2027 Cologne".
	// Without one, core already carries that branding itself (e.g. core=
	// "IEM Cologne"), so the year just appends instead: "IEM Cologne 2027".
	league := strings.TrimSpace(dto.League.Name)
	if league != "" && !strings.Contains(strings.ToLower(core), strings.ToLower(league)) {
		return strings.Join(nonEmpty(league, yearPart, core), " ")
	}
	return strings.Join(nonEmpty(core, yearPart), " ")
}

// nonEmpty returns ss with empty strings dropped, preserving order.
func nonEmpty(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func tierRank(t competition.EventTier) int {
	switch t {
	case competition.TierS:
		return 6
	case competition.TierA:
		return 5
	case competition.TierB:
		return 4
	case competition.TierC:
		return 3
	case competition.TierD:
		return 2
	case competition.TierUnranked:
		return 1
	default:
		return 0
	}
}

func betterTier(a, b competition.EventTier) competition.EventTier {
	if tierRank(b) > tierRank(a) {
		return b
	}
	return a
}

func earlierTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.Before(*b) {
		return a
	}
	return b
}

func laterTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.After(*b) {
		return a
	}
	return b
}

// mapTier normalizes PandaScore's "tier" attribute ("s", "a", "b", "c", "d",
// "unranked", or absent) to our own EventTier — an unrecognized or missing
// value maps to TierUnknown rather than TierUnranked, so it's distinguishable
// from a provider that explicitly reported "unranked".
func mapTier(tier *string) competition.EventTier {
	if tier == nil {
		return competition.TierUnknown
	}
	switch competition.EventTier(strings.ToLower(*tier)) {
	case competition.TierS, competition.TierA, competition.TierB, competition.TierC, competition.TierD, competition.TierUnranked:
		return competition.EventTier(strings.ToLower(*tier))
	default:
		return competition.TierUnknown
	}
}

func mapTeam(dto namedDTO) competition.Team {
	return competition.Team{
		ID:         common.TeamID{Value: common.NameUUID("pandascore:team:" + strconv.FormatInt(dto.ID, 10))},
		Name:       dto.Name,
		ExternalID: strconv.FormatInt(dto.ID, 10),
		Location:   strings.ToUpper(strings.TrimSpace(dto.Location)),
	}
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func mapMatch(dto matchDTO) competition.Match {
	var opponents []namedDTO
	for _, o := range dto.Opponents {
		if o.Opponent != nil {
			opponents = append(opponents, *o.Opponent)
			if len(opponents) == 2 {
				break
			}
		}
	}
	// PandaScore's own opponents[] order is not guaranteed stable across
	// two fetches of the same match (confirmed in production — repeated
	// incidents where a poll built from one fetch's order scored wrong
	// against a later fetch's opposite order). Sorting by the teams' own
	// (stable, provider-assigned) ids makes FirstTeam/SecondTeam a pure
	// function of team identity, never of API response order, so the same
	// two teams always map to the same First/Second regardless of how
	// PandaScore happened to order them this time. This doesn't matter
	// which team ends up "first" — nothing depends on that — only that it
	// never changes for a given pair.
	if len(opponents) == 2 && opponents[0].ID > opponents[1].ID {
		opponents[0], opponents[1] = opponents[1], opponents[0]
	}

	var first, second *competition.Team
	if len(opponents) >= 1 {
		t := mapTeam(opponents[0])
		first = &t
	}
	if len(opponents) >= 2 {
		t := mapTeam(opponents[1])
		second = &t
	}

	results := map[int64]int{}
	for _, r := range dto.Results {
		results[r.TeamID] = r.Score
	}

	var score *competition.MatchScore
	if first != nil && second != nil && dto.Status == "finished" { // intentionally exact-case, unlike the case-insensitive status mapping below
		firstID, firstErr := strconv.ParseInt(first.ExternalID, 10, 64)
		secondID, secondErr := strconv.ParseInt(second.ExternalID, 10, 64)
		firstScore, firstOK := results[firstID]
		secondScore, secondOK := results[secondID]
		// A plain map lookup silently returns 0 for a missing key — exactly
		// indistinguishable from a genuine 0-map result. PandaScore's own
		// results[] aggregate has been observed lagging status flipping to
		// "finished" (most likely right after the deciding map of a BO3+),
		// so require both entries to actually be present before trusting
		// them; otherwise leave score unset, same as an unfinished match —
		// the next sync tick re-fetches and, once the aggregate has caught
		// up, settles correctly instead of locking in a wrong score forever.
		if firstErr == nil && secondErr == nil && firstOK && secondOK {
			score = &competition.MatchScore{First: firstScore, Second: secondScore}
		}
	}

	var status competition.MatchStatus
	switch {
	case dto.Forfeit:
		status = competition.MatchForfeit
	case strings.EqualFold(dto.Status, "running"):
		status = competition.MatchRunning
	case strings.EqualFold(dto.Status, "finished"):
		status = competition.MatchFinished
	case strings.EqualFold(dto.Status, "canceled") || strings.EqualFold(dto.Status, "cancelled"):
		status = competition.MatchCancelled
	case strings.EqualFold(dto.Status, "postponed"):
		status = competition.MatchPostponed
	default:
		status = competition.MatchNotStarted
	}

	var actualStartedAt *time.Time
	if status == competition.MatchRunning || status == competition.MatchFinished {
		actualStartedAt = dto.BeginAt
	}

	n := 1
	if dto.NumberOfGames != nil && *dto.NumberOfGames > 0 {
		n = *dto.NumberOfGames
	}
	kind := competition.BestOf
	if dto.MatchType != nil && *dto.MatchType == "first_to" {
		kind = competition.FirstTo
	} else if n%2 == 0 {
		kind = competition.FixedMaps
	}
	format, _ := competition.NewSeriesFormat(kind, n)

	var stage, stageExternalID *string
	if dto.Tournament != nil {
		s := dto.Tournament.Name
		stage = &s
		id := strconv.FormatInt(dto.Tournament.ID, 10)
		stageExternalID = &id
	}

	return competition.Match{
		ID:              common.MatchID{Value: common.NameUUID("pandascore:match:" + strconv.FormatInt(dto.ID, 10))},
		EventID:         common.EventID{Value: common.NameUUID(fmt.Sprintf("pandascore:event:%d", dto.Serie.ID))},
		ExternalID:      strconv.FormatInt(dto.ID, 10),
		FirstTeam:       first,
		SecondTeam:      second,
		Stage:           stage,
		ScheduledAt:     dto.BeginAt,
		ActualStartedAt: actualStartedAt,
		Status:          status,
		Format:          format,
		Score:           score,
		StageExternalID: stageExternalID,
	}
}
