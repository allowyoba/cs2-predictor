-- +goose Up
-- A local copy of each game's own logo, for the same reason the team
-- crests have one: nobody's browser should be fetching images from
-- somebody else's CDN because they opened this app.
--
-- The source is the publisher's own: Valve serves the artwork for
-- Counter-Strike and Dota 2 from Steam's CDN, which is as official as a
-- game logo gets. It is fetched once, by the same slow, identified,
-- one-at-a-time job that mirrors the crests.
--
-- Keyed by the game's own code rather than by its numeric id: the code is
-- what every other table and every URL already uses.
CREATE TABLE game_logo_cache (
    game_code    text PRIMARY KEY,
    source_url   text NOT NULL,
    content_type text NOT NULL,
    bytes        bytea NOT NULL,
    digest       text NOT NULL,
    etag          text,
    last_modified text,
    -- Whether the mark itself is light, so the chip behind it can suit —
    -- the same measurement the crests get.
    is_light   boolean,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    checked_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS game_logo_cache;
