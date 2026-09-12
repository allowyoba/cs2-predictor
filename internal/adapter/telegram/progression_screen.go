package telegram

import (
	"context"
	"fmt"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func (h *UpdateHandler) progressionRepo() (scoring.ProgressionRepository, error) {
	repo, ok := h.Scoring.(scoring.ProgressionRepository)
	if !ok {
		return nil, fmt.Errorf("scoring repository does not support rating charts")
	}
	return repo, nil
}

// sendProgressionChart renders and uploads a rating-movement chart for
// period. Whether it draws every participant or just the caller's own line
// follows the exact same "personal vs. group" signal renderLeaderboard's
// title already uses (target.chatID != settings.ChatID means this
// particular render is happening in a DM, proxied from an admin managing
// settings.ChatID from their own private chat) — a chart asked for from
// inside the group itself shows everyone in it; one asked for from a DM
// shows only the person who asked.
func (h *UpdateHandler) sendProgressionChart(ctx context.Context, target replyTarget, settings chat.Settings, period scoring.StatsPeriod, viewer common.UserID) error {
	repo, err := h.progressionRepo()
	if err != nil {
		return err
	}
	points, err := repo.PointsProgression(ctx, settings.ChatID, period)
	if err != nil {
		return err
	}

	var subject *common.UserID
	if target.chatID != settings.ChatID {
		subject = &viewer
	}

	periodName, err := h.periodName(ctx, settings, period)
	if err != nil {
		return err
	}
	title := h.Texts.Get("chart.title", settings.Locale, periodName)
	if subject != nil {
		title = h.Texts.Get("chart.title_mine", settings.Locale, periodName)
	}

	img, err := buildProgressionChart(points, title, h.Texts.Get("chart.x_label", settings.Locale), h.Texts.Get("chart.y_label", settings.Locale), subject)
	if err != nil {
		return h.respond(ctx, target, h.Texts.Get("chart.empty", settings.Locale), nil)
	}
	return h.Client.SendPhoto(ctx, target.chatID.Value, "chart.png", img, "", target.topicID)
}

func chartCallbackData(period scoring.StatsPeriod) string {
	switch period.Kind {
	case scoring.PeriodYear:
		return fmt.Sprintf("stats:chart:y:%d", period.Year)
	case scoring.PeriodMonth:
		return fmt.Sprintf("stats:chart:m:%04d-%02d", period.Year, period.Month)
	case scoring.PeriodEvent:
		return "stats:chart:e:" + period.EventID.Value.String()
	default:
		return "stats:chart:a"
	}
}

// sendRankChart is sendProgressionChart's sibling for leaderboard position
// instead of cumulative points — same data source, same personal-vs-group
// rule (see sendProgressionChart's own doc comment), just handed to
// buildRankChart instead.
func (h *UpdateHandler) sendRankChart(ctx context.Context, target replyTarget, settings chat.Settings, period scoring.StatsPeriod, viewer common.UserID) error {
	repo, err := h.progressionRepo()
	if err != nil {
		return err
	}
	points, err := repo.PointsProgression(ctx, settings.ChatID, period)
	if err != nil {
		return err
	}

	var subject *common.UserID
	if target.chatID != settings.ChatID {
		subject = &viewer
	}

	periodName, err := h.periodName(ctx, settings, period)
	if err != nil {
		return err
	}
	title := h.Texts.Get("chart.rank_title", settings.Locale, periodName)
	if subject != nil {
		title = h.Texts.Get("chart.rank_title_mine", settings.Locale, periodName)
	}

	img, err := buildRankChart(points, title, h.Texts.Get("chart.x_label", settings.Locale), h.Texts.Get("chart.rank_y_label", settings.Locale), subject)
	if err != nil {
		return h.respond(ctx, target, h.Texts.Get("chart.empty", settings.Locale), nil)
	}
	return h.Client.SendPhoto(ctx, target.chatID.Value, "chart.png", img, "", target.topicID)
}

func rankChartCallbackData(period scoring.StatsPeriod) string {
	switch period.Kind {
	case scoring.PeriodYear:
		return fmt.Sprintf("stats:rankchart:y:%d", period.Year)
	case scoring.PeriodMonth:
		return fmt.Sprintf("stats:rankchart:m:%04d-%02d", period.Year, period.Month)
	case scoring.PeriodEvent:
		return "stats:rankchart:e:" + period.EventID.Value.String()
	default:
		return "stats:rankchart:a"
	}
}
