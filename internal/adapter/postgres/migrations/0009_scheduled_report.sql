-- +goose Up
CREATE TABLE scheduled_report (
    chat_id         bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    report_type     varchar(32) NOT NULL,
    period_key      varchar(16) NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, report_type, period_key)
);

CREATE INDEX idx_scheduled_report_created ON scheduled_report(created_at);

-- +goose Down
DROP TABLE IF EXISTS scheduled_report;
