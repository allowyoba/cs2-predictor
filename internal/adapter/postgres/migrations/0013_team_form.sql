-- +goose Up
CREATE TABLE team_form (
    team_id     uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    source      varchar(32) NOT NULL,
    wins        integer NOT NULL,
    losses      integer NOT NULL,
    sample      integer NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source)
);

-- +goose Down
DROP TABLE IF EXISTS team_form;
