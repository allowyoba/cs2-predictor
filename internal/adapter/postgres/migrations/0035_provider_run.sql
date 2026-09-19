-- +goose Up
-- One in-flight remote fetch per (provider, key) — an Apify actor run we
-- started and will collect on a later tick. Keeping it here, rather than in
-- memory, is what stops a restart (or a timed-out HTTP call) from starting
-- a second paid run for work already under way.
CREATE TABLE provider_run (
    provider     varchar(32)  NOT NULL,
    key          varchar(64)  NOT NULL,
    run_id       text         NOT NULL,
    status       varchar(16)  NOT NULL,
    attempts     integer      NOT NULL DEFAULT 0,
    period_start timestamptz  NOT NULL,
    started_at   timestamptz  NOT NULL,
    updated_at   timestamptz  NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, key)
);

-- +goose Down
DROP TABLE IF EXISTS provider_run;
