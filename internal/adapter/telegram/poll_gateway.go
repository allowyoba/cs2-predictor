package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

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
	var rankings map[common.TeamID]enrichment.TeamRanking
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

	locale, zoneName := common.LocaleRU, chat.DefaultTimezone
	if settings != nil {
		locale = settings.Locale
		zoneName = settings.Timezone
	}
	loc := chat.ZoneOrDefault(zoneName)

	stage := "—"
	if match.Stage != nil {
		stage = escapeHTML(*match.Stage)
	}
	var when string
	if match.ScheduledAt != nil {
		when = match.ScheduledAt.In(loc).Format("02.01 15:04 MST")
	}
	firstName := escapeHTML(formatTeamCompact(match.FirstTeam))
	secondName := escapeHTML(formatTeamCompact(match.SecondTeam))
	eventName := event.Tier.Badge() + escapeHTML(event.Name)
	question := g.texts.Get("poll.question", locale, eventName, firstName, secondName, stage, escapeHTML(match.Format.Label()), when)

	// Balance and every enrichment line are short "insight" lines — grouped
	// into one paragraph (matching the spec's target layout) rather than
	// each getting its own blank-line-separated block. Balance is
	// PandaScore-native (always shown when available) and doesn't count
	// against the enrichment cap in buildEnrichmentLines.
	var insights []string
	if haveFacts {
		firstGames := facts.FirstEventBalance.Wins + facts.FirstEventBalance.Losses + facts.FirstEventBalance.Draws
		secondGames := facts.SecondEventBalance.Wins + facts.SecondEventBalance.Losses + facts.SecondEventBalance.Draws
		if firstGames > 0 || secondGames > 0 {
			insights = append(insights, g.texts.Get("poll.balance", locale, formatBalance(facts.FirstEventBalance), formatBalance(facts.SecondEventBalance)))
		}
	}
	insights = append(insights, g.buildEnrichmentLines(locale, rankings, homeForm, awayForm, h2h, match.FirstTeam, match.SecondTeam)...)
	question, droppedInsights := composePollQuestion(question, insights, event.Name, match.FirstTeam, match.SecondTeam)
	if droppedInsights > 0 {
		g.log.Info("poll question over budget, dropped lowest-priority insight lines",
			"matchId", match.ID.Value, "dropped", droppedInsights)
	}

	options := make([]map[string]string, len(poll.Options))
	for i, o := range poll.Options {
		options[i] = map[string]string{"text": o.Score.String()}
	}

	payload := map[string]any{
		"chat_id":                 poll.ChatID.Value,
		"question":                question,
		"question_parse_mode":     "HTML",
		"options":                 options,
		"is_anonymous":            false,
		"allows_multiple_answers": false,
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

const telegramPollQuestionLimit = 300

func pollHashtag(value string) string {
	const maxTagRunes = 40
	var b strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(value) {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		if count >= maxTagRunes {
			break
		}
		b.WriteRune(r)
		count++
	}
	if b.Len() == 0 {
		return ""
	}
	return "#" + b.String()
}

// composePollQuestion fits header + insight lines + hashtags inside
// Telegram's poll-question limit. Insight lines are added by priority while
// they fit and dropped whole from the bottom (VRS points, then H2H, then
// form — buildEnrichmentLines' own order) when they don't, rather than
// letting the hashtag pass truncate the text and cut a rank line in half.
// The header and hashtags always survive: they identify the match, which is
// the one thing the poll can't be read without.
func composePollQuestion(header string, insights []string, eventName string, first, second *competition.Team) (question string, dropped int) {
	for kept := len(insights); kept >= 0; kept-- {
		body := header
		if kept > 0 {
			// \n\n, not \n: Telegram's poll question collapses a lone
			// newline into a space when rendered, but keeps a blank-line
			// break — so every logical line needs one to stay legible.
			body += "\n\n" + strings.Join(insights[:kept], "\n\n")
		}
		candidate := appendPollHashtags(body, eventName, first, second)
		// appendPollHashtags truncates as a last resort; only accept a
		// candidate it did not have to cut.
		if len([]rune(candidate)) <= telegramPollQuestionLimit && strings.Contains(candidate, lastLineOf(body)) {
			return candidate, len(insights) - kept
		}
	}
	return appendPollHashtags(header, eventName, first, second), len(insights)
}

// lastLineOf is composePollQuestion's "was anything cut?" probe: if the
// final line of the assembled body survived into the result, nothing before
// it was truncated either.
func lastLineOf(s string) string {
	if idx := strings.LastIndex(s, "\n"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

func appendPollHashtags(question, eventName string, first, second *competition.Team) string {
	values := []string{eventName}
	if first != nil {
		values = append(values, first.Name)
	}
	if second != nil {
		values = append(values, second.Name)
	}

	seen := map[string]struct{}{}
	var tags []string
	for _, value := range values {
		tag := pollHashtag(value)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return truncate(question, telegramPollQuestionLimit)
	}

	// Each tag its own <code> span, not one span around the whole line:
	// tapping a code span copies exactly that span's text, and a reader
	// wants one tag at a time, not the entire hashtag line. Joined by
	// \n\n rather than a space for the same reason composePollQuestion's
	// insight lines are: Telegram's poll question collapses a lone
	// newline into a space but keeps a blank-line break, so \n\n is what
	// actually puts each tag on its own line.
	codedTags := make([]string, len(tags))
	for i, tag := range tags {
		codedTags[i] = code(tag)
	}
	tagLine := strings.Join(codedTags, "\n\n")
	const separator = "\n\n" // hashtags read as their own paragraph, not a trailing continuation
	available := telegramPollQuestionLimit - len([]rune(tagLine)) - len([]rune(separator))
	if available <= 0 {
		return truncate(tagLine, telegramPollQuestionLimit)
	}
	return truncate(question, available) + separator + tagLine
}

func formatBalance(b competition.TeamBalance) string {
	if b.Draws > 0 {
		return fmt.Sprintf("%d-%d-%d", b.Wins, b.Losses, b.Draws)
	}
	return fmt.Sprintf("%d-%d", b.Wins, b.Losses)
}

// maxEnrichmentInsightLines caps how many enrichment-sourced lines (as
// opposed to the PandaScore-native balance line) a poll question ever
// carries — VRS rank (with points in parentheses), recent form,
// head-to-head, in that priority order. Kept explicit, rather than relying
// on there being exactly 3 line types, so adding a 4th enrichment metric
// later doesn't silently blow the poll past a readable length.
const maxEnrichmentInsightLines = 3

// buildEnrichmentLines assembles the enrichment-sourced insight lines for a
// match, most important first: VRS rank (points alongside, in parentheses),
// recent form, head-to-head. A line is skipped if its data isn't available,
// and the result is truncated to maxEnrichmentInsightLines even if more
// candidates exist.
func (g *PollGateway) buildEnrichmentLines(locale common.LocaleCode, rankings map[common.TeamID]enrichment.TeamRanking, homeForm, awayForm *enrichment.RecentForm, h2h *enrichment.HeadToHead, first, second *competition.Team) []string {
	var lines []string
	if line := formatVRSCombined(rankings, first, second); line != "" {
		lines = append(lines, g.texts.Get("poll.vrs", locale, line))
	}
	if line := formatForm(homeForm, awayForm); line != "" {
		lines = append(lines, g.texts.Get("poll.form", locale, line))
	}
	if line := formatH2H(h2h); line != "" {
		lines = append(lines, g.texts.Get("poll.h2h", locale, line))
	}
	if len(lines) > maxEnrichmentInsightLines {
		lines = lines[:maxEnrichmentInsightLines]
	}
	return lines
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

// formatVRSCombined renders the poll's single VRS line, points alongside
// rank in parentheses — "#1(1993) — #3(1908)" — rather than the rank and
// points as two separate lines, since they're the same metric read two
// ways and a reader wants them together, not spread across the question.
func formatVRSCombined(rankings map[common.TeamID]enrichment.TeamRanking, first, second *competition.Team) string {
	if first == nil || second == nil {
		return ""
	}
	firstRank, secondRank := rankingInts(rankings, first.ID, second.ID, func(r enrichment.TeamRanking) *int { return r.GlobalRank })
	firstPoints, secondPoints := rankingInts(rankings, first.ID, second.ID, func(r enrichment.TeamRanking) *int { return r.Points })
	if firstRank == nil && secondRank == nil && firstPoints == nil && secondPoints == nil {
		return ""
	}
	return formatVRSSide(firstRank, firstPoints) + " — " + formatVRSSide(secondRank, secondPoints)
}

// formatVRSSide renders one team's half of formatVRSCombined: "#rank",
// "#rank(points)", "(points)" when only points are cached, or "—" when
// neither is.
func formatVRSSide(rank, points *int) string {
	switch {
	case rank != nil && points != nil:
		return fmt.Sprintf("#%d(%d)", *rank, *points)
	case rank != nil:
		return fmt.Sprintf("#%d", *rank)
	case points != nil:
		return fmt.Sprintf("(%d)", *points)
	default:
		return "—"
	}
}

// formatForm renders "wins-losses — wins-losses" for cached recent-form
// rows, "—" for a side with none. Returns "" when neither side has one.
func formatForm(home, away *enrichment.RecentForm) string {
	if home == nil && away == nil {
		return ""
	}
	homeStr, awayStr := "—", "—"
	if home != nil {
		homeStr = fmt.Sprintf("%d-%d", home.Wins, home.Losses)
	}
	if away != nil {
		awayStr = fmt.Sprintf("%d-%d", away.Wins, away.Losses)
	}
	return homeStr + " — " + awayStr
}

// formatH2H renders "teamAWins-teamBWins", matching the match's own
// first/second order (HeadToHeadRepository.FindHeadToHead already
// remaps TeamAWins/TeamBWins to whichever order it was asked for). Returns
// "" when there's no cached record, or a zero-sample one (nothing played).
func formatH2H(h2h *enrichment.HeadToHead) string {
	if h2h == nil || h2h.Sample == 0 {
		return ""
	}
	return fmt.Sprintf("%d-%d", h2h.TeamAWins, h2h.TeamBWins)
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

func (g *PollGateway) Close(ctx context.Context, poll prediction.Poll) error {
	return g.stop(ctx, poll)
}
func (g *PollGateway) Cancel(ctx context.Context, poll prediction.Poll) error {
	return g.stop(ctx, poll)
}

func (g *PollGateway) stop(ctx context.Context, poll prediction.Poll) error {
	if poll.TelegramMessageID == nil {
		return prediction.ErrPollMissingTelegramMessage
	}
	_, err := g.client.Call(ctx, "stopPoll", map[string]any{"chat_id": poll.ChatID.Value, "message_id": *poll.TelegramMessageID})
	return err
}
