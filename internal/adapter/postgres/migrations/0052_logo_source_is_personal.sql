-- +goose Up
-- The crest source belongs to the person, not to the chat.
--
-- It was a chat setting, and it was a control that did nothing: crests are
-- only ever rendered in the Mini App, which one person opens in their own
-- private chat with the bot. Nothing read the chat's value — the app asked
-- for the default every time — so a manager could flip it and watch
-- nothing change, in any chat, ever.
--
-- Flags are the opposite and stay on the chat: they are rendered in the
-- poll and in the schedule, in the room, for everybody at once.
ALTER TABLE telegram_user ADD COLUMN prefer_hltv_logos boolean NOT NULL DEFAULT false;

-- Carried across for the few chats that had set it, keyed to the people
-- who manage them, so a preference somebody expressed is not silently
-- dropped just because it was being kept in the wrong place.
INSERT INTO telegram_user (id, display_name, prefer_hltv_logos)
SELECT DISTINCT m.user_id, '', true
  FROM chat_manager_seen m
  JOIN telegram_chat c ON c.id = m.chat_id
 WHERE c.prefer_hltv_logos
ON CONFLICT (id) DO UPDATE SET prefer_hltv_logos = true;

ALTER TABLE telegram_chat DROP COLUMN prefer_hltv_logos;

-- +goose Down
ALTER TABLE telegram_chat ADD COLUMN prefer_hltv_logos boolean NOT NULL DEFAULT false;
ALTER TABLE telegram_user DROP COLUMN prefer_hltv_logos;
