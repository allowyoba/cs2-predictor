-- +goose Up
-- Team crests, from two sources kept side by side.
--
-- logo_url is whatever the match provider published (PandaScore), which
-- covers every game this bot follows and arrives with the match sync at no
-- extra request. hltv_logo_url is the crest HLTV publishes in its own
-- ranking: Counter-Strike only, and only for ranked teams, but it is the
-- picture the CS2 audience recognises.
--
-- Two columns rather than one with a winner: which to show is a display
-- preference (see telegram_chat.prefer_hltv_logos), and collapsing them at
-- write time would mean re-fetching to change your mind.
ALTER TABLE team
    ADD COLUMN logo_url      text,
    ADD COLUMN hltv_logo_url text;

-- +goose Down
ALTER TABLE team
    DROP COLUMN IF EXISTS logo_url,
    DROP COLUMN IF EXISTS hltv_logo_url;
