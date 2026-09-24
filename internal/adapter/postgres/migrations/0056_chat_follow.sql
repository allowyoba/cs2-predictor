-- +goose Up
-- Team and player targets for a chat, alongside the tournament targets in
-- event_subscription. Soft-deleted like those, to keep history.
CREATE TABLE chat_follow (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id       bigint NOT NULL REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('TEAM', 'PLAYER')),
    team_id       uuid REFERENCES team(id) ON DELETE CASCADE,
    -- Lowercased nickname: players are only known from ranking rosters.
    player        text,
    label         text NOT NULL DEFAULT '',
    subscribed_at timestamptz NOT NULL,
    active        boolean NOT NULL DEFAULT true,
    CHECK ((kind = 'TEAM' AND team_id IS NOT NULL AND player IS NULL)
        OR (kind = 'PLAYER' AND player IS NOT NULL AND team_id IS NULL))
);
CREATE UNIQUE INDEX chat_follow_team_key ON chat_follow (chat_id, team_id) WHERE kind = 'TEAM';
CREATE UNIQUE INDEX chat_follow_player_key ON chat_follow (chat_id, player) WHERE kind = 'PLAYER';
CREATE INDEX chat_follow_team_active ON chat_follow (team_id) WHERE active AND kind = 'TEAM';
CREATE INDEX team_ranking_player_lower ON team_ranking_player (lower(player));

-- One tournament offer per chat and event, ever.
CREATE TABLE follow_cross_sell_offer (
    chat_id    bigint NOT NULL REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE,
    event_id   uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    offered_at timestamptz NOT NULL,
    PRIMARY KEY (chat_id, event_id)
);

-- +goose Down
DROP TABLE IF EXISTS follow_cross_sell_offer;
DROP INDEX IF EXISTS team_ranking_player_lower;
DROP TABLE IF EXISTS chat_follow;
