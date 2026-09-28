package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// Recording a prediction that was made in the room but never reached the
// poll: pick the match, pick who predicted, pick what they said.
//
// Entirely by button, with no free text anywhere. Every step is a choice
// between things that actually exist — a match this chat had a poll for, a
// person who has voted here before, a scoreline the poll offered — so there
// is nothing to mistype and nothing to validate beyond "is it on the list".
//
// See app.LateVoteService for why this is a manager's action rather than
// anybody's: nothing can tell a prediction made before a match from one made
// after it, so the guard is a name against the record, kept in the chat's own
// history.

const (
	lateVotePollsShown   = 6
	lateVoteVotersShown  = 8
	lateVoteRecentWindow = 180 // days of voting history the picker draws from
)

// requireLateVote is the common preamble: the feature has to be wired, and
// the caller has to manage this chat.
func (h *UpdateHandler) requireLateVote(ctx context.Context, settings chat.Settings, userID common.UserID) error {
	if h.LateVotes == nil || h.PollReads == nil {
		return newValidationError("late votes are not configured")
	}
	return h.requireManager(ctx, settings.ChatID, userID)
}

// lateVoteMatches lists the matches this chat had a poll for, newest first.
func (h *UpdateHandler) lateVoteMatches(ctx context.Context, target replyTarget, settings chat.Settings) error {
	polls, err := h.PollReads.RecentClosedPolls(ctx, settings.ChatID, lateVotePollsShown)
	if err != nil {
		return err
	}
	back := h.backButton(settings.Locale, "menu:events")
	if len(polls) == 0 {
		return h.respond(ctx, target,
			managedScreenContext(target, settings, h.Texts.Get("latevote.no_polls", settings.Locale)),
			&InlineKeyboard{InlineKeyboard: [][]InlineButton{{back}}})
	}
	rows := make([][]InlineButton, 0, len(polls)+1)
	for _, poll := range polls {
		label, err := h.matchLabel(ctx, poll)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineButton{button(truncate(label, 48), "lv:who:"+compactUUID(poll.ID.Value)+":0")})
	}
	rows = append(rows, []InlineButton{back})
	text := bold(escapeHTML(h.Texts.Get("latevote.title", settings.Locale))) + "\n\n" +
		h.Texts.Get("latevote.explainer", settings.Locale)
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// matchLabel names a poll by its match, with the result when there is one —
// the whole point is that these are matches that have already been played,
// and the score is how somebody recognises which one it was.
func (h *UpdateHandler) matchLabel(ctx context.Context, poll prediction.Poll) (string, error) {
	match, err := h.Catalog.FindMatch(ctx, poll.MatchID)
	if err != nil || match == nil {
		return poll.ID.Value.String(), err
	}
	first, second := "?", "?"
	if match.FirstTeam != nil {
		first = match.FirstTeam.Name
	}
	if match.SecondTeam != nil {
		second = match.SecondTeam.Name
	}
	label := first + " — " + second
	if match.Score != nil {
		label += "  " + match.Score.String()
	}
	return label, nil
}

// lateVoteVoters lists the people who could plausibly have been predicting:
// everyone who has voted in this chat recently. Telegram gives a bot no
// member list, so activity is the only honest answer to "who is in this
// room" — the same basis moderator candidates use.
func (h *UpdateHandler) lateVoteVoters(ctx context.Context, target replyTarget, settings chat.Settings,
	pollID common.PollID, page int) error {
	since := h.Clock.Now().AddDate(0, 0, -lateVoteRecentWindow)
	participants, err := h.PollReads.ChatParticipants(ctx, settings.ChatID, since)
	if err != nil {
		return err
	}
	back := h.backButton(settings.Locale, "lv:polls")
	if len(participants) == 0 {
		return h.respond(ctx, target,
			managedScreenContext(target, settings, h.Texts.Get("latevote.no_voters", settings.Locale)),
			&InlineKeyboard{InlineKeyboard: [][]InlineButton{{back}}})
	}
	// Whoever already has a vote on this poll is marked rather than hidden:
	// recording one again replaces it, which is a legitimate correction and
	// worth being able to see before making.
	existing, err := h.PollReads.Votes(ctx, pollID)
	if err != nil {
		return err
	}
	voted := make(map[common.UserID]bool, len(existing))
	for _, vote := range existing {
		voted[vote.UserID] = true
	}

	totalPages := (len(participants) + lateVoteVotersShown - 1) / lateVoteVotersShown
	page = clampPage(page, totalPages)
	start := page * lateVoteVotersShown
	end := min(start+lateVoteVotersShown, len(participants))

	rows := make([][]InlineButton, 0, lateVoteVotersShown+3)
	for _, userID := range participants[start:end] {
		label := h.userLabel(ctx, userID)
		if voted[userID] {
			label = "✅ " + label
		}
		rows = append(rows, []InlineButton{button(truncate(label, 40),
			fmt.Sprintf("lv:score:%s:%s", compactUUID(pollID.Value), strconv.FormatInt(userID.Value, 36)))})
	}
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return fmt.Sprintf("lv:who:%s:%d", compactUUID(pollID.Value), p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{back})
	return h.respond(ctx, target,
		managedScreenContext(target, settings, bold(escapeHTML(h.Texts.Get("latevote.pick_voter", settings.Locale)))),
		&InlineKeyboard{InlineKeyboard: rows})
}

// lateVoteScores offers the poll's own options, three to a row — the same
// scorelines the poll itself offered, so nothing can be recorded that was
// never on it.
func (h *UpdateHandler) lateVoteScores(ctx context.Context, target replyTarget, settings chat.Settings,
	pollID common.PollID, voter common.UserID) error {
	poll, err := h.PollReads.FindPoll(ctx, pollID)
	if err != nil {
		return err
	}
	if poll == nil || poll.ChatID != settings.ChatID {
		return newValidationError("unknown poll")
	}
	var rows [][]InlineButton
	var row []InlineButton
	for _, option := range poll.Options {
		row = append(row, button(option.Score.String(),
			fmt.Sprintf("lv:do:%s:%s:%d", compactUUID(pollID.Value), strconv.FormatInt(voter.Value, 36), option.Index)))
		if len(row) == 3 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "lv:who:"+compactUUID(pollID.Value)+":0")})

	name := h.userLabel(ctx, voter)
	text := bold(escapeHTML(h.Texts.Get("latevote.pick_score", settings.Locale, name)))
	if label, err := h.matchLabel(ctx, *poll); err == nil {
		text += "\n" + escapeHTML(label)
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// recordLateVote writes it and says what it earned.
func (h *UpdateHandler) recordLateVote(ctx context.Context, cb *CallbackQuery, target replyTarget,
	settings chat.Settings, pollID common.PollID, voter common.UserID, optionIndex int) (bool, error) {
	name := h.userLabel(ctx, voter)
	result, err := h.LateVotes.Record(ctx, settings.ChatID, pollID, voter, name, nil, optionIndex)
	if err != nil {
		return false, err
	}
	// Recorded in the chat's own history, naming who entered it: this is the
	// whole guard on a thing nothing can verify.
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "late_vote",
		fmt.Sprintf("%s %s", name, result.Predicted.String()))

	text := h.Texts.Get("latevote.recorded", settings.Locale,
		escapeHTML(name), escapeHTML(result.Predicted.String()))
	if result.Scored {
		text += "\n" + h.Texts.Get("latevote.scored", settings.Locale, result.Points)
	} else {
		text += "\n" + h.Texts.Get("latevote.pending", settings.Locale)
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("latevote.another", settings.Locale), "lv:polls")},
		{h.backButton(settings.Locale, "menu:events")},
	}}
	return false, h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
}

// parseLateVoteTarget reads the "<poll>:<user base36>" tail both the score
// picker and the write share.
func parseLateVoteTarget(tail string) (common.PollID, common.UserID, int, bool) {
	parts := strings.Split(tail, ":")
	if len(parts) < 2 {
		return common.PollID{}, common.UserID{}, 0, false
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return common.PollID{}, common.UserID{}, 0, false
	}
	userVal, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil {
		return common.PollID{}, common.UserID{}, 0, false
	}
	option := 0
	if len(parts) > 2 {
		if option, err = strconv.Atoi(parts[2]); err != nil || option < 0 {
			return common.PollID{}, common.UserID{}, 0, false
		}
	}
	return common.PollID{Value: id}, common.UserID{Value: userVal}, option, true
}

// lateVotePollID reads a bare poll id off a callback tail.
func lateVotePollID(tail string) (common.PollID, int, bool) {
	pollPart, pagePart, _ := strings.Cut(tail, ":")
	id, err := uuid.Parse(pollPart)
	if err != nil {
		return common.PollID{}, 0, false
	}
	page, err := strconv.Atoi(pagePart)
	if err != nil || page < 0 {
		page = 0
	}
	return common.PollID{Value: id}, page, true
}
