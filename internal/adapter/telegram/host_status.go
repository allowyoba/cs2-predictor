package telegram

import (
	"context"
	"strings"

	"cs2predictor/internal/app"
	"cs2predictor/internal/platform/common"
)

// hostStatusView answers "how close is the machine itself to trouble right
// now?" — memory, disk and one-minute-load-per-CPU, each against the same
// HostLimits threshold HostMonitor alerts on. Root-operator-only, the same
// gate /provider_status uses: this is a bot-operations concern, not
// something any group admin needs.
//
// Always a fresh read (ReadHostUsage, called directly) rather than the last
// periodic HostMonitor.Check sample — opening this screen must never touch
// HostMonitor's own breached-state or fire/suppress an alert.
func (h *UpdateHandler) hostStatusView(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("host.refresh", locale), "hub:host_status")},
		{h.backButton(locale, "hub:system")},
	}}
	if !h.isRootTeamMatchOperator(userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), back)
	}

	reader := h.HostUsageReader
	if reader == nil {
		reader = app.ReadHostUsage
	}
	usage, err := reader(h.HostRoot)
	if err != nil {
		h.Log.Warn("host usage unavailable", "error", err)
		text := bold(h.Texts.Get("host.title", locale)) + "\n" + h.Texts.Get("host.unavailable", locale)
		return h.respond(ctx, target, text, back)
	}

	limits := h.HostLimits
	rows := []string{
		hostResourceLine(h.Texts, locale, "host.memory", usage.MemoryUsed, limits.Memory),
		hostResourceLine(h.Texts, locale, "host.disk", usage.DiskUsed, limits.Disk),
		hostResourceLine(h.Texts, locale, "host.load", usage.LoadPerCPU, limits.Load),
	}
	text := bold(h.Texts.Get("host.title", locale)) + "\n\n" + strings.Join(rows, "\n\n")
	return h.respond(ctx, target, text, back)
}

// hostResourceLine renders one resource's reading next to its threshold,
// using the same OK/breached icon language /provider_status uses so an
// admin reads severity the same way across every status screen.
func hostResourceLine(texts *Texts, locale common.LocaleCode, labelKey string, value, limit float64) string {
	resource := hostResourceName(labelKey)
	icon := providerStatusIcon(!app.Breached(value, limit), limit <= 0)
	line := icon + " " + bold(texts.Get(labelKey, locale)) + ": " + app.DescribeUsage(resource, value)
	if limit > 0 {
		line += " / " + app.DescribeUsage(resource, limit)
		if app.Breached(value, limit) {
			line += " — " + texts.Get("host.breached", locale)
		} else {
			line += " — " + texts.Get("host.ok", locale)
		}
	}
	return line
}

// hostResourceName maps a locale key back to the resource name
// DescribeUsage/Breached expect — the same three strings HostMonitor.Check
// uses ("memory", "disk", "load").
func hostResourceName(labelKey string) string {
	switch labelKey {
	case "host.memory":
		return "memory"
	case "host.disk":
		return "disk"
	default:
		return "load"
	}
}
