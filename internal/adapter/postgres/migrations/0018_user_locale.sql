-- +goose Up
-- A user's own UI language for private chats with the bot, independent of
-- any group chat's setting (telegram_chat.locale). NULL means "never
-- chosen" — the handler then falls back to the product default rather than
-- guessing, so an explicit choice is distinguishable from an absent one.
ALTER TABLE telegram_user ADD COLUMN locale varchar(10);

-- +goose Down
ALTER TABLE telegram_user DROP COLUMN IF EXISTS locale;
