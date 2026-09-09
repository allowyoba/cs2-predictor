-- +goose Up
CREATE INDEX idx_vote_user ON prediction_vote(user_id);
CREATE INDEX idx_medal_chat_place ON event_medal(chat_id, place);
CREATE INDEX idx_subscription_event_active ON event_subscription(event_id) WHERE active;

-- +goose Down
-- (forward-only: no rollback provided)
