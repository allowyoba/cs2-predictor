-- +goose Up
-- What a name can be found by, as opposed to how it is written.
--
-- Searching for a team or a player compared the query against the stored
-- name with a plain substring test, so "спирит" never found "Team Spirit"
-- and "ропз" never found "r0pz" — which is most of the ways people actually
-- type these names. competition.SearchKeyBlob folds a name into the shape a
-- query is compared against (transliterated, digits-for-letters undone,
-- punctuation gone); this column stores that, so the test stays one indexed
-- substring match rather than a scan through the catalogue in Go.
--
-- Left empty here rather than filled with a SQL approximation of the fold:
-- two different folds would be worse than one that is briefly missing. The
-- rows are filled by app.SearchKeyBackfill at startup, and every write
-- computes it from then on — and until a row has one, the query still falls
-- back to matching its name, so nothing gets worse in the meantime.
ALTER TABLE team ADD COLUMN search_key text NOT NULL DEFAULT '';
ALTER TABLE player ADD COLUMN search_key text NOT NULL DEFAULT '';

CREATE INDEX team_search_key_idx ON team (game_id, search_key);
CREATE INDEX player_search_key_idx ON player (game_id, search_key);

-- +goose Down
DROP INDEX IF EXISTS player_search_key_idx;
DROP INDEX IF EXISTS team_search_key_idx;
ALTER TABLE player DROP COLUMN IF EXISTS search_key;
ALTER TABLE team DROP COLUMN IF EXISTS search_key;
