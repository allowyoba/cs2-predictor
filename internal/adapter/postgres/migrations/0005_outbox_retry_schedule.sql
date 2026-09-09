-- +goose Up
ALTER TABLE outbox_event ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();
DROP INDEX idx_outbox_pending;
CREATE INDEX idx_outbox_pending ON outbox_event(next_attempt_at, occurred_at) WHERE published_at IS NULL;

-- +goose Down
-- (forward-only: no rollback provided)
