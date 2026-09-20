package telegram

import (
	"context"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// First run.
//
// A bot added to a group used to say nothing at all: the chat row was
// created by whoever typed first, and until somebody went looking through
// the settings for the games list, nothing ever happened. That is a
// product that looks broken for as long as nobody guesses.
//
// So the bot greets the room the moment it is added, says in one line what
// it is for, and offers the single step that unblocks everything else.
// Each step announces the next one when it completes — see
// setGameEnabled — and none of them is repeated once it is done.

// welcomeNewChat creates the chat's row and posts the first step. Called
// when the bot's own membership turns into "present" and the chat has no
// row yet, which is exactly once per group in practice.
func (h *UpdateHandler) welcomeNewChat(ctx context.Context, update *ChatMemberUpdated) error {
	chatID := common.ChatID{Value: update.Chat.ID}
	title := "Telegram chat"
	if update.Chat.Title != nil {
		title = *update.Chat.Title
	}
	settings := chat.Settings{
		ChatID: chatID, Title: title, Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, Active: true,
	}
	saved, err := h.Chats.Save(ctx, settings)
	if err != nil {
		return err
	}
	return h.sendOnboardingStep(ctx, saved, "onboarding.welcome", "settings:games", "settings.games")
}

// sendOnboardingStep posts one step: what just happened or what this is,
// and the one button that continues. Best effort — a greeting that fails
// to send must not fail the membership update that triggered it, or the
// chat would be left unrecorded and the bot would never work there.
func (h *UpdateHandler) sendOnboardingStep(ctx context.Context, settings chat.Settings, textKey, callback, buttonKey string) error {
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get(buttonKey, settings.Locale), callback)},
		{button(h.Texts.Get("menu.help", settings.Locale), "menu:help")},
	}}
	if err := h.respond(ctx, sendTarget(settings.ChatID, settings.DefaultTopicID), h.Texts.Get(textKey, settings.Locale), kb); err != nil {
		loggerFrom(ctx, h.Log).Warn("onboarding message failed", "chatId", settings.ChatID.Value, "step", textKey, "error", err)
	}
	return nil
}

// onboardingNeedsTournament reports whether this chat has just finished
// picking its first game and has nothing to follow yet — the moment to
// point at the tournament list rather than leave the room waiting for
// polls that cannot come.
func (h *UpdateHandler) onboardingNeedsTournament(ctx context.Context, settings chat.Settings) bool {
	if len(settings.EnabledGames) != 1 {
		return false
	}
	subs, err := h.Subscriptions.Subscriptions(ctx, settings.ChatID)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("subscription lookup failed, skipping the onboarding nudge",
			"chatId", settings.ChatID.Value, "error", err)
		return false
	}
	return len(subs) == 0
}
