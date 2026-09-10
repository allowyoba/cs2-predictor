package telegram

import (
	"context"

	"cs2predictor/internal/platform/common"
)

// helpView renders the static command-reference screen (see help.text in
// the i18n bundles) — the one screen that never depends on which chat it
// was opened from, so unlike every other screen in this package it takes a
// bare locale/back-target pair instead of a chat.Settings.
func (h *UpdateHandler) helpView(ctx context.Context, target replyTarget, locale common.LocaleCode, backData string) error {
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, backData)}}}
	return h.respond(ctx, target, h.Texts.Get("help.text", locale), kb)
}
