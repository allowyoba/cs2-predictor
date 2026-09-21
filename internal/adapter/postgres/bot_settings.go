package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cs2predictor/internal/platform/common"
)

var _ common.BotSettings = (*ChatRepository)(nil)

// BotSetting reads one deployment-wide setting. Absent is "", which every
// caller reads as the default — see common.BotSettings.
func (r *ChatRepository) BotSetting(ctx context.Context, key common.BotSettingKey) (string, error) {
	var value string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT value FROM bot_setting WHERE key = $1`, string(key)).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (r *ChatRepository) SetBotSetting(ctx context.Context, key common.BotSettingKey, value string, by common.UserID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO bot_setting (key, value, updated_at, updated_by) VALUES ($1, $2, now(), $3)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now(), updated_by = excluded.updated_by`,
		string(key), value, by.Value)
	return err
}
