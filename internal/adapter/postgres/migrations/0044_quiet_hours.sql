-- +goose Up
-- Quiet hours: a window, in the chat's own timezone, during which the bot
-- holds its proactive messages instead of posting them.
--
-- Stored as minutes since local midnight rather than as a time, because
-- that is what the comparison needs and it sidesteps every question about
-- what a "time" column means across a DST change. NULL on both means the
-- chat has not set a window, which is the default for every chat.
ALTER TABLE telegram_chat
    ADD COLUMN quiet_from_minute smallint,
    ADD COLUMN quiet_to_minute   smallint;

ALTER TABLE telegram_chat
    ADD CONSTRAINT telegram_chat_quiet_hours_both_or_neither
    CHECK ((quiet_from_minute IS NULL) = (quiet_to_minute IS NULL)),
    ADD CONSTRAINT telegram_chat_quiet_hours_range
    CHECK (quiet_from_minute IS NULL OR
           (quiet_from_minute BETWEEN 0 AND 1439 AND quiet_to_minute BETWEEN 0 AND 1439));

-- A held message is rescheduled rather than dropped, so the outbox needs
-- no new state: next_attempt_at already means "do not touch this before".

-- +goose Down
ALTER TABLE telegram_chat
    DROP CONSTRAINT IF EXISTS telegram_chat_quiet_hours_both_or_neither,
    DROP CONSTRAINT IF EXISTS telegram_chat_quiet_hours_range,
    DROP COLUMN IF EXISTS quiet_from_minute,
    DROP COLUMN IF EXISTS quiet_to_minute;
