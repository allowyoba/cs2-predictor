-- +goose Up
-- Per-user opt-ins for the two private nudges the bot can send. Both
-- default to false: a bot that starts messaging people privately because
-- they once voted in a group is spam, so these are only ever turned on
-- from the person's own DM settings.
ALTER TABLE telegram_user ADD COLUMN result_recaps boolean NOT NULL DEFAULT false;
ALTER TABLE telegram_user ADD COLUMN poll_reminders boolean NOT NULL DEFAULT false;

-- When the pre-close reminder for this poll was sent. NULL means "not yet";
-- set once, so a reminder job that runs every minute doesn't re-send.
ALTER TABLE match_poll ADD COLUMN reminded_at timestamptz;

-- The reminder job's read pattern: open polls closing soon that have not
-- been reminded about yet.
CREATE INDEX match_poll_reminder_idx ON match_poll (closes_at) WHERE reminded_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS match_poll_reminder_idx;
ALTER TABLE match_poll DROP COLUMN IF EXISTS reminded_at;
ALTER TABLE telegram_user DROP COLUMN IF EXISTS poll_reminders;
ALTER TABLE telegram_user DROP COLUMN IF EXISTS result_recaps;
