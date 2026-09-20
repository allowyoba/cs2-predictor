-- +goose Up
-- One table for every switch the bot has, because the answer to "what is
-- this chat allowed to send me" had been spread across boolean columns on
-- two unrelated tables, and every new notification meant another column
-- and another migration.
--
-- A missing row means off. That is the whole default: nothing the bot
-- sends on its own initiative goes out until somebody has asked for it,
-- and "somebody has asked for it" is exactly a row here.
--
-- subject_id is deliberately not a foreign key. It means a chat under
-- 'chat', a person under 'user', and an operator's chat under 'operator' —
-- the last of which is a chat id read from configuration that may not
-- correspond to any row the bot has ever stored.
CREATE TABLE notification_preference (
    scope      text   NOT NULL CHECK (scope IN ('chat', 'user', 'operator')),
    subject_id bigint NOT NULL,
    kind       text   NOT NULL,
    enabled    boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, subject_id, kind)
);

-- The recipient lookups all ask "who, among these, has this kind on".
CREATE INDEX notification_preference_kind_idx
    ON notification_preference (scope, kind) WHERE enabled;

-- Carry across the three switches that already existed, so nobody who has
-- turned something on loses it. Everything else this release introduces
-- starts off, which is what it would have been anyway.
INSERT INTO notification_preference (scope, subject_id, kind, enabled)
SELECT 'user', id, 'recaps', true FROM telegram_user WHERE result_recaps;
INSERT INTO notification_preference (scope, subject_id, kind, enabled)
SELECT 'user', id, 'reminders', true FROM telegram_user WHERE poll_reminders;
INSERT INTO notification_preference (scope, subject_id, kind, enabled)
SELECT 'chat', id, 'streams', true FROM telegram_chat WHERE stream_announcements;

ALTER TABLE telegram_user DROP COLUMN result_recaps;
ALTER TABLE telegram_user DROP COLUMN poll_reminders;
ALTER TABLE telegram_chat DROP COLUMN stream_announcements;

-- +goose Down
ALTER TABLE telegram_user ADD COLUMN result_recaps boolean NOT NULL DEFAULT false;
ALTER TABLE telegram_user ADD COLUMN poll_reminders boolean NOT NULL DEFAULT false;
ALTER TABLE telegram_chat ADD COLUMN stream_announcements boolean NOT NULL DEFAULT false;

UPDATE telegram_user u SET result_recaps = true
  FROM notification_preference p
 WHERE p.scope = 'user' AND p.subject_id = u.id AND p.kind = 'recaps' AND p.enabled;
UPDATE telegram_user u SET poll_reminders = true
  FROM notification_preference p
 WHERE p.scope = 'user' AND p.subject_id = u.id AND p.kind = 'reminders' AND p.enabled;
UPDATE telegram_chat c SET stream_announcements = true
  FROM notification_preference p
 WHERE p.scope = 'chat' AND p.subject_id = c.id AND p.kind = 'streams' AND p.enabled;

DROP TABLE IF EXISTS notification_preference;
