-- +goose Up
-- How bright a crest is, so the chip behind it can be chosen to suit.
--
-- There is no one background that shows every team logo well. Esports
-- marks come in two kinds: white wordmarks drawn for dark backgrounds, and
-- black ones drawn for light. Put them all on a light chip and the white
-- ones vanish; put them all on a dark chip and the black ones do. A middle
-- grey does not split the difference, it fails both.
--
-- The bot already has the bytes — it mirrors every crest — so it measures
-- each one once, at fetch time, and records whether the mark is light or
-- dark. The app then draws a dark chip behind a light mark and a light
-- chip behind a dark one, which is the only answer that works for both.
--
-- NULL means "not measured": a format we cannot decode, or a crest stored
-- before this existed. Those keep the previous behaviour rather than being
-- guessed at.
ALTER TABLE team_logo_cache ADD COLUMN is_light boolean;

-- +goose Down
ALTER TABLE team_logo_cache DROP COLUMN IF EXISTS is_light;
