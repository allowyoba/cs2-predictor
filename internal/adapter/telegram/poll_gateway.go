package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// PollEnrichmentSources bundles the optional (nil-safe) enrichment caches
// Send may read from when building a poll's insight lines. Every field
// may be nil independently, e.g. a provider that isn't configured. Send
// treats a nil source, a lookup error, or simply no cached data for a
// team/pair the same way: omit that line. All reads are cache-only; Send
// never calls an external provider directly, only a background sync job
// does.
type PollEnrichmentSources struct {
	Rankings enrichment.RankingRepository
	Form     enrichment.FormRepository
	H2H      enrichment.HeadToHeadRepository
}

// PollGateway implements prediction.Gateway with an automatic fallback:
// if sendPoll fails because the target topic no longer exists, it clears
// the persisted topic override and retries once without message_thread_id.
type PollGateway struct {
	client     *Client
	catalog    competition.Catalog
	chats      chat.Repository
	texts      *Texts
	log        *slog.Logger
	enrichment PollEnrichmentSources

	// StreamRecorder is optional; see PollStreamRecorder.
	StreamRecorder PollStreamRecorder
}

func NewPollGateway(client *Client, catalog competition.Catalog, chats chat.Repository, texts *Texts, log *slog.Logger, enrichmentSources PollEnrichmentSources) *PollGateway {
	return &PollGateway{client: client, catalog: catalog, chats: chats, texts: texts, log: log, enrichment: enrichmentSources}
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (g *PollGateway) Send(ctx context.Context, poll prediction.Poll) (prediction.SentPoll, error) {
	match, err := g.catalog.FindMatch(ctx, poll.MatchID)
	if err != nil {
		return prediction.SentPoll{}, err
	}
	if match == nil {
		return prediction.SentPoll{}, fmt.Errorf("%w: %s", competition.ErrMatchNotFound, poll.MatchID.Value)
	}

	// Event, chat settings, match facts, and (when configured) team
	// rankings are independent reads (none needs another's result) —
	// fetched concurrently instead of as sequential round trips on this
	// poll-send hot path.
	var event *competition.Event
	var settings *chat.Settings
	var facts competition.MatchFacts
	var haveFacts bool
	var rankings, hltvRankings map[common.TeamID]enrichment.TeamRanking
	var homeForm, awayForm *enrichment.RecentForm
	var h2h *enrichment.HeadToHead
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		e, err := g.catalog.FindEvent(gctx, match.EventID)
		event = e
		return err
	})
	group.Go(func() error {
		s, err := g.chats.Find(gctx, poll.ChatID)
		settings = s
		return err
	})
	if factsCatalog, ok := g.catalog.(competition.MatchFactsCatalog); ok {
		group.Go(func() error {
			f, factsErr := factsCatalog.MatchFacts(gctx, match)
			if factsErr != nil {
				// Non-fatal: the poll still goes out without the balance
				// line, but a persistently failing MatchFacts query would
				// otherwise silently and permanently drop this enrichment
				// with no trace in the logs to explain why.
				g.log.Warn("match facts lookup failed, sending poll without balance line", "matchId", match.ID.Value, "error", factsErr)
				return nil
			}
			facts, haveFacts = f, true
			return nil
		})
	}
	if g.enrichment.Rankings != nil && match.FirstTeam != nil && match.SecondTeam != nil {
		group.Go(func() error {
			r, rankErr := g.enrichment.Rankings.FindRankings(gctx, []common.TeamID{match.FirstTeam.ID, match.SecondTeam.ID}, enrichment.SourceValveVRS)
			if rankErr != nil {
				g.log.Warn("team ranking lookup failed, sending poll without VRS line", "matchId", match.ID.Value, "error", rankErr)
				return nil
			}
			rankings = r
			return nil
		})
		group.Go(func() error {
			r, rankErr := g.enrichment.Rankings.FindRankings(gctx, []common.TeamID{match.FirstTeam.ID, match.SecondTeam.ID}, enrichment.SourceHLTV)
			if rankErr != nil {
				g.log.Warn("team ranking lookup failed, sending poll without HLTV line", "matchId", match.ID.Value, "error", rankErr)
				return nil
			}
			hltvRankings = r
			return nil
		})
	}
	if g.enrichment.Form != nil && match.FirstTeam != nil && match.SecondTeam != nil {
		group.Go(func() error {
			f, formErr := g.enrichment.Form.FindForm(gctx, match.FirstTeam.ID, enrichment.SourceGRID)
			if formErr != nil {
				g.log.Warn("team form lookup failed, sending poll without form line", "matchId", match.ID.Value, "team", match.FirstTeam.ID.Value, "error", formErr)
				return nil
			}
			homeForm = f
			return nil
		})
		group.Go(func() error {
			f, formErr := g.enrichment.Form.FindForm(gctx, match.SecondTeam.ID, enrichment.SourceGRID)
			if formErr != nil {
				g.log.Warn("team form lookup failed, sending poll without form line", "matchId", match.ID.Value, "team", match.SecondTeam.ID.Value, "error", formErr)
				return nil
			}
			awayForm = f
			return nil
		})
	}
	if g.enrichment.H2H != nil && match.FirstTeam != nil && match.SecondTeam != nil {
		group.Go(func() error {
			h, h2hErr := g.enrichment.H2H.FindHeadToHead(gctx, match.FirstTeam.ID, match.SecondTeam.ID, enrichment.SourceGRID)
			if h2hErr != nil {
				g.log.Warn("head-to-head lookup failed, sending poll without H2H line", "matchId", match.ID.Value, "error", h2hErr)
				return nil
			}
			h2h = h
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return prediction.SentPoll{}, err
	}
	if event == nil {
		return prediction.SentPoll{}, fmt.Errorf("%w: %s", competition.ErrEventNotFound, match.EventID.Value)
	}

	// The rankings are fetched above without knowing the game, because the
	// event that carries it is loaded in the same batch of concurrent
	// reads. Dropping them here is what keeps a Counter-Strike ranking out
	// of a Dota 2 poll — the two rosters behind "BetBoom Team" share a
	// sponsor and nothing else. The sync no longer writes such a row at
	// all (see enrichment.RanksGame), and this makes sure the ones written
	// before it learned better are never shown either.
	if !enrichment.RanksGame(enrichment.SourceValveVRS, event.Game) {
		rankings = nil
	}
	if !enrichment.RanksGame(enrichment.SourceHLTV, event.Game) {
		hltvRankings = nil
	}

	locale, zoneName := common.LocaleRU, chat.DefaultTimezone
	preferHLTV := false
	if settings != nil {
		locale = settings.Locale
		zoneName = settings.Timezone
		preferHLTV = settings.PreferHLTVFlags
	}
	loc := chat.ZoneOrDefault(zoneName)

	// stage/eventName go into description, which (unlike question) does get
	// HTML parse_mode below, so they're escaped; team names and the format
	// label never appear in description text controlled by an external
	// source in a way that needs it.
	stage := ""
	if match.Stage != nil {
		stage = escapeHTML(*match.Stage)
	}
	var dateWhen, timeWhen string
	if match.ScheduledAt != nil {
		local := match.ScheduledAt.In(loc)
		dateWhen = local.Format("02.01")
		timeWhen = local.Format("15:04 MST")
	}
	firstName := teamNameWithFlag(match.FirstTeam, formatTeamCompact(match.FirstTeam), preferHLTV)
	secondName := teamNameWithFlag(match.SecondTeam, formatTeamCompact(match.SecondTeam), preferHLTV)

	// The question is just "Team (record) · Team (record)" — short, single
	// line, no parse_mode (question_parse_mode only honors custom-emoji
	// entities anyway, per Bot API 7.3), so team names stay unescaped plain
	// text. Everything else — tournament, stage/format, date/time, VRS,
	// form, H2H — goes in description instead: added in Bot API 9.6,
	// it's a real multi-line, HTML-formattable field (up to 1024 chars)
	// attached to the same sendPoll call, unlike question, which Telegram
	// renders every "\n" in as a single space with no way to opt out.
	var firstRecord, secondRecord string
	if haveFacts {
		firstRecord = formatTeamRecord(facts.FirstEventBalance)
		secondRecord = formatTeamRecord(facts.SecondEventBalance)
	}
	question := composePollQuestion(firstName, firstRecord, secondName, secondRecord)

	eventName := event.Tier.Badge() + escapeHTML(event.Name)
	// Named only where it is actually ambiguous: a chat that follows one
	// game knows what it is looking at, and a poll is dense enough already.
	// It rides on the tournament line rather than taking one of its own.
	if settings != nil && len(settings.EnabledGames) > 1 {
		eventName += " · " + g.texts.Get(gameShortLabelKey(event.Game), locale)
	}
	vrsLine := formatRankingCombined(g.texts, locale, rankings, match.FirstTeam, match.SecondTeam)
	hltvLine := formatRankingCombined(g.texts, locale, hltvRankings, match.FirstTeam, match.SecondTeam)
	var vrsUpdated, hltvUpdated string
	if match.FirstTeam != nil && match.SecondTeam != nil {
		vrsUpdated = rankingUpdatedLabel(rankings, match.FirstTeam.ID, match.SecondTeam.ID, loc)
		hltvUpdated = rankingUpdatedLabel(hltvRankings, match.FirstTeam.ID, match.SecondTeam.ID, loc)
	}
	formLine := formatForm(homeForm, awayForm)
	h2hLine := formatH2H(h2h)
	description := composePollDescription(g.texts, locale, eventName, stage, match.Format.Label(), dateWhen, timeWhen,
		vrsLine, vrsUpdated, hltvLine, hltvUpdated, formLine, h2hLine)

	options := make([]map[string]string, len(poll.Options))
	for i, o := range poll.Options {
		options[i] = map[string]string{"text": o.Score.String()}
	}

	payload := map[string]any{
		"chat_id": poll.ChatID.Value,
		// No question_parse_mode: Telegram's sendPoll only honors custom
		// emoji entities there (Bot API 7.3+) — nothing this bot would send
		// needs that, and leaving parse_mode unset avoids any risk of a
		// stray "<"/">" in a team name being interpreted as (invalid,
		// silently-dropped) markup instead of shown as-is.
		"question":                question,
		"options":                 options,
		"is_anonymous":            false,
		"allows_multiple_answers": false,
	}
	if description != "" {
		payload["description"] = truncate(description, telegramPollDescriptionLimit)
		payload["description_parse_mode"] = "HTML"
	}
	// close_date hands Telegram the poll's own scheduled close time (valid
	// up to ~30 days out, comfortably more than this bot's longest lead
	// time) so it closes on the dot even if this process is down or
	// delayed at that moment — a safety net alongside, not a replacement
	// for, the scheduled CloseDue job, which still has to run to record
	// the closure in this bot's own database and drive settlement. A lead
	// time under Telegram's 5-second minimum is skipped rather than
	// rejected outright by sendPoll; CloseDue's own stopPoll call still
	// closes it in that case.
	if lead := time.Until(poll.ClosesAt); lead >= minPollCloseDateLead {
		payload["close_date"] = poll.ClosesAt.Unix()
	}
	usedTopic := poll.TopicID
	if poll.TopicID != nil {
		payload["message_thread_id"] = *poll.TopicID
	}

	result, err := g.client.Call(ctx, "sendPoll", payload)
	if err != nil {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || poll.TopicID == nil || !apiErr.IsTopicUnavailable() {
			return prediction.SentPoll{}, err
		}
		if clearErr := g.clearTopic(ctx, poll.ChatID, event.ID, *poll.TopicID); clearErr != nil {
			return prediction.SentPoll{}, clearErr
		}
		usedTopic = nil
		delete(payload, "message_thread_id")
		result, err = g.client.Call(ctx, "sendPoll", payload)
		if err != nil {
			return prediction.SentPoll{}, err
		}
	}

	var sent struct {
		Poll struct {
			ID string `json:"id"`
		} `json:"poll"`
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(result, &sent); err != nil {
		return prediction.SentPoll{}, err
	}
	return prediction.SentPoll{PollID: sent.Poll.ID, MessageID: sent.MessageID, TopicID: usedTopic}, nil
}

// telegramPollQuestionLimit/telegramPollDescriptionLimit are sendPoll's
// documented caps: question is 1-300 characters with no real line-break
// support (every "\n" renders as a single space in every Telegram client
// checked); description — added in Bot API 9.6 — is 0-1024 characters,
// does honor real line breaks, and supports normal HTML/MarkdownV2
// formatting (unlike question_parse_mode, which is restricted to custom
// emoji entities only).
const (
	telegramPollQuestionLimit    = 300
	telegramPollDescriptionLimit = 1024

	// minPollCloseDateLead is a small safety margin over sendPoll's own
	// documented 5-second minimum for close_date, so a poll whose match is
	// starting almost immediately doesn't risk sendPoll rejecting the call
	// outright over a lead time that rounds down past the limit between
	// this check and the request actually reaching Telegram.
	minPollCloseDateLead = 10 * time.Second
)

// composePollQuestion renders the poll's single-line header: each team's
// name immediately followed by its own tournament win-loss record in
// parentheses, so the record visually belongs to that team rather than
// reading as an unattached aside. A team with no record yet (hasn't played
// here, or the data isn't cached) gets no parentheses at all — never an
// empty "()" or a placeholder like "(0–0)".
func composePollQuestion(firstName, firstRecord, secondName, secondRecord string) string {
	left := firstName
	if firstRecord != "" {
		left += " (" + firstRecord + ")"
	}
	right := secondName
	if secondRecord != "" {
		right += " (" + secondRecord + ")"
	}
	return truncate(left+" · "+right, telegramPollQuestionLimit)
}

// formatTeamRecord renders a team's tournament win-loss record with a
// typographic en dash ("2–1"), or "" if they haven't played a match here
// yet — composePollQuestion omits the parenthetical entirely rather than
// show a fake "(0–0)".
func formatTeamRecord(b competition.TeamBalance) string {
	if b.Wins+b.Losses+b.Draws == 0 {
		return ""
	}
	if b.Draws > 0 {
		return fmt.Sprintf("%d–%d–%d", b.Wins, b.Losses, b.Draws)
	}
	return fmt.Sprintf("%d–%d", b.Wins, b.Losses)
}

// composePollDescription builds the poll's multi-line context block: real
// "\n" between lines (description, unlike question, honors them), each
// logical block — tournament, stage/format, date/time, VRS, HLTV, recent
// form, head-to-head — on its own line, entirely omitted when it has
// nothing to show. No separator character joins these blocks; every value
// that IS present within one line joins with " · ".
func composePollDescription(texts *Texts, locale common.LocaleCode, eventName, stage, formatLabel, dateWhen, timeWhen,
	vrsLine, vrsUpdated, hltvLine, hltvUpdated, formLine, h2hLine string) string {
	var lines []string
	if eventName != "" {
		lines = append(lines, "🏆 "+eventName)
	}
	if stageFormat := joinNonEmpty(stage, formatLabel); stageFormat != "" {
		lines = append(lines, "🎯 "+stageFormat)
	}
	if when := joinNonEmpty(dateWhen, timeWhen); when != "" {
		lines = append(lines, "🕒 "+when)
	}
	if vrsLine != "" {
		lines = append(lines, texts.Get("poll.vrs", locale, vrsLine))
	}
	if hltvLine != "" {
		lines = append(lines, texts.Get("poll.hltv", locale, hltvLine))
	}
	if formLine != "" {
		lines = append(lines, texts.Get("poll.form", locale, formLine))
	}
	if h2hLine != "" {
		lines = append(lines, texts.Get("poll.h2h", locale, h2hLine))
	}
	// A single footer note rather than cluttering each ranking line with its
	// own "(30.08)" — one place to see how fresh the data above is, which
	// reads cleaner than repeating the same (usually identical) date twice.
	if updated := rankingUpdatedFooter(texts, locale, vrsUpdated, hltvUpdated); updated != "" {
		lines = append(lines, updated)
	}
	return strings.Join(lines, "\n")
}

// rankingUpdatedFooter renders one closing line reporting when the
// VRS/HLTV rankings shown above were last refreshed — "" when neither
// source had a cached ranking to date at all. The common case (both
// sources synced together) collapses to one date; if they genuinely
// differ, both are named so the line is never misleading about which
// ranking is which age.
func rankingUpdatedFooter(texts *Texts, locale common.LocaleCode, vrsUpdated, hltvUpdated string) string {
	switch {
	case vrsUpdated == "" && hltvUpdated == "":
		return ""
	// Only one source present, or both present but in sync: nothing to
	// disambiguate, so name no source and just state the date.
	case vrsUpdated == "" || hltvUpdated == "" || vrsUpdated == hltvUpdated:
		date := vrsUpdated
		if date == "" {
			date = hltvUpdated
		}
		return texts.Get("poll.rankings_updated", locale, date)
	default:
		return texts.Get("poll.rankings_updated", locale,
			texts.Get("poll.vrs_source", locale)+" "+vrsUpdated+" · "+texts.Get("poll.hltv_source", locale)+" "+hltvUpdated)
	}
}

// rankingUpdatedLabel renders whichever side has a cached ranking's
// PublishedAt as "DD.MM" in loc — the two sides sync together in practice,
// so either one answers "how fresh is this source's data" for the whole
// line; "" when neither side has one (formatRankingCombined already
// omitted the line in that case).
func rankingUpdatedLabel(rankings map[common.TeamID]enrichment.TeamRanking, first, second common.TeamID, loc *time.Location) string {
	if r, ok := rankings[first]; ok && !r.PublishedAt.IsZero() {
		return r.PublishedAt.In(loc).Format("02.01")
	}
	if r, ok := rankings[second]; ok && !r.PublishedAt.IsZero() {
		return r.PublishedAt.In(loc).Format("02.01")
	}
	return ""
}

// joinNonEmpty joins only the non-empty values in parts with " · " — "" if
// none are non-empty, one bare value if only one is, so a missing stage,
// format, date, time, or VRS/form side never leaves a stray leading/
// trailing separator behind.
func joinNonEmpty(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, " · ")
}

// rankingInts pulls one *int field (chosen by pick) out of a cached ranking
// for each of two teams, if present.
func rankingInts(rankings map[common.TeamID]enrichment.TeamRanking, first, second common.TeamID, pick func(enrichment.TeamRanking) *int) (*int, *int) {
	var firstVal, secondVal *int
	if r, ok := rankings[first]; ok {
		firstVal = pick(r)
	}
	if r, ok := rankings[second]; ok {
		secondVal = pick(r)
	}
	return firstVal, secondVal
}

// formatRankingCombined renders one ranking source's poll line halves (VRS,
// HLTV, ...) in the same first/second order as the question's teams, joined
// by " · " — "#8 (1723) · #121 (867)" — with team names never repeated (the
// question already names them, and the order alone makes which is which
// unambiguous). The whole line is "" when NEITHER side has any cached data
// for this source, so the caller omits it entirely; but once at least one
// side does, the other side shows "N/A" rather than being silently dropped —
// dropping it would otherwise leave a single bare value with no way to tell
// which team it belongs to. The caller passes a rankings map already scoped
// to one enrichment.Source (see PollGateway.Send), so this same function
// renders every source's line — no per-source variant needed.
func formatRankingCombined(texts *Texts, locale common.LocaleCode, rankings map[common.TeamID]enrichment.TeamRanking, first, second *competition.Team) string {
	if first == nil || second == nil {
		return ""
	}
	firstRank, secondRank := rankingInts(rankings, first.ID, second.ID, func(r enrichment.TeamRanking) *int { return r.GlobalRank })
	firstPoints, secondPoints := rankingInts(rankings, first.ID, second.ID, func(r enrichment.TeamRanking) *int { return r.Points })
	if firstRank == nil && firstPoints == nil && secondRank == nil && secondPoints == nil {
		return ""
	}
	return formatRankingSide(texts, locale, firstRank, firstPoints) + " · " + formatRankingSide(texts, locale, secondRank, secondPoints)
}

// formatRankingSide renders one team's half of formatRankingCombined: "#rank
// (points)" when both are cached, "#rank" or "(points)" alone when only one
// is, or the localized "poll.no_data" placeholder when neither is cached for
// this team at all — routed through texts.Get like every other user-facing
// string here rather than a hardcoded English literal.
func formatRankingSide(texts *Texts, locale common.LocaleCode, rank, points *int) string {
	switch {
	case rank != nil && points != nil:
		return fmt.Sprintf("#%d (%d)", *rank, *points)
	case rank != nil:
		return fmt.Sprintf("#%d", *rank)
	case points != nil:
		return fmt.Sprintf("(%d)", *points)
	default:
		return texts.Get("poll.no_data", locale)
	}
}

// formatForm renders each side's recent-form win-loss record ("4–1 · 3–2")
// for whichever side(s) actually have cached data — a side with none
// contributes nothing rather than a "—" placeholder. Returns "" when
// neither side has one, so the caller omits the whole line.
func formatForm(home, away *enrichment.RecentForm) string {
	var homeStr, awayStr string
	if home != nil {
		homeStr = fmt.Sprintf("%d–%d", home.Wins, home.Losses)
	}
	if away != nil {
		awayStr = fmt.Sprintf("%d–%d", away.Wins, away.Losses)
	}
	return joinNonEmpty(homeStr, awayStr)
}

// formatH2H renders "teamAWins–teamBWins", matching the match's own
// first/second order (HeadToHeadRepository.FindHeadToHead already
// remaps TeamAWins/TeamBWins to whichever order it was asked for). Returns
// "" when there's no cached record, or a zero-sample one (nothing played).
func formatH2H(h2h *enrichment.HeadToHead) string {
	if h2h == nil || h2h.Sample == 0 {
		return ""
	}
	return fmt.Sprintf("%d–%d", h2h.TeamAWins, h2h.TeamBWins)
}

// clearTopic clears whichever topic override actually matched what was
// used: the event-specific override first, else the chat's default topic.
func (g *PollGateway) clearTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID, usedTopic int64) error {
	eventTopic, err := g.chats.EventTopic(ctx, chatID, eventID)
	if err != nil {
		return err
	}
	if eventTopic != nil && *eventTopic == usedTopic {
		return g.chats.ClearEventTopic(ctx, chatID, eventID)
	}
	settings, err := g.chats.Find(ctx, chatID)
	if err != nil || settings == nil {
		return err
	}
	if settings.DefaultTopicID != nil && *settings.DefaultTopicID == usedTopic {
		settings.DefaultTopicID = nil
		_, err := g.chats.Save(ctx, *settings)
		return err
	}
	return nil
}

// PollStreamRecorder remembers which broadcast a chat has already been sent
// for a poll. Optional: with no recorder wired the announcement still goes
// out, it just cannot be proven a second attempt would not repeat it.
type PollStreamRecorder interface {
	MarkPollStreamAnnounced(ctx context.Context, pollID common.PollID, streamURL string) error
}

func (g *PollGateway) Close(ctx context.Context, poll prediction.Poll) error {
	if err := g.stop(ctx, poll); err != nil {
		return err
	}
	// Closing is the moment the match actually starts, which is both the
	// last point a broadcast can still appear and the first point a link is
	// of any use to the chat — a URL posted with the poll hours earlier is
	// dead weight until now. Failing to announce must never fail the
	// closure itself: the poll is closed, settlement depends on it, and a
	// missing link is a cosmetic loss.
	if err := g.announceStream(ctx, poll); err != nil {
		g.log.Warn("could not announce the match broadcast", "pollId", poll.ID.Value, "error", err)
	}
	return nil
}

// announceStream posts the match's broadcast link as a reply to the poll,
// unless the chat has already been given that exact link or the providers
// still list no official stream.
func (g *PollGateway) announceStream(ctx context.Context, poll prediction.Poll) error {
	if poll.TelegramMessageID == nil {
		return nil
	}
	match, err := g.catalog.FindMatch(ctx, poll.MatchID)
	if err != nil || match == nil {
		return err
	}
	settings, err := g.chats.Find(ctx, poll.ChatID)
	if err != nil {
		return err
	}
	// Opt-in, and off by default: this is an extra message in the room
	// every time a poll closes, which a busy chat feels. A chat that wants
	// it asks in the settings.
	if settings == nil {
		return nil
	}
	switches, err := switchboardOf(g.chats)
	if err != nil {
		return err
	}
	if on, err := switches.NotifyEnabled(ctx, common.ScopeChat, poll.ChatID.Value, string(common.ChatNotifyStreams)); err != nil || !on {
		return err
	}
	locale, preferred := settings.Locale, settings.StreamLocale()
	stream, ok := match.StreamFor(preferred)
	if !ok || stream.URL == poll.StreamURL {
		return nil
	}
	payload := map[string]any{
		"chat_id":    poll.ChatID.Value,
		"text":       g.texts.Get("poll.stream_appeared", locale, streamLine(g.texts, locale, preferred, stream)),
		"parse_mode": "HTML",
		// The link is the message; a preview card of a stream page would
		// double its height for nothing.
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_parameters":     map[string]any{"message_id": *poll.TelegramMessageID, "allow_sending_without_reply": true},
	}
	if poll.TopicID != nil {
		payload["message_thread_id"] = *poll.TopicID
	}
	if err := sendToChat(ctx, g.client, g.chats, payload); err != nil {
		return err
	}
	if g.StreamRecorder == nil {
		return nil
	}
	return g.StreamRecorder.MarkPollStreamAnnounced(ctx, poll.ID, stream.URL)
}
func (g *PollGateway) Cancel(ctx context.Context, poll prediction.Poll) error {
	return g.stop(ctx, poll)
}

func (g *PollGateway) stop(ctx context.Context, poll prediction.Poll) error {
	if poll.TelegramMessageID == nil {
		return prediction.ErrPollMissingTelegramMessage
	}
	_, err := g.client.Call(ctx, "stopPoll", map[string]any{"chat_id": poll.ChatID.Value, "message_id": *poll.TelegramMessageID})
	if err == nil {
		return nil
	}
	// Expected, not a failure, for a poll sent with close_date: Telegram's
	// own timer can beat this scheduled call to closing it.
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsPollAlreadyClosed() {
		return nil
	}
	return err
}
