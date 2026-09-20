package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// ChatRepository implements chat.Repository against telegram_chat,
// telegram_user, chat_moderator, and event_topic.
type ChatRepository struct {
	pool *pgxpool.Pool
}

func NewChatRepository(pool *pgxpool.Pool) *ChatRepository {
	return &ChatRepository{pool: pool}
}

func (r *ChatRepository) Find(ctx context.Context, chatID common.ChatID) (*chat.Settings, error) {
	row := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT id, title, locale, timezone, default_topic_id, active, default_top_tier_only, stream_announcements, stream_language FROM telegram_chat WHERE id = $1`, chatID.Value)
	var s chat.Settings
	var id int64
	if err := row.Scan(&id, &s.Title, &s.Locale, &s.Timezone, &s.DefaultTopicID, &s.Active, &s.DefaultTopTierOnly, &s.StreamAnnouncements, &s.StreamLanguage); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	s.ChatID = common.ChatID{Value: id}
	games, autoSubscribed, err := r.enabledGames(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	s.EnabledGames, s.AutoSubscribeGames = games[id], autoSubscribed[id]
	return &s, nil
}

func (r *ChatRepository) ListActive(ctx context.Context) ([]chat.Settings, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id, title, locale, timezone, default_topic_id, active, default_top_tier_only, stream_announcements, stream_language
		   FROM telegram_chat
		  WHERE active = true
		  ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []chat.Settings
	var ids []int64
	for rows.Next() {
		var s chat.Settings
		if err := rows.Scan(&s.ChatID.Value, &s.Title, &s.Locale, &s.Timezone, &s.DefaultTopicID, &s.Active, &s.DefaultTopTierOnly, &s.StreamAnnouncements, &s.StreamLanguage); err != nil {
			return nil, err
		}
		out = append(out, s)
		ids = append(ids, s.ChatID.Value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	games, autoSubscribed, err := r.enabledGames(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].EnabledGames = games[out[i].ChatID.Value]
		out[i].AutoSubscribeGames = autoSubscribed[out[i].ChatID.Value]
	}
	return out, nil
}

// enabledGames batch-resolves each chat id's enabled games in one round
// trip — the same batching pattern providerCodes/gameCodes use in
// competition.go, for the same reason: avoids an N+1 query per chat.
func (r *ChatRepository) enabledGames(ctx context.Context, chatIDs []int64) (games, autoSubscribed map[int64][]competition.GameCode, err error) {
	games, autoSubscribed = map[int64][]competition.GameCode{}, map[int64][]competition.GameCode{}
	if len(chatIDs) == 0 {
		return games, autoSubscribed, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT ceg.chat_id, g.code, ceg.auto_subscribe_top_tier
		 FROM chat_enabled_game ceg JOIN game g ON g.id = ceg.game_id WHERE ceg.chat_id = ANY($1)`,
		chatIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chatID int64
		var code string
		var auto bool
		if err := rows.Scan(&chatID, &code, &auto); err != nil {
			return nil, nil, err
		}
		games[chatID] = append(games[chatID], competition.GameCode(code))
		if auto {
			autoSubscribed[chatID] = append(autoSubscribed[chatID], competition.GameCode(code))
		}
	}
	return games, autoSubscribed, rows.Err()
}

// SetAutoSubscribeGame flips the flag on the chat's row for that game. A
// game the chat does not follow has no row to flip, and silently gains
// nothing — enabling it later starts from the default, off.
func (r *ChatRepository) SetAutoSubscribeGame(ctx context.Context, chatID common.ChatID, game competition.GameCode, enabled bool) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE chat_enabled_game SET auto_subscribe_top_tier = $3
		 WHERE chat_id = $1 AND game_id = (SELECT id FROM game WHERE code = $2)`,
		chatID.Value, string(game), enabled)
	return err
}

// SetEnabledGames replaces the chat's whole set in one transaction — small,
// fixed-cardinality (currently at most len(competition.Games)), so a
// straight delete-then-insert-each needs no batching.
func (r *ChatRepository) SetEnabledGames(ctx context.Context, chatID common.ChatID, games []competition.GameCode) error {
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		ex := executor(ctx, r.pool)
		if _, err := ex.Exec(ctx, `DELETE FROM chat_enabled_game WHERE chat_id = $1`, chatID.Value); err != nil {
			return err
		}
		for _, g := range games {
			if _, err := ex.Exec(ctx,
				`INSERT INTO chat_enabled_game(chat_id, game_id) SELECT $1, id FROM game WHERE code = $2`,
				chatID.Value, string(g)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Save upserts by primary key, preserving created_at from an existing row —
// same "look up old row, keep its created_at, write everything else" pattern
// used by every upsert in the original JPA adapters.
func (r *ChatRepository) Save(ctx context.Context, s chat.Settings) (chat.Settings, error) {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO telegram_chat(id, title, locale, timezone, default_topic_id, active, default_top_tier_only, stream_announcements, stream_language, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())
		 ON CONFLICT (id) DO UPDATE SET
		   title = excluded.title, locale = excluded.locale, timezone = excluded.timezone,
		   default_topic_id = excluded.default_topic_id, active = excluded.active,
		   default_top_tier_only = excluded.default_top_tier_only,
		   stream_announcements = excluded.stream_announcements,
		   stream_language = excluded.stream_language, updated_at = now()`,
		s.ChatID.Value, s.Title, s.Locale, s.Timezone, s.DefaultTopicID, s.Active, s.DefaultTopTierOnly,
		s.StreamAnnouncements, s.StreamLanguage)
	return s, err
}

// MigrateChatID renames a chat's primary key everywhere at once via the ON
// UPDATE CASCADE foreign keys added in migration 0025. If newID already has
// its own row — a race where the bot was somehow contacted under the new
// id before the migration service message arrived — that row is already
// the source of truth, so oldID's (now-orphaned) row is deleted instead of
// overwriting it.
func (r *ChatRepository) MigrateChatID(ctx context.Context, oldID, newID common.ChatID) error {
	if oldID == newID {
		return nil
	}
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		ex := executor(ctx, r.pool)
		var newExists bool
		if err := ex.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_chat WHERE id = $1)`, newID.Value).Scan(&newExists); err != nil {
			return err
		}
		if newExists {
			_, err := ex.Exec(ctx, `DELETE FROM telegram_chat WHERE id = $1`, oldID.Value)
			return err
		}
		_, err := ex.Exec(ctx, `UPDATE telegram_chat SET id = $2 WHERE id = $1`, oldID.Value, newID.Value)
		return err
	})
}

func (r *ChatRepository) IsModerator(ctx context.Context, chatID common.ChatID, userID common.UserID) (bool, error) {
	var exists bool
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM chat_moderator WHERE chat_id = $1 AND user_id = $2)`,
		chatID.Value, userID.Value).Scan(&exists)
	return exists, err
}

// ensureUser auto-creates a stub telegram_user row if missing, with a
// placeholder display name ("Telegram user <id>"), so any foreign key
// referencing telegram_user resolves regardless of which repository writes
// first — chat_moderator (via ChatRepository) and moderator_invitation (via
// InvitationRepository.created_by) both depend on this.
func (r *ChatRepository) ensureUser(ctx context.Context, id common.UserID) error {
	return ensureUser(ctx, executor(ctx, r.pool), id)
}

func ensureUser(ctx context.Context, ex dbtx, id common.UserID) error {
	_, err := ex.Exec(ctx,
		`INSERT INTO telegram_user(id, display_name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		id.Value, "Telegram user "+id.String())
	return err
}

func (r *ChatRepository) AddModerator(ctx context.Context, m chat.Moderator) error {
	if err := r.ensureUser(ctx, m.UserID); err != nil {
		return err
	}
	if err := r.ensureUser(ctx, m.AppointedBy); err != nil {
		return err
	}
	if m.DisplayName != "" || m.Username != "" {
		_, err := executor(ctx, r.pool).Exec(ctx, `UPDATE telegram_user SET display_name = CASE WHEN $2 <> '' THEN $2 ELSE display_name END, username = NULLIF($3, ''), updated_at = now() WHERE id = $1`, m.UserID.Value, m.DisplayName, m.Username)
		if err != nil {
			return err
		}
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO chat_moderator(chat_id, user_id, appointed_by, appointed_at) VALUES ($1, $2, $3, now())
		 ON CONFLICT (chat_id, user_id) DO UPDATE SET appointed_by = excluded.appointed_by, appointed_at = now()`,
		m.ChatID.Value, m.UserID.Value, m.AppointedBy.Value)
	if err != nil {
		return err
	}
	return r.SetModeratorPermissions(ctx, m.ChatID, m.UserID, m.Permissions)
}

func (r *ChatRepository) RemoveModerator(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM chat_moderator WHERE chat_id = $1 AND user_id = $2`, chatID.Value, userID.Value)
	return err
}

// toPermissions converts the raw permission slugs Postgres returns into
// domain values, silently dropping anything that isn't currently a known
// permission (a slug from a since-removed permission, say) rather than
// failing the whole read.
func toPermissions(raw []string) []chat.Permission {
	var out []chat.Permission
	for _, s := range raw {
		p := chat.Permission(s)
		if chat.ValidPermission(p) {
			out = append(out, p)
		}
	}
	return out
}

func (r *ChatRepository) ModeratorPermissions(ctx context.Context, chatID common.ChatID, userID common.UserID) ([]chat.Permission, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT permission FROM chat_moderator_permission WHERE chat_id = $1 AND user_id = $2`,
		chatID.Value, userID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		raw = append(raw, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return toPermissions(raw), nil
}

// SetModeratorPermissions replaces the full permission set inside one
// transaction — delete-then-insert rather than a diff, since the set is
// always small (at most four permissions) and the wizard always submits the
// complete target set anyway.
func (r *ChatRepository) SetModeratorPermissions(ctx context.Context, chatID common.ChatID, userID common.UserID, permissions []chat.Permission) error {
	return RunInTx(ctx, r.pool, func(ctx context.Context) error {
		if _, err := executor(ctx, r.pool).Exec(ctx,
			`DELETE FROM chat_moderator_permission WHERE chat_id = $1 AND user_id = $2`,
			chatID.Value, userID.Value); err != nil {
			return err
		}
		for _, p := range permissions {
			if _, err := executor(ctx, r.pool).Exec(ctx,
				`INSERT INTO chat_moderator_permission(chat_id, user_id, permission) VALUES ($1, $2, $3)
				 ON CONFLICT DO NOTHING`,
				chatID.Value, userID.Value, string(p)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ChatRepository) UserProfile(ctx context.Context, userID common.UserID) (*chat.UserProfile, error) {
	var profile chat.UserProfile
	var username, displayName string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT COALESCE(username, ''), display_name FROM telegram_user WHERE id = $1`, userID.Value).
		Scan(&username, &displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profile.UserID = userID
	profile.Username = username
	profile.DisplayName = displayName
	return &profile, nil
}

func (r *ChatRepository) UserProfiles(ctx context.Context, userIDs []common.UserID) (map[common.UserID]chat.UserProfile, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	values := make([]int64, len(userIDs))
	for i, id := range userIDs {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id, COALESCE(username, ''), display_name FROM telegram_user WHERE id = ANY($1)`, values)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[common.UserID]chat.UserProfile, len(userIDs))
	for rows.Next() {
		var id int64
		var profile chat.UserProfile
		if err := rows.Scan(&id, &profile.Username, &profile.DisplayName); err != nil {
			return nil, err
		}
		profile.UserID = common.UserID{Value: id}
		out[profile.UserID] = profile
	}
	return out, rows.Err()
}

func (r *ChatRepository) EventTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID) (*int64, error) {
	var topicID int64
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT topic_id FROM event_topic WHERE chat_id = $1 AND event_id = $2`, chatID.Value, eventID.Value).Scan(&topicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &topicID, nil
}

func (r *ChatRepository) SaveEventTopic(ctx context.Context, t chat.EventTopic) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO event_topic(chat_id, event_id, topic_id) VALUES ($1, $2, $3)
		 ON CONFLICT (chat_id, event_id) DO UPDATE SET topic_id = excluded.topic_id`,
		t.ChatID.Value, t.EventID.Value, t.TopicID)
	return err
}

func (r *ChatRepository) ClearEventTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM event_topic WHERE chat_id = $1 AND event_id = $2`, chatID.Value, eventID.Value)
	return err
}

var (
	_ chat.Repository       = (*ChatRepository)(nil)
	_ chat.ActiveChatLister = (*ChatRepository)(nil)
)

// Nickname and SetNickname persist a user's own chosen result-list name —
// see chat.Repository's doc comment for why it is a separate column from
// display_name rather than an override flag on it.
func (r *ChatRepository) Nickname(ctx context.Context, userID common.UserID) (*string, error) {
	var nickname *string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT nickname FROM telegram_user WHERE id = $1`, userID.Value).Scan(&nickname)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return nickname, err
}

func (r *ChatRepository) SetNickname(ctx context.Context, userID common.UserID, nickname string) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	var value *string
	if nickname != "" {
		value = &nickname
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE telegram_user SET nickname = $2, updated_at = now() WHERE id = $1`, userID.Value, value)
	return err
}

func (r *ChatRepository) UserLocale(ctx context.Context, userID common.UserID) (*common.LocaleCode, error) {
	var raw *string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT locale FROM telegram_user WHERE id = $1`, userID.Value).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || raw == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	locale := common.LocaleFrom(*raw)
	return &locale, nil
}

// UserTimezone and SetUserTimezone persist the person's own zone for
// private-chat screens — NULL means they never chose one.
func (r *ChatRepository) UserTimezone(ctx context.Context, userID common.UserID) (*string, error) {
	var zone *string
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT timezone FROM telegram_user WHERE id = $1`, userID.Value).Scan(&zone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return zone, err
}

func (r *ChatRepository) SetUserTimezone(ctx context.Context, userID common.UserID, timezone string) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	var value *string
	if timezone != "" {
		value = &timezone
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE telegram_user SET timezone = $2, updated_at = now() WHERE id = $1`, userID.Value, value)
	return err
}

func (r *ChatRepository) SetDMReachable(ctx context.Context, userID common.UserID, reachable bool) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE telegram_user SET dm_reachable = $2, updated_at = now() WHERE id = $1`, userID.Value, reachable)
	return err
}

func (r *ChatRepository) FilterDMReachable(ctx context.Context, userIDs []common.UserID) ([]common.UserID, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	values := make([]int64, len(userIDs))
	for i, id := range userIDs {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id FROM telegram_user WHERE id = ANY($1) AND dm_reachable`, values)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.UserID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.UserID{Value: id})
	}
	return out, rows.Err()
}

var _ common.NotificationAudience = (*ChatRepository)(nil)

// notificationColumn maps a notification kind to its preference column.
// Kept as an explicit allow-list rather than string interpolation of the
// kind: this value reaches SQL, and an unknown kind must be a plain error,
// never a query.
func notificationColumn(kind common.NotificationKind) (string, bool) {
	switch kind {
	case common.NotifyResultRecaps:
		return "result_recaps", true
	case common.NotifyPollReminders:
		return "poll_reminders", true
	default:
		return "", false
	}
}

// Recipients narrows candidates to those who opted into kind AND can be
// reached by DM. Both conditions matter: an opt-in from someone who has
// since blocked the bot would otherwise produce a failing send per match.
func (r *ChatRepository) Recipients(ctx context.Context, kind common.NotificationKind, candidates []common.UserID) ([]common.UserID, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	column, ok := notificationColumn(kind)
	if !ok {
		return nil, fmt.Errorf("unknown notification kind %q", kind)
	}
	values := make([]int64, len(candidates))
	for i, id := range candidates {
		values[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT id FROM telegram_user WHERE id = ANY($1) AND dm_reachable AND `+column, values)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.UserID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.UserID{Value: id})
	}
	return out, rows.Err()
}

// NotificationPrefs reads back one person's own opt-ins, for their
// settings screen.
func (r *ChatRepository) NotificationPrefs(ctx context.Context, userID common.UserID) (chat.NotificationPrefs, error) {
	var prefs chat.NotificationPrefs
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT result_recaps, poll_reminders FROM telegram_user WHERE id = $1`, userID.Value).
		Scan(&prefs.ResultRecaps, &prefs.PollReminders)
	if errors.Is(err, pgx.ErrNoRows) {
		return chat.NotificationPrefs{}, nil
	}
	return prefs, err
}

// SetNotificationPref turns one opt-in on or off. The row is created if
// this is the first the bot has heard of the user, so a preference can be
// set before they have voted anywhere.
func (r *ChatRepository) SetNotificationPref(ctx context.Context, userID common.UserID, kind common.NotificationKind, on bool) error {
	column, ok := notificationColumn(kind)
	if !ok {
		return fmt.Errorf("unknown notification kind %q", kind)
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO telegram_user (id, display_name, `+column+`) VALUES ($1, '', $2)
		 ON CONFLICT (id) DO UPDATE SET `+column+` = EXCLUDED.`+column, userID.Value, on)
	return err
}

func (r *ChatRepository) SetUserLocale(ctx context.Context, userID common.UserID, locale common.LocaleCode) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE telegram_user SET locale = $2, updated_at = now() WHERE id = $1`, userID.Value, string(locale))
	return err
}

// RecordManaged best-effort-indexes that userID has just passed a
// manager/admin check for chatID — powers ManagedChats. ensureUser first
// since chat_manager_seen FKs to telegram_user, and a Telegram owner/admin
// who has never been added as a moderator may not have a user row yet.
func (r *ChatRepository) RecordManaged(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO chat_manager_seen(chat_id, user_id, last_confirmed_at) VALUES ($1, $2, now())
		 ON CONFLICT (chat_id, user_id) DO UPDATE SET last_confirmed_at = now()`,
		chatID.Value, userID.Value)
	return err
}

func (r *ChatRepository) ManagedChats(ctx context.Context, userID common.UserID) ([]chat.Settings, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT c.id, c.title, c.locale, c.timezone, c.default_topic_id, c.active, c.default_top_tier_only, c.stream_announcements, c.stream_language
		  FROM chat_manager_seen s
		  JOIN telegram_chat c ON c.id = s.chat_id
		 WHERE s.user_id = $1 AND c.active = true
		 ORDER BY s.last_confirmed_at DESC`, userID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.Settings
	for rows.Next() {
		var s chat.Settings
		if err := rows.Scan(&s.ChatID.Value, &s.Title, &s.Locale, &s.Timezone, &s.DefaultTopicID, &s.Active, &s.DefaultTopTierOnly, &s.StreamAnnouncements, &s.StreamLanguage); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *ChatRepository) SetDMSession(ctx context.Context, userID common.UserID, chatID common.ChatID) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO dm_admin_session(user_id, chat_id, updated_at) VALUES ($1, $2, now())
		 ON CONFLICT (user_id) DO UPDATE SET chat_id = excluded.chat_id, updated_at = now()`,
		userID.Value, chatID.Value)
	return err
}

func (r *ChatRepository) DMSession(ctx context.Context, userID common.UserID) (*common.ChatID, error) {
	var chatID int64
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT chat_id FROM dm_admin_session WHERE user_id = $1`, userID.Value).Scan(&chatID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &common.ChatID{Value: chatID}, nil
}

func (r *ChatRepository) ClearDMSession(ctx context.Context, userID common.UserID) error {
	_, err := executor(ctx, r.pool).Exec(ctx, `DELETE FROM dm_admin_session WHERE user_id = $1`, userID.Value)
	return err
}

func (r *ChatRepository) ListModerators(ctx context.Context, chatID common.ChatID) ([]chat.ModeratorInfo, error) {
	rows, err := executor(ctx, r.pool).Query(ctx, `
		SELECT m.user_id, COALESCE(u.username, ''), u.display_name, m.appointed_by,
		       COALESCE(array_agg(p.permission) FILTER (WHERE p.permission IS NOT NULL), '{}')
		  FROM chat_moderator m
		  JOIN telegram_user u ON u.id = m.user_id
		  LEFT JOIN chat_moderator_permission p ON p.chat_id = m.chat_id AND p.user_id = m.user_id
		 WHERE m.chat_id = $1
		 GROUP BY m.user_id, u.username, u.display_name, m.appointed_by
		 ORDER BY lower(u.display_name), m.user_id`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.ModeratorInfo
	for rows.Next() {
		var item chat.ModeratorInfo
		var uid, appointed int64
		var perms []string
		if err := rows.Scan(&uid, &item.Username, &item.DisplayName, &appointed, &perms); err != nil {
			return nil, err
		}
		item.UserID = common.UserID{Value: uid}
		item.AppointedBy = common.UserID{Value: appointed}
		item.Permissions = toPermissions(perms)
		out = append(out, item)
	}
	return out, rows.Err()
}
