-- +goose Up
-- An append-only record of administrative changes per chat. With
-- administration moved into private messages, a co-manager no longer
-- witnesses a change happening in the group, so this is the only way they
-- can find out who turned something off.
--
-- actor_name is denormalized on purpose: it is what the acting user was
-- called at the time, which is what a reader of the history wants, and it
-- survives that user leaving the chat entirely. There is deliberately no
-- FK to telegram_user for the same reason.
CREATE TABLE admin_action_log (
    id          bigserial PRIMARY KEY,
    chat_id     bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    actor_id    bigint NOT NULL,
    actor_name  text NOT NULL DEFAULT '',
    -- stable slug naming the surface that changed ('subscribe', 'locale');
    -- see chat.AdminAction.Kind
    kind        text NOT NULL,
    -- already-formatted display text (an event name, a timezone), stored
    -- as-is because what it names may be gone by the time it is read
    detail      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The only read pattern: the newest N for one chat.
CREATE INDEX admin_action_log_chat_recent_idx ON admin_action_log (chat_id, created_at DESC);
-- Supports the retention sweep, which scans by age across all chats.
CREATE INDEX admin_action_log_created_at_idx ON admin_action_log (created_at);

-- +goose Down
DROP TABLE IF EXISTS admin_action_log;
