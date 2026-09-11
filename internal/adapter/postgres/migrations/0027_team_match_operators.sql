-- +goose Up
-- team_match_operator is the delegated tier of /team_matches access: root
-- operators (Config.TeamMatchOperatorChatIDs, from DEPLOY_NOTIFY_CHAT_IDS)
-- are fixed at deploy time, but they can appoint additional people here via
-- /team_match_admin without an environment variable change or a restart.
CREATE TABLE team_match_operator (
    user_id         bigint PRIMARY KEY REFERENCES telegram_user(id) ON DELETE CASCADE,
    appointed_by    bigint NOT NULL,
    appointed_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS team_match_operator;
