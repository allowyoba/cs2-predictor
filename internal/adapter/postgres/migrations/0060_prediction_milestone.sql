-- +goose Up
-- Which exact-score milestones somebody has already been congratulated for.
--
-- The claim, not the count: the count is derivable from score_award at any
-- time, but "have we already said this" is not, and a settlement that is
-- retried (or a result that is corrected and re-settled) would otherwise
-- congratulate the same person for the same hundredth prediction again.
--
-- Scoped per chat because the congratulation is: it is that room's shared
-- story, and the same person's count in another chat is a different one. The
-- personal cabinet adds them up across chats for its own shelf.
CREATE TABLE prediction_milestone (
    chat_id    bigint      NOT NULL,
    user_id    bigint      NOT NULL,
    milestone  integer     NOT NULL CHECK (milestone > 0),
    reached_at timestamptz NOT NULL,
    PRIMARY KEY (chat_id, user_id, milestone)
);

-- The cabinet reads one person's shelf across every chat.
CREATE INDEX prediction_milestone_user_idx ON prediction_milestone (user_id, reached_at DESC);

-- +goose Down
DROP TABLE IF EXISTS prediction_milestone;
