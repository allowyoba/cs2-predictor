-- +goose Up
-- Partitions outbox_event by RANGE(occurred_at), one partition per UTC day
-- (see outbox_partitions.go and retention.go's maintainOutboxPartitions) —
-- dropping an old empty partition reclaims space instantly, no DELETE/VACUUM
-- needed. processed_telegram_update is NOT partitioned: its dedup relies on
-- a UNIQUE constraint on update_id alone, and Postgres requires a
-- partitioned table's unique constraints to include the partition key.
--
-- PRIMARY KEY becomes (id, occurred_at) — id alone is a UUID (practically
-- unique anyway), and every UPDATE keyed on id now also carries occurred_at.
--
-- Forward-only: no rollback provided.

ALTER TABLE outbox_event RENAME TO outbox_event_pre_partition;
ALTER TABLE outbox_event_pre_partition RENAME CONSTRAINT outbox_event_pkey TO outbox_event_pre_partition_pkey;
ALTER INDEX idx_outbox_pending RENAME TO idx_outbox_pending_pre_partition;
ALTER INDEX idx_outbox_published_at RENAME TO idx_outbox_published_at_pre_partition;

CREATE TABLE outbox_event (
    id              uuid NOT NULL,
    aggregate_type  varchar(100) NOT NULL,
    aggregate_id    varchar(100) NOT NULL,
    event_type      varchar(200) NOT NULL,
    payload         jsonb NOT NULL,
    occurred_at     timestamptz NOT NULL,
    published_at    timestamptz,
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX idx_outbox_pending ON outbox_event(next_attempt_at, occurred_at) WHERE published_at IS NULL;
CREATE INDEX idx_outbox_published_at ON outbox_event(published_at) WHERE published_at IS NOT NULL;

-- Safety net: a non-empty default partition means partition maintenance
-- has fallen behind.
CREATE TABLE outbox_event_default PARTITION OF outbox_event DEFAULT;

-- One partition per day from the oldest existing row through +2 days.
-- +goose StatementBegin
DO $$
DECLARE
    start_day date;
    d         date;
BEGIN
    SELECT LEAST(COALESCE(MIN(occurred_at)::date, CURRENT_DATE), CURRENT_DATE)
      INTO start_day
      FROM outbox_event_pre_partition;

    FOR d IN SELECT generate_series(start_day, CURRENT_DATE + 2, INTERVAL '1 day')::date LOOP
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF outbox_event FOR VALUES FROM (%L) TO (%L)',
            'outbox_event_' || to_char(d, 'YYYYMMDD'), d, d + 1
        );
    END LOOP;
END $$;
-- +goose StatementEnd

INSERT INTO outbox_event SELECT * FROM outbox_event_pre_partition;

DROP TABLE outbox_event_pre_partition;

-- +goose Down
-- (forward-only: no rollback provided)
