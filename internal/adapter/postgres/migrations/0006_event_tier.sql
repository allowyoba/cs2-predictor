-- +goose Up
ALTER TABLE tournament_event ADD COLUMN tier text NULL;

-- +goose Down
-- (forward-only: no rollback provided)
