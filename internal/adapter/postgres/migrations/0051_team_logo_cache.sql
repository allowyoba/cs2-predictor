-- +goose Up
-- A local copy of every team crest, so no viewer's browser ever talks to
-- somebody else's CDN.
--
-- Until now the app handed out HLTV's and PandaScore's own image URLs and
--every browser fetched them directly: one request per crest, per screen, per
-- person, all of it landing on a third party who never agreed to serve our
-- users. It is not a load they would notice at this size, but it is their
-- bandwidth spent on our traffic, it leaks every viewer's IP to them, and
-- HLTV's URLs are signed and will eventually stop resolving.
--
-- Now the bot fetches each crest once, keeps the bytes here, and serves
-- them from its own origin. Fetching is deliberately slow and serial —
-- see app.LogoMirror.
--
-- The bytes live in the database rather than on disk because this
-- deployment has one machine, one backup story and no object store: a
-- 2 KB PNG per team is a rounding error next to the outbox, and a file
-- tree would be one more thing to back up and restore correctly.
CREATE TABLE team_logo_cache (
    team_id      uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    source       text NOT NULL,
    -- source_url is what was fetched, so a rebrand (a new URL for the same
    -- team) is noticed without re-downloading anything that has not moved.
    source_url   text NOT NULL,
    content_type text NOT NULL,
    bytes        bytea NOT NULL,
    -- digest is the content hash, used as the cache-busting token in the
    -- URL the app renders. It lets the image be served immutable: a new
    -- crest is a new URL, so nothing has to guess at expiry times.
    digest       text NOT NULL,
    -- etag/last_modified are the CDN's own answers, kept so a later check
    -- can be conditional: "has this changed?" costs a 304 and no bytes,
    -- where a plain re-download costs the whole image every time.
    etag          text,
    last_modified text,
    fetched_at    timestamptz NOT NULL DEFAULT now(),
    -- checked_at is when we last asked, whether or not anything came back.
    -- Separate from fetched_at so a run of unchanged answers does not read
    -- as a crest that keeps being re-downloaded.
    checked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source)
);

-- The revalidation query looks for the least recently checked crests among
-- teams about to play.
CREATE INDEX team_logo_cache_checked_idx ON team_logo_cache (checked_at);

-- +goose Down
DROP TABLE IF EXISTS team_logo_cache;
