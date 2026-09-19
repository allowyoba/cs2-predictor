-- +goose Up
-- The broadcast link a poll was published with, so closing it can tell
-- whether one has appeared since. Providers regularly list a match's
-- streams only hours after the match itself, which is after the poll has
-- already gone out — recording what was shown is what makes "and now there
-- is one" answerable without messaging a chat twice about the same link.
ALTER TABLE match_poll ADD COLUMN stream_url text;

-- +goose Down
ALTER TABLE match_poll DROP COLUMN stream_url;
