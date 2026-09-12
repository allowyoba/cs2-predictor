-- +goose Up
-- Lets a chat opt into "every new S/A tournament joins automatically"
-- instead of only ever being offered one via the proactive
-- big-event-discovered notification (see announceBigEvent in
-- internal/app/sync.go) — off by default, same as default_top_tier_only.
ALTER TABLE telegram_chat ADD COLUMN auto_subscribe_top_tier boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE telegram_chat DROP COLUMN auto_subscribe_top_tier;
