-- +goose Up
-- A pending unsubscribe request awaiting another manager's DM confirmation
-- — the DM-era replacement for the old "second person sees the same group
-- message" safeguard. resolved_at is set (to any value) once confirmed or
-- rejected, so a second tap on a fanned-out button is a no-op; a request
-- nobody resolves in time is simply left alone here (no reaper job) and
-- treated as invalid by the domain's own Expired() check at read time.
CREATE TABLE pending_unsubscribe (
    id                  uuid PRIMARY KEY,
    chat_id             bigint NOT NULL REFERENCES telegram_chat(id) ON DELETE CASCADE,
    event_id            uuid NOT NULL REFERENCES tournament_event(id) ON DELETE CASCADE,
    requested_by        bigint NOT NULL REFERENCES telegram_user(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    resolved_at         timestamptz,
    -- true when no other manager needed to be (or could be) asked, so the
    -- requester's own confirmation is sufficient; see the domain type's doc
    -- comment on PendingUnsubscribe.SelfConfirmable.
    self_confirmable    boolean NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS pending_unsubscribe;
