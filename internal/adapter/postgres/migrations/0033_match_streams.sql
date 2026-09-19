-- +goose Up
-- Caches PandaScore's streams_list for each match (raw JSON: language, url,
-- main, official) so the "upcoming matches" screen can link a broadcast
-- without an extra provider call. Nullable/absent for matches synced before
-- this column existed or with no reported broadcast at all.
ALTER TABLE esport_match ADD COLUMN streams jsonb;

-- +goose Down
ALTER TABLE esport_match DROP COLUMN streams;
