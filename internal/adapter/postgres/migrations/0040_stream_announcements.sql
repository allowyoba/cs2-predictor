-- +goose Up
-- The "the match is starting, watch here" message a closing poll posts is
-- an extra message in the room, so it is opt-in: every chat that exists
-- today keeps its current experience, and a chat that wants the link asks
-- for it in the settings.
ALTER TABLE telegram_chat ADD COLUMN stream_announcements boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE telegram_chat DROP COLUMN stream_announcements;
