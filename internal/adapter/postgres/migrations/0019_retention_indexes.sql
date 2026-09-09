-- +goose Up
-- Indexes the retention sweep needs to delete by age without scanning the
-- whole table. Each is partial where that keeps it small: only published
-- outbox rows and resolved confirmations are ever eligible for deletion.
CREATE INDEX idx_processed_update_processed_at ON processed_telegram_update(processed_at);
CREATE INDEX idx_outbox_published_at ON outbox_event(published_at) WHERE published_at IS NOT NULL;
CREATE INDEX idx_pending_unsubscribe_resolved_at ON pending_unsubscribe(resolved_at) WHERE resolved_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_pending_unsubscribe_resolved_at;
DROP INDEX IF EXISTS idx_outbox_published_at;
DROP INDEX IF EXISTS idx_processed_update_processed_at;
