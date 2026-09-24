-- +goose Up
-- Team/player subscriptions: a chat follows a team or a player across
-- every tournament it plays in, independent of event_subscription (the
-- tournament-scoped kind). One table for both kinds, same shape as
-- notification_preference's scope/kind split, since a player is just a
-- second flavor of "target" and duplicating the table would only diverge
-- over time.
-- target_id is text, not a foreign key: for TEAM it is a team.id (uuid) as
-- text, for PLAYER it is whatever provider id the (not yet modeled) player
-- data uses — see subscription.TargetSubscription's doc comment.
CREATE TABLE target_subscription (
    chat_id       bigint      NOT NULL,
    kind          text        NOT NULL CHECK (kind IN ('TEAM', 'PLAYER')),
    target_id     text        NOT NULL,
    target_name   text        NOT NULL DEFAULT '',
    subscribed_at timestamptz NOT NULL,
    active        boolean     NOT NULL DEFAULT true,
    PRIMARY KEY (chat_id, kind, target_id)
);

-- Fan-out lookups: "which chats follow this team/player" for notifications
-- and cross-sell.
CREATE INDEX target_subscription_target_idx
    ON target_subscription (kind, target_id) WHERE active;

-- One-tap tournament cross-sell offers made off the back of a team/player
-- subscription — tracked per (chat, event) so a chat is never offered the
-- same tournament twice regardless of which target subscription surfaced
-- it first.
CREATE TABLE target_cross_sell_offer (
    chat_id     bigint      NOT NULL,
    event_id    uuid        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('TEAM', 'PLAYER')),
    target_id   text        NOT NULL,
    offered_at  timestamptz NOT NULL,
    dismissed   boolean     NOT NULL DEFAULT false,
    subscribed  boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (chat_id, event_id)
);

-- +goose Down
DROP TABLE IF EXISTS target_cross_sell_offer;
DROP TABLE IF EXISTS target_subscription;
