-- +goose Up
-- Whether the bot may DM this user. Telegram refuses a bot's first message
-- to someone who has never started a chat with it, so this is the only way
-- to know in advance whether a confirmation request can actually reach a
-- co-manager. Set true on any private interaction, false when a send comes
-- back 403; false is the safe default for users we've only ever seen in a
-- group.
ALTER TABLE telegram_user ADD COLUMN dm_reachable boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE telegram_user DROP COLUMN IF EXISTS dm_reachable;
