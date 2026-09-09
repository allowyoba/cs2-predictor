-- +goose Up
CREATE TABLE provider_sync_state (
    provider                varchar(32) PRIMARY KEY,
    last_success_at         timestamptz,
    last_error_at           timestamptz,
    last_error              text,
    consecutive_failures    integer NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE IF EXISTS provider_sync_state;
