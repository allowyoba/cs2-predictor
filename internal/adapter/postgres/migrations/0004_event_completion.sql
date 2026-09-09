-- +goose Up
CREATE TABLE event_chat_completion (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id        uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    result_hash     varchar(1000) NOT NULL,
    completed_at    timestamptz NOT NULL,
    PRIMARY KEY (chat_id, event_id)
);

-- +goose Down
-- (forward-only: no rollback provided)
