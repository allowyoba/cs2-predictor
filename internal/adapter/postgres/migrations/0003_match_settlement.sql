-- +goose Up
CREATE TABLE match_settlement (
    poll_id         uuid PRIMARY KEY REFERENCES match_poll(id) ON DELETE CASCADE,
    result_hash     varchar(100) NOT NULL,
    settled_at      timestamptz NOT NULL
);

-- +goose Down
-- (forward-only: no rollback provided)
