package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

var (
	_ common.OutboxPublisher = (*EventFinishedPublisher)(nil)
	_ common.OutboxPublisher = (*MatchResultPublisher)(nil)
	_ common.OutboxPublisher = (*UnsubscribeConfirmationPublisher)(nil)
	_ common.OutboxPublisher = (*MonthlyDigestPublisher)(nil)
	_ common.OutboxPublisher = (*AnnualDigestPublisher)(nil)
	_ common.OutboxPublisher = (*BigEventPublisher)(nil)
	_ common.OutboxPublisher = (*TeamMatchAskPublisher)(nil)
	_ prediction.Gateway     = (*PollGateway)(nil)
	_ chat.MembershipGateway = (*MembershipAdapter)(nil)
)

func digestRows(rows []common.DigestStanding) string {
	var lines []string
	for _, s := range rows {
		lines = append(lines, fmt.Sprintf("%s %s — ⭐ <b>%d</b> · 🎯 <b>%d%%</b>",
			medalFor(s.Rank), code(escapeHTML(truncate(s.DisplayName, 26))), s.Points, s.Accuracy))
	}
	return strings.Join(lines, "\n")
}

func annualHighlightsText(texts *Texts, locale common.LocaleCode, year int, h common.AnnualHighlightsNotification) string {
	var items []string
	if h.Comeback != nil {
		items = append(items, texts.Get("digest.comeback", locale,
			code(escapeHTML(truncate(h.Comeback.DisplayName, 28))), h.Comeback.StartRank, h.Comeback.FinalRank))
	}
	if h.Sniper != nil {
		items = append(items, texts.Get("digest.sniper", locale,
			code(escapeHTML(truncate(h.Sniper.DisplayName, 28))), h.Sniper.Accuracy, h.Sniper.Predictions))
	}
	if h.Expert != nil {
		items = append(items, texts.Get("digest.expert", locale,
			code(escapeHTML(truncate(h.Expert.DisplayName, 28))), h.Expert.Value))
	}
	if h.Exact != nil {
		items = append(items, texts.Get("digest.exact", locale,
			code(escapeHTML(truncate(h.Exact.DisplayName, 28))), h.Exact.Value))
	}
	if h.Streak != nil {
		items = append(items, texts.Get("digest.streak", locale,
			code(escapeHTML(truncate(h.Streak.DisplayName, 28))), h.Streak.Value))
	}
	if h.TeamSynergy != nil {
		items = append(items, texts.Get("digest.team_synergy", locale,
			code(escapeHTML(truncate(h.TeamSynergy.DisplayName, 24))),
			bold(escapeHTML(truncate(h.TeamSynergy.TeamName, 24))),
			h.TeamSynergy.Correct, h.TeamSynergy.Predictions, h.TeamSynergy.Accuracy))
	}
	if len(items) == 0 {
		return ""
	}
	return texts.Get("digest.highlights", locale, year) + "\n\n" + strings.Join(items, "\n\n")
}

func sendDigest(ctx context.Context, client *Client, payload map[string]any) error {
	_, err := client.Call(ctx, "sendMessage", payload)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsTopicUnavailable() {
		delete(payload, "message_thread_id")
		_, err = client.Call(ctx, "sendMessage", payload)
	}
	return err
}

type MonthlyDigestPublisher struct {
	client *Client
	chats  chat.Repository
	texts  *Texts
}

func NewMonthlyDigestPublisher(client *Client, chats chat.Repository, texts *Texts) *MonthlyDigestPublisher {
	return &MonthlyDigestPublisher{client: client, chats: chats, texts: texts}
}

func (p *MonthlyDigestPublisher) Supports(eventType string) bool {
	return eventType == "telegram.monthly-digest"
}

func (p *MonthlyDigestPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.MonthlyDigestNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := resolveLocale(ctx, p.chats, common.ChatID{Value: n.ChatID})
	month := time.Month(n.Month)
	monthName := shortMonthName(month, locale)

	parts := []string{
		p.texts.Get("digest.monthly_title", locale, escapeHTML(monthName), n.Year),
		p.texts.Get("digest.month_top", locale),
		digestRows(n.MonthStandings),
	}
	if len(n.YearStandings) > 0 {
		parts = append(parts, p.texts.Get("digest.year_progress", locale, n.Year), digestRows(n.YearStandings))
	}
	text := strings.Join(parts, "\n\n")
	payload := map[string]any{
		"chat_id": n.ChatID, "text": text, "parse_mode": "HTML",
		"reply_markup": InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			button(p.texts.Get("digest.full_month", locale), fmt.Sprintf("stats:month:%04d-%02d", n.Year, n.Month)),
			button(p.texts.Get("digest.full_year", locale, n.Year), fmt.Sprintf("stats:year:%d", n.Year)),
		}}},
	}
	if n.TopicID != nil {
		payload["message_thread_id"] = *n.TopicID
	}
	return sendDigest(ctx, p.client, payload)
}

type AnnualDigestPublisher struct {
	client *Client
	chats  chat.Repository
	texts  *Texts
}

func NewAnnualDigestPublisher(client *Client, chats chat.Repository, texts *Texts) *AnnualDigestPublisher {
	return &AnnualDigestPublisher{client: client, chats: chats, texts: texts}
}

func (p *AnnualDigestPublisher) Supports(eventType string) bool {
	return eventType == "telegram.annual-digest"
}

func (p *AnnualDigestPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.AnnualDigestNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := resolveLocale(ctx, p.chats, common.ChatID{Value: n.ChatID})
	parts := []string{p.texts.Get("digest.annual_title", locale, n.Year)}
	if len(n.DecemberStandings) > 0 {
		parts = append(parts, p.texts.Get("digest.december_top", locale), digestRows(n.DecemberStandings))
	}
	parts = append(parts,
		p.texts.Get("digest.year_top", locale), digestRows(n.YearStandings),
		p.texts.Get("digest.year_summary", locale, n.Participants, n.Predictions),
	)
	if highlights := annualHighlightsText(p.texts, locale, n.Year, n.Highlights); highlights != "" {
		parts = append(parts, highlights)
	}
	text := strings.Join(parts, "\n\n")
	payload := map[string]any{
		"chat_id": n.ChatID, "text": text, "parse_mode": "HTML",
		"reply_markup": InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			button(p.texts.Get("digest.full_month", locale), fmt.Sprintf("stats:month:%04d-12", n.Year)),
			button(p.texts.Get("digest.full_year", locale, n.Year), fmt.Sprintf("stats:year:%d", n.Year)),
		}}},
	}
	if n.TopicID != nil {
		payload["message_thread_id"] = *n.TopicID
	}
	return sendDigest(ctx, p.client, payload)
}

func medalFor(rank int) string {
	switch rank {
	case 1:
		return "🥇"
	case 2:
		return "🥈"
	case 3:
		return "🥉"
	default:
		return "  "
	}
}

func resolveLocale(ctx context.Context, chats chat.Repository, chatID common.ChatID) common.LocaleCode {
	settings, err := chats.Find(ctx, chatID)
	if err != nil || settings == nil {
		return common.LocaleRU
	}
	return settings.Locale
}

// truncate cuts s to at most n runes (not bytes): a byte-based s[:n] can
// split a multi-byte UTF-8 character in half — a real risk here since RU
// (Cyrillic, 2 bytes/rune) is this bot's default locale, not an edge case.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n])
	}
	return s
}

// EventFinishedPublisher publishes the "telegram.event-finished" outbox
// event type.
type EventFinishedPublisher struct {
	client *Client
	chats  chat.Repository
	texts  *Texts
}

func NewEventFinishedPublisher(client *Client, chats chat.Repository, texts *Texts) *EventFinishedPublisher {
	return &EventFinishedPublisher{client: client, chats: chats, texts: texts}
}

func (p *EventFinishedPublisher) Supports(eventType string) bool {
	return eventType == "telegram.event-finished"
}

func (p *EventFinishedPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.EventFinishedNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := resolveLocale(ctx, p.chats, common.ChatID{Value: n.ChatID})

	var lines []string
	for _, s := range n.Standings {
		if s.Rank > 3 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s — %s", medalFor(s.Rank), code(escapeHTML(truncate(s.DisplayName, 28))), bold(strconv.Itoa(s.Points))))
	}
	podium := p.texts.Get("stats.empty", locale)
	if len(lines) > 0 {
		podium = strings.Join(lines, "\n")
	}

	text := fmt.Sprintf("%s\n\n%s\n\n%s",
		p.texts.Get("event.finished", locale, escapeHTML(n.EventName)), podium, p.texts.Get("event.congratulations", locale))

	msgPayload := map[string]any{"chat_id": n.ChatID, "text": text, "parse_mode": "HTML"}
	if n.TopicID != nil {
		msgPayload["message_thread_id"] = *n.TopicID
	}

	_, err := p.client.Call(ctx, "sendMessage", msgPayload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && n.TopicID != nil && apiErr.IsTopicUnavailable() {
			delete(msgPayload, "message_thread_id")
			_, err = p.client.Call(ctx, "sendMessage", msgPayload)
		}
	}
	return err
}

// MatchResultPublisher publishes the "telegram.match-result" outbox event
// type.
type MatchResultPublisher struct {
	client *Client
	chats  chat.Repository
	texts  *Texts
	// botUsername mirrors UpdateHandler.BotUsername — resolved once at
	// startup via getMe, used to hand the personal "my rank" view off to a
	// DM instead of posting it in the group. Empty falls back to the old
	// in-group callback button.
	botUsername string
}

func NewMatchResultPublisher(client *Client, chats chat.Repository, texts *Texts, botUsername string) *MatchResultPublisher {
	return &MatchResultPublisher{client: client, chats: chats, texts: texts, botUsername: botUsername}
}

func (p *MatchResultPublisher) Supports(eventType string) bool {
	return eventType == "telegram.match-result"
}

func movementIndicator(rank int, previousRank *int, texts *Texts, locale common.LocaleCode) string {
	if previousRank == nil {
		return texts.Get("result.new", locale)
	}
	switch {
	case rank < *previousRank:
		return fmt.Sprintf("↑%d", *previousRank-rank)
	case rank > *previousRank:
		return fmt.Sprintf("↓%d", rank-*previousRank)
	default:
		return "•"
	}
}

func (p *MatchResultPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.MatchResultNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := resolveLocale(ctx, p.chats, common.ChatID{Value: n.ChatID})

	var lines []string
	for _, s := range n.Standings {
		movement := movementIndicator(s.Rank, s.PreviousRank, p.texts, locale)
		lines = append(lines, fmt.Sprintf("%s %s %s — %s",
			medalFor(s.Rank), code(escapeHTML(truncate(s.DisplayName, 24))), italic(movement), bold(fmt.Sprintf("%d (+%d)", s.Points, s.PointsDelta))))
	}
	standingsText := strings.Join(lines, "\n")

	title := fmt.Sprintf("%s %s %s", bold(escapeHTML(n.FirstTeam)), code(escapeHTML(n.Score)), bold(escapeHTML(n.SecondTeam)))
	var contextParts []string
	contextParts = append(contextParts, escapeHTML(n.EventName))
	if n.Stage != nil && *n.Stage != "" {
		contextParts = append(contextParts, escapeHTML(*n.Stage))
	}
	contextParts = append(contextParts, escapeHTML(n.Format))
	contextLine := italic(strings.Join(contextParts, " · "))

	text := fmt.Sprintf("%s\n\n%s\n%s\n\n%s\n%s",
		p.texts.Get("result.finished", locale), title, contextLine, p.texts.Get("result.leaderboard", locale), standingsText)

	msgPayload := map[string]any{"chat_id": n.ChatID, "text": text, "parse_mode": "HTML"}
	if n.TopicID != nil {
		msgPayload["message_thread_id"] = *n.TopicID
	}
	if n.ReplyToMessageID != nil {
		msgPayload["reply_parameters"] = map[string]any{"message_id": *n.ReplyToMessageID, "allow_sending_without_reply": true}
	}
	// "My rank" is personal, not group, information — it hands off to a DM
	// (matching every other personal-stats view) instead of posting it into
	// the group, unless the bot's own username is unresolved, in which case
	// the old in-group callback button is the only way to still offer it.
	myStatsButton := button(p.texts.Get("stats.mine", locale), "stats:notif:mine:"+n.EventID)
	if p.botUsername != "" {
		token := strconv.FormatInt(n.ChatID, 36) + ":" + strings.ReplaceAll(n.EventID, "-", "")
		myStatsButton = urlButton(p.texts.Get("stats.mine", locale), fmt.Sprintf("https://t.me/%s?start=pstats_%s", p.botUsername, token))
	}
	msgPayload["reply_markup"] = InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		// stats:notif:* rather than the menu's own stats:event:/stats:mine:
		// prefixes: this button lives on a match-result notification, which
		// routeCallback must never edit in place — doing so would silently
		// overwrite that historical result with a leaderboard. See
		// routeCallback's stats:notif: cases.
		button(p.texts.Get("stats.all", locale), "stats:notif:event:"+n.EventID),
		myStatsButton,
	}}}

	_, err := p.client.Call(ctx, "sendMessage", msgPayload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.IsTopicUnavailable() || apiErr.IsReplyUnavailable()) {
			if apiErr.IsTopicUnavailable() {
				delete(msgPayload, "message_thread_id")
			}
			delete(msgPayload, "reply_parameters") // unconditionally stripped on any retry
			_, err = p.client.Call(ctx, "sendMessage", msgPayload)
		}
	}
	return err
}

// BigEventPublisher publishes the "telegram.big-event-discovered" outbox
// event type: a proactive suggestion, sent to a chat not yet subscribed to
// a newly discovered S/A tier tournament, with a one-tap subscribe button
// (reusing the same "subscribe:<eventId>" flow /events search results use).
type BigEventPublisher struct {
	client *Client
	chats  chat.Repository
	texts  *Texts
}

func NewBigEventPublisher(client *Client, chats chat.Repository, texts *Texts) *BigEventPublisher {
	return &BigEventPublisher{client: client, chats: chats, texts: texts}
}

func (p *BigEventPublisher) Supports(eventType string) bool {
	return eventType == "telegram.big-event-discovered"
}

func (p *BigEventPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.BigEventDiscoveredNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := resolveLocale(ctx, p.chats, common.ChatID{Value: n.ChatID})

	badge := competition.EventTier(n.Tier).Badge()
	text := p.texts.Get("bigevent.announcement", locale, badge+escapeHTML(n.EventName))
	payload := map[string]any{
		"chat_id": n.ChatID, "text": text, "parse_mode": "HTML",
		"reply_markup": InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			button(p.texts.Get("bigevent.subscribe", locale), "subscribe:"+n.EventID),
		}}},
	}
	if n.TopicID != nil {
		payload["message_thread_id"] = *n.TopicID
	}
	return sendDigest(ctx, p.client, payload)
}

// UnsubscribeConfirmationPublisher delivers one manager's copy of an
// unsubscribe confirmation request (the "telegram.unsubscribe-confirmation"
// outbox event type).
//
// Telegram refuses a bot's first message to a user who has never started a
// chat with it. That refusal is expected rather than exceptional here, so
// it is not an error to retry: the recipient is marked unreachable — which
// keeps future requests from counting on them — and the message is dropped.
type UnsubscribeConfirmationPublisher struct {
	client  *Client
	chats   chat.Repository
	texts   *Texts
	metrics AdminMetrics
}

func NewUnsubscribeConfirmationPublisher(client *Client, chats chat.Repository, texts *Texts, metrics AdminMetrics) *UnsubscribeConfirmationPublisher {
	return &UnsubscribeConfirmationPublisher{client: client, chats: chats, texts: texts, metrics: metrics}
}

func (p *UnsubscribeConfirmationPublisher) record(result string) {
	if p.metrics != nil {
		p.metrics.RecordDMDelivery(result)
	}
}

func (p *UnsubscribeConfirmationPublisher) Supports(eventType string) bool {
	return eventType == "telegram.unsubscribe-confirmation"
}

func (p *UnsubscribeConfirmationPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.UnsubscribeConfirmationNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := common.LocaleFrom(n.Locale)

	text := p.texts.Get("events.unsubscribe_fanout", locale,
		bold(escapeHTML(n.ChatTitle)), bold(escapeHTML(n.EventName)))
	if n.Requester != "" {
		text += "\n" + italic(escapeHTML(n.Requester))
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		button(p.texts.Get("events.unsubscribe_confirm", locale), "unsubok:"+n.RequestID),
		button(p.texts.Get("events.unsubscribe_reject", locale), "unsubreject:"+n.RequestID),
	}}}

	_, err := p.client.Call(ctx, "sendMessage", map[string]any{
		"chat_id": n.UserID, "text": text, "parse_mode": "HTML", "reply_markup": kb,
	})
	if err == nil {
		p.record("delivered")
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsUnreachableUser() {
		p.record("unreachable")
		if markErr := p.chats.SetDMReachable(ctx, common.UserID{Value: n.UserID}, false); markErr != nil {
			return markErr
		}
		return nil // delivered as far as it ever can be; retrying won't help
	}
	p.record("error")
	return err
}

// personalNotePublisher is the shared delivery half of the two opt-in
// private nudges (result recaps, poll reminders). Both compose a plain
// text message for one user's DM and both have to handle the same
// outcome: Telegram refusing to deliver, which means "stop counting on
// this person" rather than "retry forever". Only the composition differs,
// so that is all the two types below supply.
type personalNotePublisher struct {
	eventType string
	client    *Client
	chats     chat.Repository
	texts     *Texts
	metrics   AdminMetrics
	// compose renders the message and names its recipient.
	compose func(payload string) (userID int64, text string, err error)
}

func (p *personalNotePublisher) Supports(eventType string) bool { return eventType == p.eventType }

func (p *personalNotePublisher) record(result string) {
	if p.metrics != nil {
		p.metrics.RecordDMDelivery(result)
	}
}

func (p *personalNotePublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	userID, text, err := p.compose(message.Payload)
	if err != nil {
		return err
	}
	_, err = p.client.Call(ctx, "sendMessage", map[string]any{
		"chat_id": userID, "text": text, "parse_mode": "HTML",
	})
	if err == nil {
		p.record("delivered")
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsUnreachableUser() {
		p.record("unreachable")
		// The person blocked the bot or never started it. Recording that
		// stops every future nudge from costing a failed send; their opt-in
		// is deliberately left alone, so it still stands if they come back.
		if markErr := p.chats.SetDMReachable(ctx, common.UserID{Value: userID}, false); markErr != nil {
			return markErr
		}
		return nil
	}
	p.record("error")
	return err
}

// NewResultRecapPublisher delivers the opt-in "here's how you did" DM
// after a match a person predicted is settled.
func NewResultRecapPublisher(client *Client, chats chat.Repository, texts *Texts, metrics AdminMetrics) common.OutboxPublisher {
	return &personalNotePublisher{
		eventType: "telegram.result-recap", client: client, chats: chats, texts: texts, metrics: metrics,
		compose: func(payload string) (int64, string, error) {
			var n common.ResultRecapNotification
			if err := json.Unmarshal([]byte(payload), &n); err != nil {
				return 0, "", err
			}
			locale := common.LocaleFrom(n.Locale)
			// The scoreline is the point of the message, so it leads; the
			// verdict line reads the same whether they scored or not, which
			// is deliberate — a recap that only arrives on a win is
			// flattery, not a record.
			text := bold(escapeHTML(n.EventName)) + "\n" +
				escapeHTML(n.FirstTeam) + " " + bold(escapeHTML(n.Score)) + " " + escapeHTML(n.SecondTeam)
			if n.ChatTitle != "" {
				text += "\n" + italic(escapeHTML(n.ChatTitle))
			}
			text += "\n\n" + texts.Get("recap.your_pick", locale, bold(escapeHTML(n.Predicted)))
			if n.Points > 0 {
				text += "\n" + texts.Get("recap.scored", locale, n.Points)
			} else {
				text += "\n" + texts.Get("recap.no_points", locale)
			}
			return n.UserID, text, nil
		},
	}
}

// NewPollReminderPublisher delivers the opt-in "you haven't voted yet" DM
// shortly before a poll closes.
func NewPollReminderPublisher(client *Client, chats chat.Repository, texts *Texts, metrics AdminMetrics) common.OutboxPublisher {
	return &personalNotePublisher{
		eventType: "telegram.poll-reminder", client: client, chats: chats, texts: texts, metrics: metrics,
		compose: func(payload string) (int64, string, error) {
			var n common.PollReminderNotification
			if err := json.Unmarshal([]byte(payload), &n); err != nil {
				return 0, "", err
			}
			locale := common.LocaleFrom(n.Locale)
			text := texts.Get("reminder.title", locale, n.MinutesLeft) + "\n" +
				bold(escapeHTML(n.FirstTeam)) + " — " + bold(escapeHTML(n.SecondTeam))
			if n.ChatTitle != "" {
				text += "\n" + italic(escapeHTML(n.ChatTitle))
			}
			text += "\n\n" + texts.Get("reminder.hint", locale)
			return n.UserID, text, nil
		},
	}
}

// NewTeamMatchOperatorPingPublisher delivers the one-line "a new team-match
// request needs review" ping to a configured operator (see
// app.TeamMatchService.notifyOperators) — plain text, no keyboard, since
// the operator acts via /team_matches rather than a button on the ping
// itself.
func NewTeamMatchOperatorPingPublisher(client *Client, chats chat.Repository, texts *Texts, metrics AdminMetrics) common.OutboxPublisher {
	return &personalNotePublisher{
		eventType: "telegram.team-match-operator-ping", client: client, chats: chats, texts: texts, metrics: metrics,
		compose: func(payload string) (int64, string, error) {
			var n common.TeamMatchOperatorPingNotification
			if err := json.Unmarshal([]byte(payload), &n); err != nil {
				return 0, "", err
			}
			// Root operators are an ops concern, not a per-person
			// preference — RU (this project's primary locale) rather than
			// a per-chat lookup this compose closure has no ctx for.
			text := texts.Get("teammatch.operator_ping", common.LocaleRU, escapeHTML(n.ExternalName))
			return n.ChatID, text, nil
		},
	}
}

// TeamMatchAskPublisher delivers one crowd-review question to one helper's
// DM (the "telegram.team-match-ask" outbox event type) — see
// app.TeamMatchService.AskChatHelpers for who gets asked and how often.
// Unlike the plain personalNotePublisher deliveries above, this carries an
// inline keyboard (yes/no), so it needs its own Publish rather than reusing
// that shared type.
type TeamMatchAskPublisher struct {
	client  *Client
	chats   chat.Repository
	texts   *Texts
	metrics AdminMetrics
}

func NewTeamMatchAskPublisher(client *Client, chats chat.Repository, texts *Texts, metrics AdminMetrics) *TeamMatchAskPublisher {
	return &TeamMatchAskPublisher{client: client, chats: chats, texts: texts, metrics: metrics}
}

func (p *TeamMatchAskPublisher) Supports(eventType string) bool {
	return eventType == "telegram.team-match-ask"
}

func (p *TeamMatchAskPublisher) record(result string) {
	if p.metrics != nil {
		p.metrics.RecordDMDelivery(result)
	}
}

func (p *TeamMatchAskPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	var n common.TeamMatchAskNotification
	if err := json.Unmarshal([]byte(message.Payload), &n); err != nil {
		return err
	}
	locale := common.LocaleFrom(n.Locale)
	// teammatch.ask_question already wraps both {0} and {1} in <b>...</b>
	// itself — bolding CandidateName here too would double-nest the tag.
	text := p.texts.Get("teammatch.ask_question", locale, escapeHTML(n.ExternalName), escapeHTML(n.CandidateName))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		button(p.texts.Get("teammatch.ask_yes", locale), "tmatch:ans:"+n.RequestID+":yes"),
		button(p.texts.Get("teammatch.ask_no", locale), "tmatch:ans:"+n.RequestID+":no"),
	}}}

	_, err := p.client.Call(ctx, "sendMessage", map[string]any{
		"chat_id": n.UserID, "text": text, "parse_mode": "HTML", "reply_markup": kb,
	})
	if err == nil {
		p.record("delivered")
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsUnreachableUser() {
		p.record("unreachable")
		if markErr := p.chats.SetDMReachable(ctx, common.UserID{Value: n.UserID}, false); markErr != nil {
			return markErr
		}
		return nil
	}
	p.record("error")
	return err
}
