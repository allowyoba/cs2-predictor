-- +goose Up
CREATE TABLE team_external_identity (
    team_id             uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    provider            varchar(32) NOT NULL,
    external_id         varchar(200) NOT NULL,
    external_name       varchar(200) NOT NULL,
    confidence          varchar(16) NOT NULL,
    last_verified_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, external_id)
);

CREATE INDEX idx_team_external_identity_team ON team_external_identity(team_id);

CREATE TABLE team_alias (
    team_id             uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    alias               varchar(200) NOT NULL,
    normalized_alias    varchar(200) NOT NULL,
    PRIMARY KEY (team_id, normalized_alias)
);

-- +goose Down
DROP TABLE IF EXISTS team_alias;
DROP TABLE IF EXISTS team_external_identity;
