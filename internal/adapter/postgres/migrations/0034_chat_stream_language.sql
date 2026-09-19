-- +goose Up
-- The language a chat wants match broadcasts in. Empty (the default) means
-- "follow the chat's UI language" — see chat.Settings.StreamLocale.
ALTER TABLE telegram_chat ADD COLUMN stream_language text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE telegram_chat DROP COLUMN stream_language;
