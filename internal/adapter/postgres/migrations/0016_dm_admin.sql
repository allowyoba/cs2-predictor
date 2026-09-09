-- +goose Up
-- chat_manager_seen records every (chat, user) pair where the user has
-- successfully passed a manager/admin authorization check at least once —
-- an opportunistic index populated as a side effect of normal admin
-- actions, not a live Telegram query (Telegram has no "list every chat a
-- user administers" API). Powers the DM "chats you manage" picker; a stale
-- or missing row only affects that picker's completeness, never
-- authorization itself (every actual admin action still re-checks live
-- Telegram membership via chat.AuthorizationService).
CREATE TABLE chat_manager_seen (
    chat_id             bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    user_id             bigint NOT NULL REFERENCES telegram_user(id),
    last_confirmed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX chat_manager_seen_user_id_idx ON chat_manager_seen(user_id);

-- dm_admin_session holds which single managed chat a user's private
-- conversation with the bot is currently "pointed at" — Telegram inline
-- button callback_data has no room to carry a full chat id on every single
-- admin-menu button (they're reused verbatim between the group and DM
-- rendering paths), so this is the DM equivalent of "which group chat is
-- this message in" for a private chat. One row per user: opening another
-- managed chat's panel simply overwrites it.
CREATE TABLE dm_admin_session (
    user_id     bigint PRIMARY KEY REFERENCES telegram_user(id),
    chat_id     bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS dm_admin_session;
DROP TABLE IF EXISTS chat_manager_seen;
