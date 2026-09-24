package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// ctxLoggerKey carries a per-update logger (stamped with telegramUpdateId)
// through the call chain so every log line emitted while handling one
// webhook update can be correlated back to it, without threading a logger
// parameter through every method signature.
type ctxLoggerKey struct{}

func withLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxLoggerKey{}, log)
}

// loggerFrom returns the logger stashed by withLogger, or fallback if none
// was ever attached (e.g. code paths reachable outside Handle, such as
// tests calling a renderer directly).
func loggerFrom(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if log, ok := ctx.Value(ctxLoggerKey{}).(*slog.Logger); ok {
		return log
	}
	return fallback
}

// validationError marks an expected, user-facing input problem (missing
// sender, "reply to a member message", etc.) — the Go equivalent of the
// input problem. Any other error is treated as unexpected and propagates
// out of Handle so the webhook responds 5xx and Telegram retries the
// update later.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
func newValidationError(format string, args ...any) error {
	return &validationError{msg: fmt.Sprintf(format, args...)}
}

// AdminMetrics records what the admin and DM surfaces actually do. Declared
// here, on the consumer side, so this adapter doesn't depend on the app
// package that implements it; a nil Metrics disables recording entirely,
// which is what tests use.
type AdminMetrics interface {
	RecordAdminAction(action, result string)
	RecordDMDelivery(result string)
}

// UpdateHandler dispatches Telegram updates: commands, callback queries,
// and poll answers.
type UpdateHandler struct {
	Dedup         common.UpdateDeduplicator
	Predictions   *prediction.Service
	Chats         chat.Repository
	Authorization *chat.AuthorizationService
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Scoring       scoring.Repository
	Texts         *Texts
	Client        *Client
	Clock         common.Clock
	Log           *slog.Logger
	// PendingApprovals backs the DM fan-out confirmation flow for
	// unsubscribe — see chat.PendingApprovalRepository.
	PendingApprovals chat.PendingApprovalRepository
	// Outbox carries the confirmation fan-out: one event per manager to
	// ask, so the webhook returns without waiting on N sequential sends and
	// delivery inherits the dispatcher's retries and pacing.
	Outbox common.Outbox
	// RunTx commits the pending request and its fan-out events together —
	// the transactional-outbox rule. Nil runs fn directly, which is what
	// tests without a database do.
	RunTx func(ctx context.Context, fn func(ctx context.Context) error) error
	// Metrics counts admin actions and DM deliveries; nil disables it.
	Metrics AdminMetrics
	// AdminActions records who changed what, so co-managers can see changes
	// they never witnessed happening (administration lives in DMs now).
	// Nil disables the history screen entirely.
	AdminActions chat.AdminActionLog
	// Invitations backs the one-time moderator invitation links (see
	// moderator_invitations.go). Nil disables the "🔗 Создать приглашение"
	// assignment method.
	Invitations chat.ModeratorInvitationRepository
	// InboundLimiter caps how often a single Telegram user may trigger the
	// bot to do any work at all — a lightweight defense against one account
	// flooding the bot with commands or callback taps. Nil disables
	// throttling entirely (every test today).
	InboundLimiter *InboundLimiter
	// BotUsername (without the leading "@") is resolved once at startup via
	// getMe, and builds the t.me/<username>?start=... deep links that hand
	// a group's admin panel off to a DM. Left empty, deep-link buttons
	// aren't rendered (see dmDeepLink).
	BotUsername string

	// --- team-identity review (see internal/app.TeamMatchService) ---

	TeamMatches        enrichment.TeamMatchRepository
	TeamMatchHelpers   enrichment.TeamMatchHelperRepository
	TeamMatchOperators enrichment.TeamMatchOperatorRepository
	TeamRankings       enrichment.RankingRepository
	TeamIdentity       enrichment.IdentityRepository
	TeamSnapshots      enrichment.SnapshotRepository
	// TournamentMetadata backs the Liquipedia region/series line on a
	// tournament's own detail card (eventDetails) — nil (Liquipedia
	// disabled) simply omits that line, same as any other optional
	// enrichment source.
	TournamentMetadata enrichment.TournamentMetadataRepository
	// TeamMatchOperatorChatIDs are the root /team_matches operators
	// (DEPLOY_NOTIFY_CHAT_IDS) — always allowed, and the only ones who may
	// appoint/revoke the delegated tier in TeamMatchOperators via
	// /team_match_admin (see isRootTeamMatchOperator).
	TeamMatchOperatorChatIDs []int64

	// --- provider health (see /provider_status, provider_status.go) ---

	// ProviderGateway backs the PandaScore/competition-provider section of
	// /provider_status. Nil (e.g. in tests that don't wire one) simply
	// omits that section.
	ProviderGateway *app.CompetitionProviderGateway
	// EnrichmentState and EnrichmentSources back the per-source sections —
	// the exact same provider_sync_state data /healthz/ready already
	// exposes, just human-readable inside a DM. Nil/empty omits them.
	EnrichmentState   enrichment.SyncStateRepository
	EnrichmentSources []enrichment.Source
	// EnrichmentIntervals is how often each source is expected to run, so
	// the status screen can say whether a timestamp is late rather than
	// just old — see providerStatusView.
	EnrichmentIntervals map[enrichment.Source]time.Duration
	// DeadLetters backs the undelivered-messages panel next to it. Nil
	// omits the panel, exactly like the sections above.
	DeadLetters common.DeadLetterStore

	// Feedback backs the Ideas screen (see suggestions.go). Nil turns the
	// channel off: the button says so rather than failing on use.
	Feedback *app.FeedbackService

	// MiniAppURL is where the Mini App is served from. Empty hides its
	// button entirely — a deployment that does not host the page must not
	// offer a button that opens nothing.
	MiniAppURL string
}

// requireManager wraps chat.AuthorizationService.RequireManager: on success
// it best-effort records the (chat, user) pair into the "chats you manage"
// index that powers the DM chat picker (see recordManaged). A failure to
// record is logged and otherwise ignored; it must never turn a successful
// authorization check into a failed action.
func (h *UpdateHandler) requireManager(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	if err := h.Authorization.RequireManager(ctx, chatID, userID); err != nil {
		h.recordAdminAction("require_manager", "denied")
		return err
	}
	h.recordManaged(ctx, chatID, userID)
	return nil
}

// requirePermission is requireManager's granular sibling: Telegram
// owner/admin always pass; a moderator passes only with the specific
// permission asked for. Used for the settings surfaces that map cleanly
// onto one of the four granted permissions (manage_group_settings,
// view_stats) — see chat.Permission's doc comment for the full mapping.
func (h *UpdateHandler) requirePermission(ctx context.Context, chatID common.ChatID, userID common.UserID, permission chat.Permission) error {
	if err := h.Authorization.RequirePermission(ctx, chatID, userID, permission); err != nil {
		h.recordAdminAction("require_permission", "denied")
		return err
	}
	h.recordManaged(ctx, chatID, userID)
	return nil
}

// recordAdminAction is the one place metrics are optional-checked, so call
// sites read as plain statements.
func (h *UpdateHandler) recordAdminAction(action, result string) {
	if h.Metrics != nil {
		h.Metrics.RecordAdminAction(action, result)
	}
}

// logAdminAction appends one entry to the chat's change history. Purely
// best-effort: the change it describes has already happened, and losing the
// record of it must never turn a successful action into a failed one.
func (h *UpdateHandler) logAdminAction(ctx context.Context, chatID common.ChatID, actor *User, kind, detail string) {
	if h.AdminActions == nil || actor == nil {
		return
	}
	action := chat.AdminAction{
		ChatID: chatID, ActorID: common.UserID{Value: actor.ID},
		ActorName: actor.DisplayName(), Kind: kind, Detail: detail,
		CreatedAt: h.Clock.Now(),
	}
	if err := h.AdminActions.Record(ctx, action); err != nil {
		loggerFrom(ctx, h.Log).Warn("admin action log failed", "chatId", chatID.Value, "kind", kind, "error", err)
	}
}

// requireTelegramAdmin mirrors requireManager for the stricter
// Telegram-admin-only check (moderator appointment).
func (h *UpdateHandler) requireTelegramAdmin(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	if err := h.Authorization.RequireTelegramAdmin(ctx, chatID, userID); err != nil {
		return err
	}
	h.recordManaged(ctx, chatID, userID)
	return nil
}

// ownerContext binds everything rendered downstream to the person who
// asked for it, so a menu posted into a group only answers to them.
// Telegram has no way to show a message to one member only; a keyboard
// that refuses everyone else, with a private alert explaining why, is the
// closest the Bot API allows. A message with no sender (anonymous admin,
// channel post) yields an unowned menu that anyone may use.
func (h *UpdateHandler) ownerContext(ctx context.Context, from *User) context.Context {
	if from == nil {
		return ctx
	}
	return withCallbackScope(ctx, userScope(common.UserID{Value: from.ID}))
}

// inTx runs fn inside a transaction when one is available, and directly
// otherwise.
func (h *UpdateHandler) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if h.RunTx == nil {
		return fn(ctx)
	}
	return h.RunTx(ctx, fn)
}

// markDMReachable records that this user can be messaged privately — they
// just did, so the bot may. Best-effort: it only informs who the
// confirmation fan-out counts on.
func (h *UpdateHandler) markDMReachable(ctx context.Context, userID common.UserID) {
	if err := h.Chats.SetDMReachable(ctx, userID, true); err != nil {
		loggerFrom(ctx, h.Log).Warn("dm reachability record failed", "userId", userID.Value, "error", err)
	}
}

func (h *UpdateHandler) recordManaged(ctx context.Context, chatID common.ChatID, userID common.UserID) {
	if err := h.Chats.RecordManaged(ctx, chatID, userID); err != nil {
		loggerFrom(ctx, h.Log).Warn("managed-chat index record failed", "chatId", chatID.Value, "userId", userID.Value, "error", err)
	}
}

// userLocale is the language for everything rendered in a private chat: the
// user's own stored choice, else the product default. A group chat's locale
// is deliberately separate — someone can run an English DM while managing a
// Russian-speaking chat, and neither setting should follow the other.
func (h *UpdateHandler) userLocale(ctx context.Context, userID common.UserID) common.LocaleCode {
	locale, err := h.Chats.UserLocale(ctx, userID)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("user locale lookup failed, falling back to default", "userId", userID.Value, "error", err)
		return common.LocaleRU
	}
	if locale == nil {
		return common.LocaleRU
	}
	return *locale
}

// userZone is the timezone for everything rendered in a private chat: the
// person's own stored choice, else the product default. Deliberately
// separate from any group's zone, for the same reason userLocale is —
// somebody can live in Berlin and manage a chat set to Moscow, and neither
// setting should quietly become the other.
func (h *UpdateHandler) userZone(ctx context.Context, userID common.UserID) *time.Location {
	zone, err := h.Chats.UserTimezone(ctx, userID)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("user timezone lookup failed, falling back to default", "userId", userID.Value, "error", err)
		return chat.ZoneOrDefault(chat.DefaultTimezone)
	}
	if zone == nil {
		return chat.ZoneOrDefault(chat.DefaultTimezone)
	}
	return chat.ZoneOrDefault(*zone)
}

// resolveUserLocale is userLocale plus first-contact seeding: a user who has
// never chosen a language gets one derived from their Telegram client's own
// language_code, persisted so it survives and so the toggle has something to
// flip. Only EN is derived — everything else keeps the product default —
// because those are the two bundles that exist.
func (h *UpdateHandler) resolveUserLocale(ctx context.Context, userID common.UserID, from *User) common.LocaleCode {
	stored, err := h.Chats.UserLocale(ctx, userID)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("user locale lookup failed, falling back to default", "userId", userID.Value, "error", err)
		return common.LocaleRU
	}
	if stored != nil {
		return *stored
	}
	locale := common.LocaleRU
	if from != nil && from.LanguageCode != nil && strings.HasPrefix(strings.ToLower(*from.LanguageCode), "en") {
		locale = common.LocaleEN
	}
	if err := h.Chats.SetUserLocale(ctx, userID, locale); err != nil {
		loggerFrom(ctx, h.Log).Warn("user locale seed failed", "userId", userID.Value, "error", err)
	}
	return locale
}

func (h *UpdateHandler) Handle(ctx context.Context, update Update) error {
	log := h.Log.With("telegramUpdateId", update.UpdateID)
	ctx = withLogger(ctx, log)

	claimed, err := h.Dedup.Claim(ctx, update.UpdateID)
	if err != nil {
		return err
	}
	if !claimed {
		log.Debug("duplicate telegram update, skipping")
		return nil
	}

	if actor, ok := updateActor(update); ok && !h.InboundLimiter.Allow(actor) {
		log.Debug("inbound rate limit exceeded, dropping update", "userId", actor)
		h.recordAdminAction("inbound_rate_limit", "dropped")
		if update.CallbackQuery != nil {
			// Otherwise the tapped button's own loading spinner sits there
			// until the Telegram client's timeout — a bare ack (no text)
			// at least tells the client the tap was received, even though
			// this update itself is being dropped.
			if ackErr := h.answer(ctx, update.CallbackQuery.ID); ackErr != nil {
				log.Debug("failed to ack a throttled callback query", "error", ackErr)
			}
		}
		return nil
	}

	if err := h.dispatch(ctx, update); err != nil {
		// Un-claim so a Telegram webhook retry can reprocess this update,
		// so a retried delivery gets a fresh attempt. This does mean a
		// handler whose user-visible reply already went out before some
		// later, unexpected failure (a DB write, a second Bot API call)
		// will have that reply replayed too on retry — accepted rather
		// than fixed, for two reasons: (1) every expected failure a
		// handler can hit (chat.ErrAccessDenied, a validationError) is
		// already caught by handleCommandError and turned into a friendly
		// reply + nil before it ever reaches here, so an error actually
		// reaching this point means something unexpected happened (DB or
		// Telegram outage), not routine user input; and (2) this
		// codebase's own handler convention is to run every mutation
		// before the final h.respond/h.send call, not after, so the
		// window where a reply has already gone out but the handler can
		// still fail afterward is narrow in practice. Closing it for good
		// would need every handler's side effects made idempotent
		// end-to-end, which is out of proportion to how rarely this path
		// is even reached.
		_ = h.Dedup.Release(ctx, update.UpdateID)
		return err
	}
	return nil
}

func (h *UpdateHandler) dispatch(ctx context.Context, update Update) error {
	if update.PollAnswer != nil {
		a := update.PollAnswer
		if err := h.Predictions.RecordVote(ctx, a.PollID, common.UserID{Value: a.User.ID}, a.OptionIDs, a.User.Username, a.User.DisplayName()); err != nil {
			return err
		}
	}
	if update.Message != nil {
		if err := h.handleMessage(ctx, update.Message); err != nil {
			return err
		}
	}
	if update.CallbackQuery != nil {
		if err := h.handleCallback(ctx, update.CallbackQuery); err != nil {
			return err
		}
	}
	if update.MyChatMember != nil {
		if err := h.handleMyChatMember(ctx, update.MyChatMember); err != nil {
			return err
		}
	}
	return nil
}

// handleMyChatMember reacts to the bot's own membership changing in a chat.
// Removed (kicked, or an admin used "leave group" on the bot's behalf)
// marks the chat inactive — the same flag ListActive/ManagedChats already
// filter on — so the bot stops listing it as manageable and stops trying
// to post into a chat it's no longer in; re-added flips it back to active.
// Without this, a chat the bot was removed from stayed listed as active
// forever, since nothing else in this codebase ever clears the flag.
func (h *UpdateHandler) handleMyChatMember(ctx context.Context, update *ChatMemberUpdated) error {
	settings, err := h.Chats.Find(ctx, common.ChatID{Value: update.Chat.ID})
	if err != nil {
		return err
	}
	if settings == nil {
		// No row yet. Being added is the one case worth acting on: that is
		// the bot's first moment in this room, and saying nothing leaves a
		// group waiting for polls that will never come (see
		// welcomeNewChat). Anything else — added and removed before anyone
		// typed — has nothing to update.
		if update.NewChatMember.Status == "member" || update.NewChatMember.Status == "administrator" {
			return h.welcomeNewChat(ctx, update)
		}
		return nil
	}
	switch update.NewChatMember.Status {
	case "left", "kicked":
		settings.Active = false
	default:
		settings.Active = true
	}
	_, err = h.Chats.Save(ctx, *settings)
	return err
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (h *UpdateHandler) handleMessage(ctx context.Context, msg *Message) error {
	if msg.Chat.Type == "private" {
		return h.handlePrivateMessage(ctx, msg)
	}
	// A pure service message announcing this basic group's permanent
	// upgrade to a supergroup — nothing else on it to process (no text, no
	// sender action), so rename the chat's row and stop.
	if msg.MigrateToChatID != nil {
		return h.Chats.MigrateChatID(ctx, common.ChatID{Value: msg.Chat.ID}, common.ChatID{Value: *msg.MigrateToChatID})
	}

	chatID := common.ChatID{Value: msg.Chat.ID}
	current, err := h.Chats.Find(ctx, chatID)
	if err != nil {
		return err
	}
	settings := chat.Settings{ChatID: chatID, Title: "Telegram chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	if current != nil {
		settings = *current
	}
	if msg.Chat.Title != nil {
		settings.Title = *msg.Chat.Title
	}
	settings.Active = true
	saved, err := h.Chats.Save(ctx, settings)
	if err != nil {
		return err
	}
	settings = saved

	if msg.Text == nil {
		return nil
	}
	text := strings.TrimSpace(*msg.Text)

	// isEventSearchReply's flow can only be entered via the events:search
	// button, which (since the group /menu hands Events off to a DM — see
	// the /events case below) is only ever rendered inside a DM. Kept here
	// as a defensive fallback rather than removed outright, in case an
	// old prompt is still sitting in a group as something to reply to.
	// A group message need not have a sender: anonymous admins and channel
	// posts arrive with from unset, and menus opened that way simply have no
	// owner to bind to (see ownerContext).
	ownerCtx := h.ownerContext(ctx, msg.From)

	var cmdErr error
	if !strings.HasPrefix(text, "/") && isEventSearchReply(msg, settings.Locale, h.Texts) {
		cmdErr = h.searchEvents(ctx, msg, settings, text)
	} else {
		switch {
		case strings.HasPrefix(text, "/start"), strings.HasPrefix(text, "/menu"):
			cmdErr = h.menu(ownerCtx, sendTarget(chatID, msg.MessageThreadID), settings, false)
		case strings.HasPrefix(text, "/events"):
			if handled, err := h.tryRedirectToDMAdmin(ctx, settings, msg.MessageThreadID, "dm.redirect_events"); handled {
				cmdErr = err
			} else {
				cmdErr = h.searchEvents(ctx, msg, settings, strings.TrimSpace(strings.TrimPrefix(text, "/events")))
			}
		case strings.HasPrefix(text, "/stats"):
			cmdErr = h.statsMenu(ctx, sendTarget(chatID, msg.MessageThreadID), settings)
		case strings.HasPrefix(text, "/timezone "):
			if handled, err := h.tryRedirectToDMAdmin(ctx, settings, msg.MessageThreadID, "dm.redirect_timezone"); handled {
				cmdErr = err
			} else {
				cmdErr = h.changeTimezone(ctx, msg, settings, strings.TrimSpace(strings.TrimPrefix(text, "/timezone ")))
			}
		case text == "/topic", text == "/topic here":
			cmdErr = h.setTopic(ctx, msg, settings)
		case strings.HasPrefix(text, "/topics"):
			// Per-event topic binding stays a lightweight group-only command
			// (like /topic itself), not part of the DM admin hand-off: binding
			// only works when invoked from inside the actual forum topic
			// thread being bound, which a DM cannot represent. subscribedEvents
			// with dmContext=false shows the bind-topic buttons but never
			// unsubscribe, which is DM-only administration (see events:mine).
			cmdErr = h.subscribedEvents(ownerCtx, sendTarget(chatID, msg.MessageThreadID), settings, false)
		case strings.HasPrefix(text, "/moderator add"):
			cmdErr = h.changeModerator(ctx, msg, settings, true)
		case strings.HasPrefix(text, "/moderator remove"):
			cmdErr = h.changeModerator(ctx, msg, settings, false)
		case strings.HasPrefix(text, "/help"):
			cmdErr = h.helpView(ctx, sendTarget(chatID, msg.MessageThreadID), settings.Locale, "menu:main", false)
		}
	}
	return h.handleCommandError(ctx, settings, msg.MessageThreadID, text, cmdErr)
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (h *UpdateHandler) handlePrivateMessage(ctx context.Context, msg *Message) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if msg.Text == nil {
		return nil
	}

	text := strings.TrimSpace(*msg.Text)
	chatID := common.ChatID{Value: msg.Chat.ID}
	userID := common.UserID{Value: msg.From.ID}
	locale := h.resolveUserLocale(ctx, userID, msg.From)
	h.markDMReachable(ctx, userID)

	switch {
	case strings.HasPrefix(text, "/start "):
		if targetChatID, ok := parseAdminDeepLink(text); ok {
			return h.openManagedChat(ctx, sendTarget(chatID, nil), userID, locale, targetChatID)
		}
		if statsChatID, ok := parseStatsDeepLink(text); ok {
			return h.openGroupStatsInDM(ctx, sendTarget(chatID, nil), userID, locale, statsChatID)
		}
		if eventChatID, eventID, ok := parsePersonalStatsDeepLink(text); ok {
			return h.openPersonalEventStats(ctx, sendTarget(chatID, nil), userID, locale, eventChatID, eventID)
		}
		if token, ok := parseInvitationDeepLink(text); ok {
			return h.openInvitationAccept(ctx, sendTarget(chatID, nil), userID, locale, token)
		}
		return h.startLanding(ctx, sendTarget(chatID, nil), userID, locale)
	case strings.HasPrefix(text, "/start"), strings.HasPrefix(text, "/menu"):
		return h.startLanding(ctx, sendTarget(chatID, nil), userID, locale)
	case strings.HasPrefix(text, "/stats"):
		return h.privateResultsMenu(ctx, sendTarget(chatID, nil), userID, locale)
	case strings.HasPrefix(text, "/bets"):
		return h.privateBetsMenu(ctx, sendTarget(chatID, nil), userID, locale, betsFilter{}, 0, 0)
	case strings.HasPrefix(text, "/team_match_admin"):
		return h.handleCommandError(ctx, chat.Settings{ChatID: chatID, Locale: locale}, nil, text,
			h.handleTeamMatchAdminCommand(ctx, chatID, userID, locale, strings.TrimSpace(strings.TrimPrefix(text, "/team_match_admin"))))
	case strings.HasPrefix(text, "/team_matches"):
		return h.teamMatchQueueMenu(ctx, sendTarget(chatID, nil), userID, locale, 0)
	case strings.HasPrefix(text, "/provider_status"):
		return h.providerStatusView(ctx, sendTarget(chatID, nil), userID, locale)
	case strings.HasPrefix(text, "/help"):
		return h.helpView(ctx, sendTarget(chatID, nil), locale, "pstats:menu", true)
	case !strings.HasPrefix(text, "/") && h.isSuggestion(msg, locale):
		// Same reasoning as the rename reply below: user-scoped, so it
		// must work in every DM rather than only with a managed-chat
		// session open.
		return h.handleCommandError(ctx, chat.Settings{ChatID: chatID, Locale: locale}, nil, text,
			h.applySuggestionReply(ctx, msg, msg.From, locale, text))
	case !strings.HasPrefix(text, "/") && isRenameReply(msg, locale, h.Texts):
		// User-scoped, not chat-scoped — unlike the events-search reply
		// below, this must work for every DM, not only someone with an
		// active managed-chat session, so it's checked before that gate.
		return h.handleCommandError(ctx, chat.Settings{ChatID: chatID, Locale: locale}, nil, text, h.applyNicknameReply(ctx, msg, userID, locale, text))
	}

	// Everything below operates on the chat the user's DM session
	// (chat.Repository.DMSession) currently points at: the DM side of the
	// group command surface handed off here by tryRedirectToDMAdmin
	// (/events, /timezone), plus the "reply with a tournament name"
	// continuation of the events:search button (see requestEventSearch).
	// No active session falls through to the personal dashboard, same as
	// any other free text.
	settings, ok, err := h.currentManagedChat(ctx, userID)
	if err != nil {
		return err
	}
	if !ok {
		return h.privateStatsMenu(ctx, sendTarget(chatID, nil), userID, locale)
	}
	switch {
	case !strings.HasPrefix(text, "/") && isEventSearchReply(msg, settings.Locale, h.Texts):
		return h.handleCommandError(ctx, settings, nil, text, h.searchEvents(ctx, msg, settings, text))
	case strings.HasPrefix(text, "/events"):
		return h.handleCommandError(ctx, settings, nil, text, h.searchEvents(ctx, msg, settings, strings.TrimSpace(strings.TrimPrefix(text, "/events"))))
	case strings.HasPrefix(text, "/timezone "):
		return h.handleCommandError(ctx, settings, nil, text, h.changeTimezone(ctx, msg, settings, strings.TrimSpace(strings.TrimPrefix(text, "/timezone "))))
	default:
		// In a 1:1 chat, free-form text with no more specific meaning is most
		// useful as a shortcut back to the personal dashboard instead of the
		// old "groups only" dead end.
		return h.privateStatsMenu(ctx, sendTarget(chatID, nil), userID, locale)
	}
}

// isSuggestion reports whether this message answers the Ideas prompt,
// using whichever size limit the running policy states in it.
func (h *UpdateHandler) isSuggestion(msg *Message, locale common.LocaleCode) bool {
	if h.Feedback == nil {
		return false
	}
	return isSuggestionReply(msg, locale, h.Texts, h.Feedback.Policy.MaxRunes)
}

// adminDeepLinkPrefix is the "/start <prefix><base36 chat id>" payload
// generated by dmDeepLink for the "manage this chat in DM" button.
const adminDeepLinkPrefix = "admin_"

// parseAdminDeepLink extracts a chat id from a "/start admin_<id>" deep
// link payload (t.me/<bot>?start=admin_<id>) — Telegram delivers the
// payload as the text following "/start ", space-separated.
func parseAdminDeepLink(text string) (common.ChatID, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
	if !strings.HasPrefix(payload, adminDeepLinkPrefix) {
		return common.ChatID{}, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(payload, adminDeepLinkPrefix), 36, 64)
	if err != nil {
		return common.ChatID{}, false
	}
	return common.ChatID{Value: id}, true
}

// statsDeepLinkPrefix opens a group-scoped, read-only statistics panel in DM.
const statsDeepLinkPrefix = "stats_"

func parseStatsDeepLink(text string) (common.ChatID, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
	if !strings.HasPrefix(payload, statsDeepLinkPrefix) {
		return common.ChatID{}, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(payload, statsDeepLinkPrefix), 36, 64)
	if err != nil {
		return common.ChatID{}, false
	}
	return common.ChatID{Value: id}, true
}

func (h *UpdateHandler) openGroupStatsInDM(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, chatID common.ChatID) error {
	settings, err := h.Chats.Find(ctx, chatID)
	if err != nil {
		return err
	}
	if settings == nil {
		return h.privateStatsMenu(ctx, target, userID, locale)
	}
	return h.statsMenu(withCallbackScope(ctx, chatScope(chatID)), target, *settings)
}

// personalStatsDeepLinkPrefix is the "/start pstats_<base36 chat id>:<hex
// event id>" payload generated by MatchResultPublisher's "🙋 My stats"
// button — carries both ids since personalStats reports one user's rank
// within one specific chat's tracking of one specific event, not the
// cross-chat aggregate the rest of the private dashboard shows.
const personalStatsDeepLinkPrefix = "pstats_"

func parsePersonalStatsDeepLink(text string) (common.ChatID, common.EventID, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
	if !strings.HasPrefix(payload, personalStatsDeepLinkPrefix) {
		return common.ChatID{}, common.EventID{}, false
	}
	token := strings.TrimPrefix(payload, personalStatsDeepLinkPrefix)
	chatPart, eventPart, ok := strings.Cut(token, ":")
	if !ok {
		return common.ChatID{}, common.EventID{}, false
	}
	chatIDVal, err := strconv.ParseInt(chatPart, 36, 64)
	if err != nil {
		return common.ChatID{}, common.EventID{}, false
	}
	eventUUID, err := uuid.Parse(eventPart)
	if err != nil {
		return common.ChatID{}, common.EventID{}, false
	}
	return common.ChatID{Value: chatIDVal}, common.EventID{Value: eventUUID}, true
}

// openPersonalEventStats renders the DM side of the "🙋 My stats" hand-off
// — the chat named in the deep link may no longer be known (e.g. the bot
// was removed from it since); that's not an error, it just falls back to
// the personal dashboard rather than showing a broken screen.
func (h *UpdateHandler) openPersonalEventStats(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, chatID common.ChatID, eventID common.EventID) error {
	settings, err := h.Chats.Find(ctx, chatID)
	if err != nil {
		return err
	}
	if settings == nil {
		return h.privateStatsMenu(ctx, target, userID, locale)
	}
	return h.personalStats(ctx, target, *settings, User{ID: userID.Value}, eventID)
}

// handleCommandError maps a command's returned error to a user-facing
// reply: chat.ErrAccessDenied -> error.forbidden, a validationError ->
// error.generic (with a warning log including the command prefix);
// anything else propagates unchanged.
func (h *UpdateHandler) handleCommandError(ctx context.Context, settings chat.Settings, topicID *int64, text string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, chat.ErrAccessDenied) {
		return h.sendText(ctx, settings.ChatID, h.Texts.Get("error.forbidden", settings.Locale), topicID)
	}
	var ve *validationError
	if errors.As(err, &ve) {
		loggerFrom(ctx, h.Log).Warn("command failed", "chatId", settings.ChatID.Value, "command", strings.SplitN(text, " ", 2)[0])
		return h.sendText(ctx, settings.ChatID, h.Texts.Get("error.generic", settings.Locale), topicID)
	}
	// Anything else (a DB outage, a Telegram API failure, ...) is genuinely
	// unexpected — the caller still un-claims dedup and lets Telegram retry
	// by returning err below, but a tapped button or typed command must not
	// look like it silently did nothing in the meantime. Best-effort: if
	// even this reply fails, the original err still propagates unchanged.
	if sendErr := h.sendText(ctx, settings.ChatID, h.Texts.Get("error.generic", settings.Locale), topicID); sendErr != nil {
		loggerFrom(ctx, h.Log).Warn("failed to notify user of an unexpected error", "chatId", settings.ChatID.Value, "error", sendErr)
	}
	return err
}

// parseEventSearch splits a raw /events query into an optional explicit
// top-tier override (nil when the query carries no filter word, meaning
// "use the chat's default") and the remaining search text. The filter word
// may also be used on its own: "/events all" and "/events top" open the
// corresponding browse catalog instead of searching for the literal words
// "all" or "top".
func parseEventSearch(query string) (override *bool, rest string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ""
	}
	parts := strings.Fields(query)
	first := strings.ToLower(parts[0])
	switch first {
	case "top", "топ":
		forceTop := true
		return &forceTop, strings.TrimSpace(strings.TrimPrefix(query, parts[0]))
	case "all", "все":
		forceAll := false
		return &forceAll, strings.TrimSpace(strings.TrimPrefix(query, parts[0]))
	default:
		return nil, query
	}
}

// searchEvents replies into msg.Chat (the group or DM the command/reply was
// actually sent from) — NOT necessarily settings.ChatID, the chat whose
// subscriptions are being managed: reached from a DM admin session, those
// two differ, and the search results/browse screen must land back in the
// DM the user is looking at, not silently post into the managed group.
// requireManager and filterUnsubscribedEvents still correctly key off
// settings.ChatID, the chat actually being managed.
func (h *UpdateHandler) searchEvents(ctx context.Context, msg *Message, settings chat.Settings, rawQuery string) error {
	override, query := parseEventSearch(rawQuery)
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}); err != nil {
		return err
	}
	replyChatID := common.ChatID{Value: msg.Chat.ID}

	topTierOnly := settings.DefaultTopTierOnly
	if override != nil {
		topTierOnly = *override
	}
	if query == "" {
		return h.renderEventBrowse(ctx, sendTarget(replyChatID, msg.MessageThreadID), settings, topTierOnly, 0)
	}
	if len([]rune(query)) < 2 {
		return h.sendText(ctx, replyChatID, h.Texts.Get("events.search_hint", settings.Locale), msg.MessageThreadID)
	}

	// Fetch a wider candidate set before removing tournaments already added
	// to this chat, so subscriptions do not make the search page look empty.
	found, err := h.Catalog.SearchEvents(ctx, query, 100, topTierOnly, settings.EnabledGames)
	if err != nil {
		return err
	}
	found, err = h.filterUnsubscribedEvents(ctx, settings.ChatID, found)
	if err != nil {
		return err
	}
	if len(found) > 20 {
		found = found[:20]
	}
	rows := h.gameSectionRows(found, settings.Locale, func(e competition.Event) []InlineButton {
		return []InlineButton{button(eventLabel(e), cbSubscribe(e.ID))}
	})
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "events:add")})

	header := bold(escapeHTML(query))
	body := header + "\n\n" + h.Texts.Get("events.search_pick_prompt", settings.Locale)
	if len(found) == 0 {
		body = header + "\n\n" + h.Texts.Get("events.search_empty", settings.Locale)
	}
	return h.sendTextWithKeyboard(ctx, replyChatID, body, InlineKeyboard{InlineKeyboard: rows}, msg.MessageThreadID)
}

func isEventSearchReply(msg *Message, locale common.LocaleCode, texts *Texts) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == nil {
		return false
	}
	return strings.TrimSpace(*msg.ReplyToMessage.Text) == strings.TrimSpace(stripHTML(texts.Get("events.search_prompt", locale)))
}

func stripHTML(s string) string {
	r := strings.NewReplacer("<b>", "", "</b>", "", "<i>", "", "</i>", "", "<code>", "", "</code>", "")
	return r.Replace(s)
}

// changeTimezone replies into msg.Chat — the group (its own /timezone was
// retired in favor of a DM redirect, but the handler stays generic) or,
// today, exclusively the DM the command was typed in — not necessarily
// settings.ChatID, the chat whose timezone is actually being changed.
func (h *UpdateHandler) changeTimezone(ctx context.Context, msg *Message, settings chat.Settings, value string) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requirePermission(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}, chat.PermissionManageGroupSettings); err != nil {
		return err
	}
	replyChatID := common.ChatID{Value: msg.Chat.ID}
	loc, err := time.LoadLocation(value)
	if err != nil {
		return h.sendText(ctx, replyChatID, h.Texts.Get("timezone.unknown", settings.Locale, code(escapeHTML(value))), nil)
	}
	settings.Timezone = loc.String()
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return err
	}
	h.logAdminAction(ctx, settings.ChatID, msg.From, "timezone", loc.String())
	return h.sendText(ctx, replyChatID, h.Texts.Get("timezone.set_confirmed", settings.Locale, code(escapeHTML(loc.String()))), nil)
}

func (h *UpdateHandler) setTopic(ctx context.Context, msg *Message, settings chat.Settings) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}); err != nil {
		return err
	}
	settings.DefaultTopicID = msg.MessageThreadID
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return err
	}
	text := "✅ Prediction topic saved"
	if settings.Locale == common.LocaleRU {
		text = "✅ Тема для прогнозов сохранена"
	}
	return h.sendText(ctx, settings.ChatID, text, msg.MessageThreadID)
}

func (h *UpdateHandler) changeModerator(ctx context.Context, msg *Message, settings chat.Settings, add bool) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	actor := common.UserID{Value: msg.From.ID}
	if err := h.requireTelegramAdmin(ctx, settings.ChatID, actor); err != nil {
		return err
	}
	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		return newValidationError("reply to a member message")
	}
	targetUser := msg.ReplyToMessage.From
	target := common.UserID{Value: targetUser.ID}
	username := ""
	if targetUser.Username != nil {
		username = *targetUser.Username
	}
	var err error
	if add {
		// The reply-command appointment path predates granular permissions
		// and has no wizard to pick them from — it grants full access, same
		// as every moderator appointed before this feature existed. The
		// admin can narrow it afterward from the moderator's card in DM.
		err = h.Chats.AddModerator(ctx, chat.Moderator{ChatID: settings.ChatID, UserID: target, AppointedBy: actor, Username: username, DisplayName: targetUser.DisplayName(), Permissions: chat.PresetFullAccess()})
	} else {
		err = h.Chats.RemoveModerator(ctx, settings.ChatID, target)
	}
	if err != nil {
		return err
	}
	h.logAdminAction(ctx, settings.ChatID, msg.From, ternary(add, "moderator_added", "moderator_removed"), targetUser.DisplayName())
	text := h.Texts.Get("moderators.updated", settings.Locale)
	return h.sendText(ctx, settings.ChatID, text, msg.MessageThreadID)
}

// --- send helpers ---
//
// Both delegate to render.go's send, which always sets parse_mode HTML —
// every text passed here that embeds user-controlled data (a team/event
// name, a display name, a chat's saved timezone string) must already have
// gone through escapeHTML.

func (h *UpdateHandler) sendText(ctx context.Context, chatID common.ChatID, text string, topicID *int64) error {
	return h.send(ctx, chatID, text, nil, topicID)
}

func (h *UpdateHandler) sendTextWithKeyboard(ctx context.Context, chatID common.ChatID, text string, keyboard InlineKeyboard, topicID *int64) error {
	return h.send(ctx, chatID, text, &keyboard, topicID)
}
