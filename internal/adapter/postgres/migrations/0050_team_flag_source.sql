-- +goose Up
-- Where a Counter-Strike team's flag comes from.
--
-- PandaScore reports a country on its team objects, and it is often the
-- organisation's country of registration rather than the roster's — an org
-- headquartered in one place fielding five players from another gets a
-- flag that matches neither the players nor how anybody talks about them.
-- HLTV decides a team's country from its roster, which is what a flag
-- beside a team name is actually meant to say.
--
-- Stored beside the provider's own, never over it: both are kept and the
-- chat chooses which is rendered, exactly like hltv_logo_url.
ALTER TABLE team ADD COLUMN hltv_location text;

-- Default false — the match provider covers every game and every team,
-- while HLTV's ranking covers thirty Counter-Strike teams. A chat that
-- wants the better flags for the teams HLTV knows asks for them.
ALTER TABLE telegram_chat ADD COLUMN prefer_hltv_flags boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE team DROP COLUMN IF EXISTS hltv_location;
ALTER TABLE telegram_chat DROP COLUMN IF EXISTS prefer_hltv_flags;
