package telegram

import (
	"context"

	"cs2predictor/internal/platform/common"
)

// groupCommands/privateCommands are the "/" command hints Telegram shows
// while typing — see RegisterCommands. Kept as plain functions of locale
// rather than i18n bundle keys: Telegram's command list has its own
// constraints (plain text, ≤256 chars per description, no HTML) distinct
// from the chat message bundles, and changing one must never accidentally
// change the other.
func groupCommands(locale common.LocaleCode) []BotCommand {
	if locale == common.LocaleEN {
		return []BotCommand{
			{Command: "menu", Description: "Open the bot menu"},
			{Command: "stats", Description: "This group's leaderboard"},
			{Command: "events", Description: "Add or search for a tournament"},
			{Command: "timezone", Description: "Change the group's timezone"},
			{Command: "topic", Description: "Bind this forum topic to predictions"},
			{Command: "topics", Description: "Manage tournament topic bindings"},
			{Command: "moderator", Description: "Appoint/remove a moderator (reply to them)"},
			{Command: "help", Description: "Command reference"},
		}
	}
	return []BotCommand{
		{Command: "menu", Description: "Открыть меню бота"},
		{Command: "stats", Description: "Таблица лидеров этой группы"},
		{Command: "events", Description: "Добавить или найти турнир"},
		{Command: "timezone", Description: "Сменить часовой пояс группы"},
		{Command: "topic", Description: "Привязать эту тему форума к прогнозам"},
		{Command: "topics", Description: "Управление привязками тем турниров"},
		{Command: "moderator", Description: "Назначить/снять модератора (ответом на сообщение)"},
		{Command: "help", Description: "Справка по командам"},
	}
}

func privateCommands(locale common.LocaleCode) []BotCommand {
	if locale == common.LocaleEN {
		return []BotCommand{
			{Command: "start", Description: "Open your personal dashboard"},
			{Command: "menu", Description: "Personal statistics dashboard"},
			{Command: "stats", Description: "Personal statistics dashboard"},
			{Command: "bets", Description: "All your bets and their results"},
			{Command: "events", Description: "Search tournaments for the open group panel"},
			{Command: "timezone", Description: "Change the open group's timezone"},
			{Command: "team_matches", Description: "Review pending team-identity matches"},
			{Command: "team_match_admin", Description: "Manage team-match reviewers (root admins only)"},
			{Command: "provider_status", Description: "Data-source health status (root admins only)"},
			{Command: "help", Description: "Command reference"},
		}
	}
	return []BotCommand{
		{Command: "start", Description: "Открыть личный кабинет статистики"},
		{Command: "menu", Description: "Личный кабинет статистики"},
		{Command: "stats", Description: "Личный кабинет статистики"},
		{Command: "bets", Description: "Все ваши ставки и их результаты"},
		{Command: "events", Description: "Поиск турниров для открытой панели группы"},
		{Command: "timezone", Description: "Сменить часовой пояс открытой группы"},
		{Command: "team_matches", Description: "Проверка сопоставлений команд с рейтингами"},
		{Command: "team_match_admin", Description: "Управление операторами сопоставления (только для главных админов)"},
		{Command: "provider_status", Description: "Статус источников данных (только для главных админов)"},
		{Command: "help", Description: "Справка по командам"},
	}
}

// RegisterCommands pushes the "/" command hints to Telegram for both chat
// kinds (group/private) and both client languages this bot understands.
// The empty language_code call is the fallback Telegram shows to a client
// in any language with no explicit entry — set to Russian, this project's
// primary locale (see README) — and is deliberately overwritten by neither
// a group's nor a user's own in-bot locale choice: Telegram only lets this
// vary by the *client's* language, which the bot doesn't control.
//
// Best-effort by design: called once at startup, a failure here means
// people briefly miss the autocomplete hints, not that the bot can't serve
// requests — callers should log a failure rather than treat it as fatal.
func RegisterCommands(ctx context.Context, client *Client) error {
	scopes := []struct {
		scope    botCommandScope
		commands func(common.LocaleCode) []BotCommand
	}{
		{scopeAllGroupChats, groupCommands},
		{scopeAllPrivateChats, privateCommands},
	}
	languages := []struct {
		code   string
		locale common.LocaleCode
	}{
		{"", common.LocaleRU},
		{"ru", common.LocaleRU},
		{"en", common.LocaleEN},
	}
	for _, s := range scopes {
		for _, lang := range languages {
			if err := client.SetMyCommands(ctx, s.commands(lang.locale), s.scope, lang.code); err != nil {
				return err
			}
		}
	}
	return nil
}
