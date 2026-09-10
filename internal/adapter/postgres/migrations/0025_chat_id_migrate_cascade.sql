-- +goose Up
-- Telegram permanently renumbers a group's chat id when it's upgraded to a
-- supergroup (a `migrate_to_chat_id` service message announces the new
-- id) — a very common, ordinary event, not something the group's admins
-- trigger deliberately. Every foreign key referencing telegram_chat(id)
-- (or transitively, chat_moderator's composite key) needs ON UPDATE CASCADE
-- so a single `UPDATE telegram_chat SET id = new WHERE id = old` renames
-- the chat everywhere at once, instead of the bot creating a second,
-- parallel telegram_chat row under the new id — which is what silently
-- happened before this migration, and is why the same group could appear
-- twice in the "chats you manage" list.
ALTER TABLE chat_moderator DROP CONSTRAINT chat_moderator_chat_id_fkey;
ALTER TABLE chat_moderator ADD CONSTRAINT chat_moderator_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE chat_moderator_permission DROP CONSTRAINT chat_moderator_permission_chat_id_user_id_fkey;
ALTER TABLE chat_moderator_permission ADD CONSTRAINT chat_moderator_permission_chat_id_user_id_fkey
    FOREIGN KEY (chat_id, user_id) REFERENCES chat_moderator(chat_id, user_id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE event_subscription DROP CONSTRAINT event_subscription_chat_id_fkey;
ALTER TABLE event_subscription ADD CONSTRAINT event_subscription_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE event_topic DROP CONSTRAINT event_topic_chat_id_fkey;
ALTER TABLE event_topic ADD CONSTRAINT event_topic_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE match_poll DROP CONSTRAINT match_poll_chat_id_fkey;
ALTER TABLE match_poll ADD CONSTRAINT match_poll_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE score_award DROP CONSTRAINT score_award_chat_id_fkey;
ALTER TABLE score_award ADD CONSTRAINT score_award_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE event_medal DROP CONSTRAINT event_medal_chat_id_fkey;
ALTER TABLE event_medal ADD CONSTRAINT event_medal_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE event_chat_completion DROP CONSTRAINT event_chat_completion_chat_id_fkey;
ALTER TABLE event_chat_completion ADD CONSTRAINT event_chat_completion_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE scheduled_report DROP CONSTRAINT scheduled_report_chat_id_fkey;
ALTER TABLE scheduled_report ADD CONSTRAINT scheduled_report_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE chat_manager_seen DROP CONSTRAINT chat_manager_seen_chat_id_fkey;
ALTER TABLE chat_manager_seen ADD CONSTRAINT chat_manager_seen_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE dm_admin_session DROP CONSTRAINT dm_admin_session_chat_id_fkey;
ALTER TABLE dm_admin_session ADD CONSTRAINT dm_admin_session_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE pending_unsubscribe DROP CONSTRAINT pending_unsubscribe_chat_id_fkey;
ALTER TABLE pending_unsubscribe ADD CONSTRAINT pending_unsubscribe_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE admin_action_log DROP CONSTRAINT admin_action_log_chat_id_fkey;
ALTER TABLE admin_action_log ADD CONSTRAINT admin_action_log_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE moderator_invitation DROP CONSTRAINT moderator_invitation_chat_id_fkey;
ALTER TABLE moderator_invitation ADD CONSTRAINT moderator_invitation_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON UPDATE CASCADE ON DELETE CASCADE;

-- +goose Down
ALTER TABLE moderator_invitation DROP CONSTRAINT moderator_invitation_chat_id_fkey;
ALTER TABLE moderator_invitation ADD CONSTRAINT moderator_invitation_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE admin_action_log DROP CONSTRAINT admin_action_log_chat_id_fkey;
ALTER TABLE admin_action_log ADD CONSTRAINT admin_action_log_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE pending_unsubscribe DROP CONSTRAINT pending_unsubscribe_chat_id_fkey;
ALTER TABLE pending_unsubscribe ADD CONSTRAINT pending_unsubscribe_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE dm_admin_session DROP CONSTRAINT dm_admin_session_chat_id_fkey;
ALTER TABLE dm_admin_session ADD CONSTRAINT dm_admin_session_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE chat_manager_seen DROP CONSTRAINT chat_manager_seen_chat_id_fkey;
ALTER TABLE chat_manager_seen ADD CONSTRAINT chat_manager_seen_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE scheduled_report DROP CONSTRAINT scheduled_report_chat_id_fkey;
ALTER TABLE scheduled_report ADD CONSTRAINT scheduled_report_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE event_chat_completion DROP CONSTRAINT event_chat_completion_chat_id_fkey;
ALTER TABLE event_chat_completion ADD CONSTRAINT event_chat_completion_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE event_medal DROP CONSTRAINT event_medal_chat_id_fkey;
ALTER TABLE event_medal ADD CONSTRAINT event_medal_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE score_award DROP CONSTRAINT score_award_chat_id_fkey;
ALTER TABLE score_award ADD CONSTRAINT score_award_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE match_poll DROP CONSTRAINT match_poll_chat_id_fkey;
ALTER TABLE match_poll ADD CONSTRAINT match_poll_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE event_topic DROP CONSTRAINT event_topic_chat_id_fkey;
ALTER TABLE event_topic ADD CONSTRAINT event_topic_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE event_subscription DROP CONSTRAINT event_subscription_chat_id_fkey;
ALTER TABLE event_subscription ADD CONSTRAINT event_subscription_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;

ALTER TABLE chat_moderator_permission DROP CONSTRAINT chat_moderator_permission_chat_id_user_id_fkey;
ALTER TABLE chat_moderator_permission ADD CONSTRAINT chat_moderator_permission_chat_id_user_id_fkey
    FOREIGN KEY (chat_id, user_id) REFERENCES chat_moderator(chat_id, user_id) ON DELETE CASCADE;

ALTER TABLE chat_moderator DROP CONSTRAINT chat_moderator_chat_id_fkey;
ALTER TABLE chat_moderator ADD CONSTRAINT chat_moderator_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES telegram_chat(id) ON DELETE CASCADE;
