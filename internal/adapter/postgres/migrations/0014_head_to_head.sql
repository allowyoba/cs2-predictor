-- +goose Up
-- team_a_id/team_b_id are always stored with team_a_id < team_b_id (enforced
-- by the repository layer, which canonicalizes the pair before every
-- read/write) so an unordered team pair has exactly one cached row per
-- source, never two depending on lookup order.
CREATE TABLE head_to_head (
    team_a_id   uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    team_b_id   uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
    source      varchar(32) NOT NULL,
    team_a_wins integer NOT NULL,
    team_b_wins integer NOT NULL,
    sample      integer NOT NULL,
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_a_id, team_b_id, source),
    CONSTRAINT head_to_head_ordered_pair CHECK (team_a_id < team_b_id)
);

-- +goose Down
DROP TABLE IF EXISTS head_to_head;
