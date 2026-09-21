-- +goose Up
-- Settings that belong to the deployment rather than to a chat or a person.
--
-- The crest source is the first of them. It was a chat setting, where
-- nothing read it; then a personal one, which was closer — crests are only
-- ever drawn in the Mini App — but still wrong: which pictures this bot
-- shows is a decision about the product, not a matter of taste, and having
-- forty people answer it forty ways only means the same team looks
-- different depending on who is looking.
--
-- Key/value rather than a column per setting: these are rare, unrelated,
-- and read one at a time. A table that grows a column per answer is a
-- migration per answer.
CREATE TABLE bot_setting (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Who set it, for the same reason miniapp_access records who decided:
    -- a deployment-wide switch nobody signed is one nobody can review.
    updated_by bigint
);

-- Carry the operators' own choice across, so a preference somebody
-- expressed today is not dropped by moving where it lives. Any operator
-- having asked for HLTV is enough: they are the ones who would set it now.
INSERT INTO bot_setting (key, value)
SELECT 'crest_source', 'hltv'
 WHERE EXISTS (SELECT 1 FROM telegram_user WHERE prefer_hltv_logos);

ALTER TABLE telegram_user DROP COLUMN prefer_hltv_logos;

-- +goose Down
ALTER TABLE telegram_user ADD COLUMN prefer_hltv_logos boolean NOT NULL DEFAULT false;
DROP TABLE IF EXISTS bot_setting;
