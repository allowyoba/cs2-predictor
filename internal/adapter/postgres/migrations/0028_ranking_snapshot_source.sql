-- +goose Up
-- valve_vrs_snapshot is renamed and widened to hold more than one ranking
-- feed (HLTV added alongside Valve VRS): a bare normalized_name primary key
-- would let two different feeds' entries for a similarly-named team
-- silently overwrite each other, which is exactly the collision the new
-- app.RankingSync (generalized from the Valve-only sync job) must not risk.
ALTER TABLE valve_vrs_snapshot RENAME TO ranking_snapshot;
ALTER TABLE ranking_snapshot ADD COLUMN source varchar(32) NOT NULL DEFAULT 'VALVE_VRS';
ALTER TABLE ranking_snapshot ALTER COLUMN source DROP DEFAULT;
ALTER TABLE ranking_snapshot DROP CONSTRAINT valve_vrs_snapshot_pkey;
ALTER TABLE ranking_snapshot ADD PRIMARY KEY (source, normalized_name);

-- +goose Down
-- A row per (source, normalized_name) can't losslessly collapse back to one
-- per normalized_name if more than one source ever cached the same name;
-- keeping the Valve VRS row (this table's only source before this
-- migration) on any such collision is the least surprising choice.
DELETE FROM ranking_snapshot a USING ranking_snapshot b
 WHERE a.normalized_name = b.normalized_name AND a.source <> 'VALVE_VRS' AND b.source = 'VALVE_VRS';
DELETE FROM ranking_snapshot a
 WHERE a.source <> 'VALVE_VRS'
   AND EXISTS (SELECT 1 FROM ranking_snapshot b WHERE b.normalized_name = a.normalized_name AND b.source < a.source);
ALTER TABLE ranking_snapshot DROP CONSTRAINT ranking_snapshot_pkey;
ALTER TABLE ranking_snapshot DROP COLUMN source;
ALTER TABLE ranking_snapshot ADD PRIMARY KEY (normalized_name);
ALTER TABLE ranking_snapshot RENAME TO valve_vrs_snapshot;
