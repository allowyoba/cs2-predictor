-- +goose Up
-- Widened for markers keyed by an event id (36 chars) rather than a
-- calendar period: the tournament-eve nudge claims one per event, the same
-- way monthly/annual digests claim one per period.
ALTER TABLE scheduled_report ALTER COLUMN period_key TYPE varchar(64);

-- One row per release the bot has already announced to its administrators.
-- Deliberately not a scheduled_report marker: that table is keyed by a chat
-- that must exist in telegram_chat, and the administrators are addressed by
-- raw chat id (DEPLOY_NOTIFY_CHAT_IDS) which need not be a registered chat.
-- Announcing is per release, not per recipient, so one row covers the fan-out.
CREATE TABLE release_announcement (
    version      varchar(64) NOT NULL,
    commit_sha   varchar(64) NOT NULL,
    announced_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (version, commit_sha)
);

-- +goose Down
DROP TABLE IF EXISTS release_announcement;
ALTER TABLE scheduled_report ALTER COLUMN period_key TYPE varchar(16);
