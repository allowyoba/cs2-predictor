-- +goose Up
-- Players, as their own entity rather than the free text they have been so
-- far.
--
-- Until now the only player data anywhere was team_ranking_player: HLTV's
-- reported roster, stored as nicknames keyed by (team, source, position).
-- That is enough to print a roster and nothing else — a nickname is not an
-- identity, so nothing could be followed, counted or joined to a match.
--
-- Two feeds name the same person differently: PandaScore by a numeric id
-- (with a nickname in "name"), HLTV by a nickname on a roster and nothing
-- else. So the canonical key is ours, and each feed's own key hangs off it
-- in player_identity — the same shape team_identity already uses for teams,
-- for the same reason.
CREATE TABLE player (
    id          uuid PRIMARY KEY,
    game_id     smallint     NOT NULL REFERENCES game (id),
    nickname    varchar(100) NOT NULL,
    full_name   varchar(200) NOT NULL DEFAULT '',
    nationality varchar(8)   NOT NULL DEFAULT '',
    image_url   text         NOT NULL DEFAULT '',
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now()
);

-- Nickname search is the whole point of the follow flow, and it is always
-- scoped to the games a chat follows.
CREATE INDEX player_nickname_idx ON player (game_id, lower(nickname));

-- One row per (player, feed). external_id is the feed's own key: PandaScore's
-- numeric player id, or HLTV's normalized nickname — HLTV publishes no id of
-- any kind, which is exactly why the nickname has to serve as one there.
CREATE TABLE player_identity (
    player_id     uuid         NOT NULL REFERENCES player (id) ON DELETE CASCADE,
    source        varchar(32)  NOT NULL,
    external_id   varchar(200) NOT NULL,
    external_name varchar(200) NOT NULL,
    confidence    varchar(32)  NOT NULL,
    matched_at    timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, source),
    UNIQUE (source, external_id)
);

-- Who is on a team right now, from the match provider's own roster. Replaced
-- as a set on every sync: a player who left is not on it any more, and a row
-- nothing overwrote is not the same as a row that is true (the rule
-- SaveRanking already follows for team_ranking_player).
CREATE TABLE team_roster (
    team_id   uuid     NOT NULL REFERENCES team (id) ON DELETE CASCADE,
    player_id uuid     NOT NULL REFERENCES player (id) ON DELETE CASCADE,
    position  smallint NOT NULL DEFAULT 0,
    role      varchar(64) NOT NULL DEFAULT '',
    seen_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, player_id)
);

CREATE INDEX team_roster_player_idx ON team_roster (player_id);

-- +goose Down
DROP TABLE IF EXISTS team_roster;
DROP TABLE IF EXISTS player_identity;
DROP TABLE IF EXISTS player;
