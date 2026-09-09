-- +goose Up
-- A user's own chosen name for leaderboards and result lists, independent
-- of their live Telegram profile name. NULL/empty means "unset" — every
-- reader falls back to display_name, which stays Telegram-sourced and
-- keeps being refreshed on every vote (see PredictionRepository.SaveVote):
-- nickname is a separate column specifically so that refresh can never
-- clobber a name the person chose for themselves.
ALTER TABLE telegram_user ADD COLUMN nickname varchar(60);
ALTER TABLE telegram_user ADD CONSTRAINT telegram_user_nickname_not_blank
    CHECK (nickname IS NULL OR char_length(btrim(nickname)) > 0);

-- +goose Down
ALTER TABLE telegram_user DROP CONSTRAINT IF EXISTS telegram_user_nickname_not_blank;
ALTER TABLE telegram_user DROP COLUMN IF EXISTS nickname;
