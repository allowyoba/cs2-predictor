-- +goose Up
-- Granular moderator permissions: a moderator now holds zero or more named
-- capabilities instead of the previous all-or-nothing flag. Existing rows
-- are backfilled with every permission so nobody who was already trusted as
-- a moderator loses access the moment this ships.
CREATE TABLE chat_moderator_permission (
    chat_id     bigint NOT NULL,
    user_id     bigint NOT NULL,
    permission  varchar(40) NOT NULL,
    granted_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, user_id, permission),
    FOREIGN KEY (chat_id, user_id) REFERENCES chat_moderator(chat_id, user_id) ON DELETE CASCADE
);

INSERT INTO chat_moderator_permission (chat_id, user_id, permission, granted_at)
SELECT chat_id, user_id, perm, appointed_at
  FROM chat_moderator
 CROSS JOIN (VALUES ('manage_events'), ('manage_matches'), ('view_stats'), ('manage_group_settings')) AS p(perm);

-- One-time, revocable deep links that hand a chosen permission set to
-- whoever accepts them — an alternative to picking an existing member.
CREATE TABLE moderator_invitation (
    token       varchar(40) PRIMARY KEY,
    chat_id     bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    permissions varchar(200) NOT NULL,
    created_by  bigint NOT NULL REFERENCES telegram_user(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    used_by     bigint REFERENCES telegram_user(id),
    revoked_at  timestamptz
);
CREATE INDEX moderator_invitation_chat_idx ON moderator_invitation (chat_id);

-- +goose Down
DROP TABLE IF EXISTS moderator_invitation;
DROP TABLE IF EXISTS chat_moderator_permission;
