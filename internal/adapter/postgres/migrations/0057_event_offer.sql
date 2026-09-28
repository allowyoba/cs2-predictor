-- +goose Up
-- Which chats have already been told about which tournament, so the
-- "a new top-tier tournament showed up" offer can be reconciled instead of
-- fired once and forgotten.
--
-- Until now the offer (and the auto-subscription that replaces it) happened
-- in exactly one place: the instant an event row was first inserted. That
-- made it unreachable whenever the tier arrived on a later sync — which is
-- the normal case, PandaScore frequently reports no tier at first and the
-- adapter merges a better one in later — or the chat turned the switch on
-- afterwards, or the chat was added afterwards. With a record of what has
-- been decided, the same decision can be re-asked on a schedule and still
-- only ever produce one message per (chat, tournament).
CREATE TABLE event_offer (
    chat_id    bigint      NOT NULL,
    event_id   uuid        NOT NULL REFERENCES tournament_event (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('OFFERED', 'AUTO_SUBSCRIBED')),
    decided_at timestamptz NOT NULL,
    PRIMARY KEY (chat_id, event_id)
);

-- A tournament a chat is already subscribed to is settled business: it must
-- not be offered retroactively just because this table is new. Everything
-- else stays absent on purpose, so the tournaments that were missed while
-- the offer was one-shot do still reach their chats — a few per run, see
-- app.EventOfferReconciler's per-run cap.
INSERT INTO event_offer (chat_id, event_id, kind, decided_at)
SELECT chat_id, event_id, 'AUTO_SUBSCRIBED', subscribed_at
  FROM event_subscription
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS event_offer;
