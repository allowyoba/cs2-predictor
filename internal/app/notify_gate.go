package app

import (
	"context"

	"cs2predictor/internal/platform/common"
)

// NotifyGate is the single question every proactive message has to pass:
// did anyone ask for this. It sits in front of the outbox rather than
// inside the publishers so that a muted notification is never enqueued at
// all — a message that is written, delivered and then dropped costs a row,
// a retry budget and a Telegram call to achieve silence.
//
// A gate with no store behind it answers no. That is not defensive
// programming, it is the default itself: nothing here is on until
// something says it is, and a misconfigured deployment should go quiet
// rather than start broadcasting.
type NotifyGate struct {
	Switches common.NotifySwitchboard
}

// ChatWants reports whether this chat asked for that kind of post.
func (g NotifyGate) ChatWants(ctx context.Context, chatID common.ChatID, kind common.ChatNotificationKind) (bool, error) {
	return g.wants(ctx, common.ScopeChat, chatID.Value, string(kind))
}

// UserWants reports whether this person asked for that kind of DM.
func (g NotifyGate) UserWants(ctx context.Context, userID common.UserID, kind common.NotificationKind) (bool, error) {
	return g.wants(ctx, common.ScopeUser, userID.Value, string(kind))
}

// OperatorWants reports whether this operator chat asked for that alert.
// Recovery notices travel under the kind that opened them, so an operator
// who turned an alert on always hears that it is over.
func (g NotifyGate) OperatorWants(ctx context.Context, chatID int64, kind common.AdminAlertKind) (bool, error) {
	return g.wants(ctx, common.ScopeOperator, chatID, string(common.AlertSwitch(kind)))
}

// ChatsWanting narrows a batch in one query.
func (g NotifyGate) ChatsWanting(ctx context.Context, kind common.ChatNotificationKind, chatIDs []int64) (map[int64]bool, error) {
	if g.Switches == nil || len(chatIDs) == 0 {
		return map[int64]bool{}, nil
	}
	ids, err := g.Switches.NotifySubjects(ctx, common.ScopeChat, string(kind), chatIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (g NotifyGate) wants(ctx context.Context, scope common.NotifyScope, subject int64, kind string) (bool, error) {
	if g.Switches == nil {
		return false, nil
	}
	return g.Switches.NotifyEnabled(ctx, scope, subject, kind)
}
