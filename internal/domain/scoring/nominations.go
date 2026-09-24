package scoring

import (
	"sort"

	"cs2predictor/internal/platform/common"
)

// NominationKind names one "interesting nomination" over a period.
type NominationKind string

const (
	NominationBestAccuracy  NominationKind = "best_accuracy"
	NominationMostActive    NominationKind = "most_active"
	NominationActiveChat    NominationKind = "most_active_chat"
	NominationBestStreak    NominationKind = "best_streak"
	NominationBiggestUpset  NominationKind = "biggest_upset"
	NominationMinAccuracyN                 = 10
	NominationMinStreak                    = 3
	NominationFactsMax                     = 5000
	nominationUpsetMinPlace                = 1
)

// Nomination is one winner. Value is the headline number (percent, count,
// streak length or ranking places); Sample is what it rests on.
type Nomination struct {
	Kind     NominationKind
	UserID   common.UserID
	UserName string
	ChatID   common.ChatID
	Value    int
	Sample   int
	// Picked/Opponent describe the upset call.
	Picked   string
	Opponent string
}

// Nominate picks winners from settled facts. A nomination with no
// qualifying candidate is left out rather than awarded on noise.
func Nominate(facts []PredictionFact) []Nomination {
	if len(facts) == 0 {
		return nil
	}
	ordered := make([]PredictionFact, len(facts))
	copy(ordered, facts)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].PlayedAt.Before(ordered[b].PlayedAt) })

	type tally struct {
		name           string
		correct, total int
		run, best      int
	}
	users := map[common.UserID]*tally{}
	chats := map[common.ChatID]int{}
	var upset *Nomination
	for _, f := range ordered {
		t := users[f.UserID]
		if t == nil {
			t = &tally{name: f.UserName}
			users[f.UserID] = t
		}
		t.total++
		chats[f.ChatID]++
		if f.Correct {
			t.correct++
			t.run++
			if t.run > t.best {
				t.best = t.run
			}
		} else {
			t.run = 0
		}
		if f.Correct && f.Ranked() && -f.RankGap() >= nominationUpsetMinPlace && (upset == nil || -f.RankGap() > upset.Value) {
			upset = &Nomination{Kind: NominationBiggestUpset, UserID: f.UserID, UserName: f.UserName, ChatID: f.ChatID,
				Value: -f.RankGap(), Sample: 1, Picked: f.PickedTeam, Opponent: f.OpponentTeam}
		}
	}

	// Deterministic order so ties resolve the same way on every load.
	ids := make([]common.UserID, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a].Value < ids[b].Value })

	var accuracy, active, streak *Nomination
	for _, id := range ids {
		t := users[id]
		if t.total >= NominationMinAccuracyN {
			pct := accuracyPercent(t.correct, t.total)
			if accuracy == nil || pct > accuracy.Value || (pct == accuracy.Value && t.total > accuracy.Sample) {
				accuracy = &Nomination{Kind: NominationBestAccuracy, UserID: id, UserName: t.name, Value: pct, Sample: t.total}
			}
		}
		if active == nil || t.total > active.Value {
			active = &Nomination{Kind: NominationMostActive, UserID: id, UserName: t.name, Value: t.total, Sample: t.total}
		}
		if t.best >= NominationMinStreak && (streak == nil || t.best > streak.Value) {
			streak = &Nomination{Kind: NominationBestStreak, UserID: id, UserName: t.name, Value: t.best, Sample: t.total}
		}
	}

	chatIDs := make([]common.ChatID, 0, len(chats))
	for id := range chats {
		chatIDs = append(chatIDs, id)
	}
	sort.Slice(chatIDs, func(a, b int) bool { return chatIDs[a].Value < chatIDs[b].Value })
	var chat *Nomination
	for _, id := range chatIDs {
		if chat == nil || chats[id] > chat.Value {
			chat = &Nomination{Kind: NominationActiveChat, ChatID: id, Value: chats[id], Sample: chats[id]}
		}
	}

	var out []Nomination
	for _, n := range []*Nomination{accuracy, streak, upset, active, chat} {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}
