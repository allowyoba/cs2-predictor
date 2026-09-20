-- +goose Up
-- Who may open the Mini App.
--
-- Off for everybody by default: the app reads a person's whole prediction
-- history, and a surface like that opens to nobody until somebody decides
-- otherwise. Root operators are exempt in code rather than by a row here —
-- they are the ones who grant it, and a bootstrap that needs itself
-- granted is a bootstrap that cannot start.
CREATE TABLE miniapp_access (
    user_id      bigint PRIMARY KEY REFERENCES telegram_user(id) ON DELETE CASCADE,
    status       text NOT NULL CHECK (status IN ('PENDING', 'GRANTED', 'DENIED')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    -- Who decided, for the audit trail: this is an access grant, and an
    -- access grant nobody signed is one nobody can review later.
    decided_by   bigint REFERENCES telegram_user(id)
);

CREATE INDEX miniapp_access_pending_idx ON miniapp_access (requested_at) WHERE status = 'PENDING';

-- +goose Down
DROP TABLE IF EXISTS miniapp_access;
