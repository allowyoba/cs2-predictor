-- +goose Up
-- Generalizes the two-manager confirmation from "unsubscribe" to any
-- action that needs a second pair of eyes: disabling a game is the second
-- one, and it has no event to point at. The old rows keep working — they
-- are all unsubscribes, which is exactly what the default says.
ALTER TABLE pending_unsubscribe RENAME TO pending_approval;
ALTER TABLE pending_approval ALTER COLUMN event_id DROP NOT NULL;
ALTER TABLE pending_approval ADD COLUMN kind varchar(32) NOT NULL DEFAULT 'unsubscribe';
-- What the action is about when it is not an event: the game code for a
-- game being switched off, say.
ALTER TABLE pending_approval ADD COLUMN subject varchar(64) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE pending_approval DROP COLUMN subject;
ALTER TABLE pending_approval DROP COLUMN kind;
DELETE FROM pending_approval WHERE event_id IS NULL;
ALTER TABLE pending_approval ALTER COLUMN event_id SET NOT NULL;
ALTER TABLE pending_approval RENAME TO pending_unsubscribe;
