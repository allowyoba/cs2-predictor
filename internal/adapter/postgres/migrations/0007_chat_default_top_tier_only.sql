-- +goose Up
ALTER TABLE telegram_chat ADD COLUMN default_top_tier_only boolean NOT NULL DEFAULT false;

-- +goose Down
-- (forward-only: no rollback provided)
