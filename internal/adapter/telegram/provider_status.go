package telegram

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// providerStatusView answers "is anything actually broken right now?" for
// every external resource this bot depends on: the competition provider
// gateway (PandaScore, behind CompetitionProviderGateway's own circuit
// breaker) and every enabled enrichment source (Valve VRS, HLTV, GRID,
// Liquipedia — each backed by provider_sync_state, the same table
// /healthz/ready already reads). Root-operator-only (the same
// DEPLOY_NOTIFY_CHAT_IDS set /team_match_admin uses) since this is a
// bot-operations concern, not something any group admin needs.
func (h *UpdateHandler) providerStatusView(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("providers.refresh", locale), "hub:provider_status")},
		{h.backButton(locale, "hub:system")},
	}}
	if !h.isRootTeamMatchOperator(userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), back)
	}

	rows := h.collectProviderRows(ctx, locale)
	if len(rows) == 0 {
		return h.respond(ctx, target, h.Texts.Get("providers.empty", locale), back)
	}

	// Worst first. An operator opens this screen to find out whether
	// anything needs them, and a broken provider four scrolls down behind
	// three healthy ones is a broken provider nobody sees.
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].severity > rows[b].severity })

	var healthy int
	for _, row := range rows {
		if row.severity == severityOK {
			healthy++
		}
	}
	headline := h.Texts.Get("providers.all_ok", locale, healthy)
	if healthy < len(rows) {
		headline = h.Texts.Get("providers.some_down", locale, len(rows)-healthy, len(rows))
	}

	sections := make([]string, 0, len(rows))
	for _, row := range rows {
		sections = append(sections, row.text)
	}
	text := bold(h.Texts.Get("providers.title", locale)) + "\n" + headline + "\n\n" + strings.Join(sections, "\n\n")
	return h.respond(ctx, target, text, back)
}

// Severity orders the screen and decides the headline. Three levels rather
// than a boolean because "never ran" and "ran and failed" need different
// answers from an operator: one is a provider nobody enabled properly, the
// other is a provider that broke.
const (
	severityOK = iota
	severityUnknown
	severityDown
)

type providerRow struct {
	severity int
	text     string
}

// collectProviderRows gathers one row per external dependency.
func (h *UpdateHandler) collectProviderRows(ctx context.Context, locale common.LocaleCode) []providerRow {
	var rows []providerRow
	if h.ProviderGateway != nil {
		status, details := h.ProviderGateway.Health()
		severity := severityDown
		switch status {
		case app.HealthUp:
			severity = severityOK
		case app.HealthUnknown:
			severity = severityUnknown
		}
		rows = append(rows, providerRow{
			severity: severity,
			text:     formatCompetitionProviderStatus(h.Texts, locale, status, details, h.Clock.Now()),
		})
	}
	for _, source := range h.EnrichmentSources {
		if h.EnrichmentState == nil {
			continue
		}
		st, err := h.EnrichmentState.State(ctx, source)
		if err != nil {
			rows = append(rows, providerRow{severity: severityDown,
				text: "🔴 " + bold(providerName(source)) + "\n" + h.Texts.Get("providers.status_error", locale)})
			continue
		}
		rows = append(rows, h.enrichmentRow(locale, source, st))
	}
	return rows
}

// providerSeverity judges one source: whether it is broken, and whether it
// is simply late.
//
// A feed polled every fifteen minutes that last succeeded three hours ago
// is broken; one fetched weekly is not. Without the expected interval the
// same timestamp means both things, which is why this screen used to leave
// the judgement to whoever remembered the configuration. Two intervals of
// grace before calling it late: one missed run is a blip, two in a row is
// a pattern.
func providerSeverity(st *enrichment.SyncState, now time.Time, expected time.Duration) (severity int, overdue bool) {
	switch {
	case st.LastSuccessAt == nil && st.LastErrorAt == nil:
		return severityUnknown, false
	case st.ConsecutiveFailures > 0 || st.LastSuccessAt == nil:
		return severityDown, false
	}
	if expected > 0 && now.Sub(*st.LastSuccessAt) > 2*expected {
		return severityDown, true
	}
	return severityOK, false
}

// enrichmentRow renders one source, and judges its freshness against how
// often it is actually configured to run.
func (h *UpdateHandler) enrichmentRow(locale common.LocaleCode, source enrichment.Source, st *enrichment.SyncState) providerRow {
	now := h.Clock.Now()
	expected, haveExpected := h.EnrichmentIntervals[source]
	severity, overdue := providerSeverity(st, now, expected)

	icon := providerStatusIcon(severity == severityOK, severity == severityUnknown)
	lines := []string{icon + " " + bold(providerName(source))}
	lines = append(lines, h.Texts.Get("providers.last_success", locale)+": "+h.age(locale, now, st.LastSuccessAt))
	if haveExpected && expected > 0 {
		line := h.Texts.Get("providers.expected", locale, humanAge(h.Texts, locale, expected))
		if overdue {
			line += " — " + h.Texts.Get("providers.overdue", locale)
		}
		lines = append(lines, line)
	}
	if st.ConsecutiveFailures > 0 {
		lines = append(lines, h.Texts.Get("providers.consecutive_failures", locale)+": "+strconv.Itoa(st.ConsecutiveFailures))
	}
	if st.LastErrorAt != nil {
		lines = append(lines, h.Texts.Get("providers.last_error", locale)+": "+h.age(locale, now, st.LastErrorAt))
		if msg := strings.TrimSpace(st.LastError); msg != "" {
			lines = append(lines, code(escapeHTML(truncate(msg, 120))))
		}
	}
	return providerRow{severity: severity, text: strings.Join(lines, "\n")}
}

// age renders how long ago something happened, or "never" when it has not.
func (h *UpdateHandler) age(locale common.LocaleCode, now time.Time, at *time.Time) string {
	if at == nil {
		return h.Texts.Get("providers.never", locale)
	}
	return h.Texts.Get("providers.ago", locale, humanAge(h.Texts, locale, now.Sub(*at)))
}

// providerName spells a source the way a person would say it, instead of
// the SCREAMING_SNAKE constant the database stores.
func providerName(source enrichment.Source) string {
	switch source {
	case enrichment.SourceValveVRS:
		return "Valve VRS"
	case enrichment.SourceHLTV:
		return "HLTV"
	case enrichment.SourceGRID:
		return "GRID"
	case enrichment.SourceLiquipedia:
		return "Liquipedia"
	default:
		return string(source)
	}
}

// humanAge renders a duration the way somebody reading a status page
// needs it — "4 мин", "3 ч", "2 дн" — because the question is always "is
// this fresh?" and never "what exact second was it".
func humanAge(texts *Texts, locale common.LocaleCode, d time.Duration) string {
	switch {
	case d < time.Minute:
		return texts.Get("providers.just_now", locale)
	case d < time.Hour:
		return texts.Get("providers.minutes", locale, int(d.Minutes()))
	case d < 24*time.Hour:
		return texts.Get("providers.hours", locale, int(d.Hours()))
	default:
		return texts.Get("providers.days", locale, int(d.Hours()/24))
	}
}

// formatCompetitionProviderStatus renders app.CompetitionProviderGateway's
// aggregate Health() — one combined status across every configured
// competition provider (see CompetitionProviderGateway.Health's own doc for
// why this is aggregate rather than per-provider: the gateway's circuit
// breaker state is in-memory only, per-process, not persisted).
func formatCompetitionProviderStatus(texts *Texts, locale common.LocaleCode, status app.HealthStatus, details map[string]any, now time.Time) string {
	b := providerStatusIcon(status == app.HealthUp, status == app.HealthUnknown) + " " + bold("PandaScore") +
		" · " + texts.Get("providers.source_of_truth", locale)
	if at, ok := parseStatusTime(details["lastSuccess"]); ok {
		b += "\n" + texts.Get("providers.last_success", locale) + ": " +
			texts.Get("providers.ago", locale, humanAge(texts, locale, now.Sub(at)))
	} else {
		b += "\n" + texts.Get("providers.last_success", locale) + ": " + texts.Get("providers.never", locale)
	}
	if at, ok := parseStatusTime(details["lastFailure"]); ok {
		b += "\n" + texts.Get("providers.last_failure", locale) + ": " +
			texts.Get("providers.ago", locale, humanAge(texts, locale, now.Sub(at)))
	}
	return b
}

// parseStatusTime reads the gateway's own RFC3339 strings back. An
// unparsable or absent value is "never", not a zero time rendered as 1970.
func parseStatusTime(value any) (time.Time, bool) {
	text, _ := value.(string)
	if text == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// providerStatusIcon: 🟢 up, 🔴 down, ⚪ unknown (no call has ever
// succeeded or failed yet — a provider that was only just enabled).
func providerStatusIcon(up, unknown bool) string {
	switch {
	case unknown:
		return "⚪"
	case up:
		return "🟢"
	default:
		return "🔴"
	}
}
