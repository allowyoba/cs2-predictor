-- +goose Up
-- Which crest a chat's surfaces show for Counter-Strike teams: the match
-- provider's (default, and the only one that covers every game) or HLTV's.
--
-- A preference rather than a data decision: both URLs are stored, so this
-- switches what is rendered and never what was collected.
ALTER TABLE telegram_chat ADD COLUMN prefer_hltv_logos boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE telegram_chat DROP COLUMN IF EXISTS prefer_hltv_logos;
