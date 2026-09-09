-- +goose Up
CREATE TABLE tournament_metadata (
    event_id    uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    source      varchar(32) NOT NULL,
    full_name   text,
    series      text,
    region      text,
    stage       text,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, source)
);

-- +goose Down
DROP TABLE IF EXISTS tournament_metadata;
