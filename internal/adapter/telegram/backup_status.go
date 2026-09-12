package telegram

import (
	"context"
	"fmt"
	"strings"

	"cs2predictor/internal/platform/common"
)

// backupStatusRecentLimit bounds how many recent backups the screen lists —
// enough to see a healthy run cadence (roughly one per deploy) without the
// screen growing unbounded as backups accumulate over the project's life.
const backupStatusRecentLimit = 10

// backupStatusView answers "is the database actually getting backed up?"
// for an operator without having to SSH in and check the deploy logs or the
// bucket by hand. Root-operator-only (the same DEPLOY_NOTIFY_CHAT_IDS set
// /team_match_admin and /provider_status use) since this is a bot-operations
// concern, not something any group admin needs. This bot process never
// performs a backup itself — see db_backup_log's own migration comment —
// so an empty or nil BackupStatus just means "nothing recorded yet", not
// "backups are broken".
func (h *UpdateHandler) backupStatusView(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "hub:system")}}}
	if !h.isRootTeamMatchOperator(userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), back)
	}
	if h.BackupStatus == nil {
		return h.respond(ctx, target, h.Texts.Get("backup.empty", locale), back)
	}
	backups, err := h.BackupStatus.RecentBackups(ctx, backupStatusRecentLimit)
	if err != nil {
		return err
	}
	if len(backups) == 0 {
		return h.respond(ctx, target, h.Texts.Get("backup.empty", locale), back)
	}

	var sections []string
	for _, rec := range backups {
		sections = append(sections, formatBackupRecord(h.Texts, locale, rec))
	}
	text := bold(h.Texts.Get("backup.title", locale)) + "\n\n" + strings.Join(sections, "\n\n")
	return h.respond(ctx, target, text, back)
}

func formatBackupRecord(texts *Texts, locale common.LocaleCode, rec common.BackupRecord) string {
	size := texts.Get("backup.size_unknown", locale)
	if rec.SizeBytes != nil {
		size = formatByteSize(*rec.SizeBytes)
	}
	return fmt.Sprintf("🟢 <code>%s</code>\n%s · %s: %s · %s: %s",
		escapeHTML(rec.Label),
		rec.CreatedAt.UTC().Format("02.01.2006 15:04 UTC"),
		texts.Get("backup.storage", locale), escapeHTML(strings.ToUpper(rec.Storage)),
		texts.Get("backup.size", locale), size)
}

// formatByteSize renders a byte count as the largest whole unit that keeps
// one decimal place readable ("482.3 MB"), matching the precision an
// operator sanity-checking a backup's size actually needs — not exact byte
// counts, which a compressed dump's size never needs to be checked to.
func formatByteSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
