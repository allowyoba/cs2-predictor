-- +goose Up
CREATE TABLE team_ranking (
    team_id         uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    source          varchar(32) NOT NULL,
    global_rank     integer,
    regional_rank   integer,
    region          varchar(32),
    points          integer,
    roster          text[],
    published_at    timestamptz NOT NULL,
    fetched_at      timestamptz NOT NULL DEFAULT now(),
    raw_payload     jsonb,
    PRIMARY KEY (team_id, source)
);

-- +goose Down
DROP TABLE IF EXISTS team_ranking;
