package telegram

import (
	"context"
	"fmt"
	"strings"

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
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "hub:system")}}}
	if !h.isRootTeamMatchOperator(userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), back)
	}

	var sections []string
	if h.ProviderGateway != nil {
		status, details := h.ProviderGateway.Health()
		sections = append(sections, formatCompetitionProviderStatus(h.Texts, locale, status, details))
	}
	for _, source := range h.EnrichmentSources {
		if h.EnrichmentState == nil {
			continue
		}
		st, err := h.EnrichmentState.State(ctx, source)
		if err != nil {
			sections = append(sections, "🔴 <b>"+escapeHTML(string(source))+"</b> — "+h.Texts.Get("providers.status_error", locale))
			continue
		}
		sections = append(sections, formatEnrichmentProviderStatus(h.Texts, locale, source, st))
	}
	if len(sections) == 0 {
		return h.respond(ctx, target, h.Texts.Get("providers.empty", locale), back)
	}
	text := bold(h.Texts.Get("providers.title", locale)) + "\n\n" + strings.Join(sections, "\n\n")
	return h.respond(ctx, target, text, back)
}

// formatCompetitionProviderStatus renders app.CompetitionProviderGateway's
// aggregate Health() — one combined status across every configured
// competition provider (see CompetitionProviderGateway.Health's own doc for
// why this is aggregate rather than per-provider: the gateway's circuit
// breaker state is in-memory only, per-process, not persisted).
func formatCompetitionProviderStatus(texts *Texts, locale common.LocaleCode, status app.HealthStatus, details map[string]any) string {
	lastSuccess, _ := details["lastSuccess"].(string)
	lastFailure, _ := details["lastFailure"].(string)
	return fmt.Sprintf("%s <b>PandaScore</b> (%s)\n%s: %s\n%s: %s",
		providerStatusIcon(status == app.HealthUp, status == app.HealthUnknown),
		escapeHTML(string(status)),
		texts.Get("providers.last_success", locale), escapeHTML(lastSuccess),
		texts.Get("providers.last_failure", locale), escapeHTML(lastFailure))
}

// formatEnrichmentProviderStatus renders one enrichment.SyncState — this
// one IS per-provider and persisted (provider_sync_state), unlike the
// competition gateway's in-memory aggregate above.
func formatEnrichmentProviderStatus(texts *Texts, locale common.LocaleCode, source enrichment.Source, st *enrichment.SyncState) string {
	up := st.ConsecutiveFailures == 0 && st.LastSuccessAt != nil
	unknown := st.LastSuccessAt == nil && st.LastErrorAt == nil
	lastSuccess := texts.Get("providers.never", locale)
	if st.LastSuccessAt != nil {
		lastSuccess = st.LastSuccessAt.UTC().Format("02.01.2006 15:04 UTC")
	}
	b := fmt.Sprintf("%s <b>%s</b>\n%s: %s",
		providerStatusIcon(up, unknown), escapeHTML(string(source)),
		texts.Get("providers.last_success", locale), lastSuccess)
	if st.LastErrorAt != nil {
		b += fmt.Sprintf("\n%s: %s — %s", texts.Get("providers.last_error", locale),
			st.LastErrorAt.UTC().Format("02.01.2006 15:04 UTC"), escapeHTML(truncate(st.LastError, 80)))
	}
	if st.ConsecutiveFailures > 0 {
		b += fmt.Sprintf("\n%s: %d", texts.Get("providers.consecutive_failures", locale), st.ConsecutiveFailures)
	}
	return b
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
