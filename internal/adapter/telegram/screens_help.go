package telegram

import (
	"context"

	"cs2predictor/internal/platform/common"
)

// helpView renders the command reference for the surface it was opened
// from: a group is shown the commands that work in a group, a private chat
// the ones that work there.
//
// It used to be one screen listing both, which meant half of it was always
// about somewhere else — the single most common way a help screen stops
// being read. The page also carries the one link people actually come
// looking for next to "what are the commands": how points are scored.
func (h *UpdateHandler) helpView(ctx context.Context, target replyTarget, locale common.LocaleCode, backData string, private bool) error {
	key := "help.group"
	if private {
		key = "help.private"
	}
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("menu.rules", locale), rulesTarget(backData))},
		{h.backButton(locale, backData)},
	}}
	return h.respond(ctx, target, h.Texts.Get(key, locale), kb)
}

// rulesTarget encodes where the rules screen should return to, so opening
// the rules from help does not drop somebody back at a menu they did not
// come from.
func rulesTarget(backData string) string {
	if backData == "" {
		return "menu:rules"
	}
	return "menu:rules:" + backData
}
