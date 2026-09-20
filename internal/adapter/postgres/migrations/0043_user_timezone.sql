-- +goose Up
-- Every time shown in a private chat — a bet's date, a reminder, a recap —
-- used to be rendered in the timezone of some group the person happens to
-- be in. For anybody not living where their group's chat is set, that is
-- quietly the wrong number on every screen.
--
-- NULL means "never chosen": those people keep following their chat's
-- zone, which is what they see today.
ALTER TABLE telegram_user ADD COLUMN timezone varchar(64);

-- +goose Down
ALTER TABLE telegram_user DROP COLUMN IF EXISTS timezone;
