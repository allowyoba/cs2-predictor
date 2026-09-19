-- +goose Up
-- The Ideas channel: one row per attempt, accepted or not.
--
-- Rejections are stored deliberately. The rate limits are computed from
-- attempts rather than from accepted messages, because a limiter that only
-- counted successes would be defeated by failing on purpose — send junk
-- forever, never hit the quota.
CREATE TABLE feature_suggestion (
    id          uuid PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES telegram_user(id) ON DELETE CASCADE,
    outcome     text NOT NULL,
    -- Populated only for an accepted suggestion; a rejected attempt keeps
    -- no copy of what was sent, which is both less to store and less to
    -- leak.
    body        text,
    fingerprint text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The two reads this table serves: "what has this person tried lately"
-- (rate limits) and "have they sent this exact idea before" (duplicates).
CREATE INDEX feature_suggestion_user_time_idx ON feature_suggestion (user_id, created_at DESC);
CREATE INDEX feature_suggestion_fingerprint_idx ON feature_suggestion (user_id, fingerprint, created_at DESC)
    WHERE fingerprint IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS feature_suggestion;
