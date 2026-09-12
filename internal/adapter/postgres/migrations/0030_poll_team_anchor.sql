-- +goose Up
-- Anchors which physical team a poll's option scores' "first"/"second"
-- refer to, captured once at poll creation. PandaScore's own opponents
-- order is not guaranteed stable across two fetches of the same match, so
-- esport_match's own first/second team assignment can drift between a
-- poll's creation and its settlement — nullable because existing polls
-- have no recorded anchor and must keep scoring exactly as before.
ALTER TABLE match_poll ADD COLUMN first_team_id uuid REFERENCES team(id);
ALTER TABLE match_poll ADD COLUMN second_team_id uuid REFERENCES team(id);

-- +goose Down
ALTER TABLE match_poll DROP COLUMN first_team_id;
ALTER TABLE match_poll DROP COLUMN second_team_id;
