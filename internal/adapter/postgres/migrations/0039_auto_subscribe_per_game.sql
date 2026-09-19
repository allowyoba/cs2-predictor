-- +goose Up
-- Auto-subscription becomes a per-game decision: a chat can follow every
-- big CS2 tournament automatically while still choosing its Dota 2 ones by
-- hand. The flag moves onto the chat's own row for that game, and every
-- chat that had it on keeps it on for each game it already follows.
ALTER TABLE chat_enabled_game ADD COLUMN auto_subscribe_top_tier boolean NOT NULL DEFAULT false;

UPDATE chat_enabled_game ceg
SET auto_subscribe_top_tier = true
FROM telegram_chat c
WHERE c.id = ceg.chat_id AND c.auto_subscribe_top_tier;

ALTER TABLE telegram_chat DROP COLUMN auto_subscribe_top_tier;

-- +goose Down
ALTER TABLE telegram_chat ADD COLUMN auto_subscribe_top_tier boolean NOT NULL DEFAULT false;

UPDATE telegram_chat c
SET auto_subscribe_top_tier = true
WHERE EXISTS (SELECT 1 FROM chat_enabled_game ceg WHERE ceg.chat_id = c.id AND ceg.auto_subscribe_top_tier);

ALTER TABLE chat_enabled_game DROP COLUMN auto_subscribe_top_tier;
