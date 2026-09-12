package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config holds the Telegram Bot API client settings.
type Config struct {
	BaseURL       string
	Token         string
	WebhookSecret string

	// GlobalMessagesPerSecond/PerChatMessagesPerSecond pace outbound calls
	// below Telegram's own ceilings. Zero (the zero value, and what tests
	// use) disables pacing entirely; production passes real limits — see
	// DefaultGlobalMessagesPerSecond.
	GlobalMessagesPerSecond  float64
	PerChatMessagesPerSecond float64
}

func DefaultConfig() Config {
	return Config{BaseURL: "https://api.telegram.org"}
}

// APIError wraps a Telegram Bot API failure. It intentionally never embeds
// the underlying transport error (which could contain the request URL, and
// therefore the bot token) — only Telegram's own "ok":false description,
// which is safe to surface. See client_test.go for the contract test.
type APIError struct {
	Method  string
	Message string

	// ErrorCode/RetryAfter are populated for a 429 "Too Many Requests"
	// response (RetryAfter in seconds, from Telegram's own
	// parameters.retry_after). Call already retries on this
	// automatically; these fields are exposed mainly for tests and
	// logging.
	ErrorCode  int
	RetryAfter int
}

func (e *APIError) Error() string { return e.Message }

func (e *APIError) isDescriptionContains(substrings ...string) bool {
	lower := strings.ToLower(e.Message)
	for _, s := range substrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// IsTopicUnavailable matches Telegram's error text for a removed/invalid
// forum topic (e.g. "message thread not found").
func (e *APIError) IsTopicUnavailable() bool { return e.isDescriptionContains("thread", "topic") }

// IsReplyUnavailable matches Telegram's error text for a reply target that
// no longer exists (e.g. "message to be replied not found").
func (e *APIError) IsReplyUnavailable() bool {
	return e.isDescriptionContains("replied", "reply message")
}

// IsRateLimited is true for Telegram's 429 "Too Many Requests".
func (e *APIError) IsRateLimited() bool { return e.ErrorCode == http.StatusTooManyRequests }

// IsUnreachableUser matches Telegram's refusals to deliver a private
// message: a user who never started a chat with the bot, one who blocked
// it, or a deactivated account. All three mean "don't retry, and stop
// counting on this person" rather than "something went wrong".
func (e *APIError) IsUnreachableUser() bool {
	return e.ErrorCode == http.StatusForbidden ||
		e.isDescriptionContains("bot can't initiate conversation") ||
		e.isDescriptionContains("bot was blocked") ||
		e.isDescriptionContains("user is deactivated") ||
		e.isDescriptionContains("chat not found")
}

// IsNotModified matches editMessageText's error for editing a message with
// content identical to what it already has (e.g. a double-tap on the same
// menu button) — not a real failure, safe to treat as a no-op success.
func (e *APIError) IsNotModified() bool { return e.isDescriptionContains("message is not modified") }

// IsEditUnavailable matches editMessageText's errors for a target that can
// no longer be edited at all: deleted, too old (Telegram only allows
// editing within 48h), or edited by another actor into a state this bot
// lost track of. Callers should fall back to sending a new message.
func (e *APIError) IsEditUnavailable() bool {
	return e.isDescriptionContains("message to edit not found", "message can't be edited", "message is too old")
}

// IsPollAlreadyClosed matches stopPoll's error for a poll Telegram already
// auto-closed via sendPoll's close_date — expected, not a real failure,
// once a poll carries its own close_date (see PollGateway.Send): the
// scheduled close job's own stopPoll call can lose the race to Telegram's
// own timer and arrive after it already closed the poll. Telegram uses (at
// least) two different wordings for this — "poll has already been closed"
// and "poll can't be stopped" — confirmed in production: a poll matching
// only the first wording left CloseDue retrying the same stuck poll every
// tick indefinitely, since it never counted as success and so never
// persisted the local CLOSED status.
func (e *APIError) IsPollAlreadyClosed() bool {
	return e.isDescriptionContains("poll has already been closed", "poll can't be stopped")
}

// Client is the single generic Bot API caller — every Bot API method goes
// through Call(method, payload) rather than a per-method typed wrapper.
type Client struct {
	config     Config
	httpClient *http.Client
	sleep      func(ctx context.Context, d time.Duration) error // overridable in tests
	limiter    *rateLimiter
}

func NewClient(config Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{config: config, httpClient: httpClient, sleep: sleepContext,
		limiter: newRateLimiter(config.GlobalMessagesPerSecond, config.PerChatMessagesPerSecond)}
}

// GetMe resolves the bot's own identity — used once at startup to learn its
// @username for t.me/<username>?start=... deep links (nothing else in this
// adapter needs it, so it isn't cached here; the caller is expected to
// resolve it once and hold onto the result).
func (c *Client) GetMe(ctx context.Context) (User, error) {
	raw, err := c.Call(ctx, "getMe", nil)
	if err != nil {
		return User{}, err
	}
	var me User
	if err := json.Unmarshal(raw, &me); err != nil {
		return User{}, err
	}
	return me, nil
}

// BotCommand is one entry Telegram shows in a chat's "/" command menu.
// Command is the bare name (no leading slash, lowercase letters/digits/
// underscores, ≤32 chars); Description is the one-line explanation shown
// next to it (≤256 chars).
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// botCommandScope selects which chats a SetMyCommands call applies to.
// Telegram resolves the most specific scope that matches a given chat, so
// "all_private_chats"/"all_group_chats" only need setting once each — no
// per-chat calls.
type botCommandScope struct {
	Type string `json:"type"`
}

var (
	scopeAllPrivateChats = botCommandScope{Type: "all_private_chats"}
	scopeAllGroupChats   = botCommandScope{Type: "all_group_chats"}
)

// SetMyCommands registers the "/" command hints Telegram shows while typing,
// scoped to either private or group chats and optionally to one client
// language (empty languageCode is the fallback shown to every language that
// has no explicit entry of its own).
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand, scope botCommandScope, languageCode string) error {
	payload := map[string]any{"commands": commands, "scope": scope}
	if languageCode != "" {
		payload["language_code"] = languageCode
	}
	_, err := c.Call(ctx, "setMyCommands", payload)
	return err
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maxRetryAttempts bounds how many times Call retries a 429; maxRetryWait
// caps how long a single retry_after wait can be, so a misbehaving or
// overly conservative retry_after value can't block a scheduler goroutine
// indefinitely.
const (
	maxRetryAttempts = 3
	maxRetryWait     = 30 * time.Second
)

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Call POSTs payload as JSON to {baseUrl}/bot{token}/{method} and returns
// the raw "result" field. A 429 response is retried automatically, honoring
// Telegram's own retry_after (capped at maxRetryWait, up to maxRetryAttempts
// times) — safe to retry because a 429 means Telegram rejected the request
// before processing it, unlike a transport-level timeout where the call may
// have already gone through. Any transport-level failure is rewritten as a
// generic APIError without the underlying error text (see APIError's doc)
// and is NOT retried, for the same reason.
func (c *Client) Call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	// Pace before sending rather than only reacting to a 429 afterwards:
	// crossing Telegram's ceilings costs a round trip and a sleep, and the
	// bot crosses them exactly when it's busiest.
	if err := c.limiter.wait(ctx, chatIDFromPayload(payload)); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetryAttempts; attempt++ {
		result, err := c.doCall(ctx, method, payload)
		if err == nil {
			return result, nil
		}
		lastErr = err

		apiErr, ok := err.(*APIError)
		if !ok || !apiErr.IsRateLimited() || attempt == maxRetryAttempts {
			return nil, err
		}
		wait := time.Duration(apiErr.RetryAfter) * time.Second
		if wait <= 0 || wait > maxRetryWait {
			wait = maxRetryWait
		}
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return nil, sleepErr
		}
	}
	return nil, lastErr
}

// doCallTimeout bounds a single HTTP round trip to Telegram so a stalled
// connection can't hang a request-handling path indefinitely — this bot is
// webhook-driven (no getUpdates long-polling call ever goes through here),
// so every method this client calls is expected to complete quickly.
// context.WithTimeout takes the earlier of this and any deadline the caller
// already set, so it only ever tightens, never loosens, an existing one.
// A var, not a const, so a test can shrink it rather than actually waiting
// out a 15s stall.
var doCallTimeout = 15 * time.Second

func (c *Client) doCall(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if strings.TrimSpace(c.config.Token) == "" {
		return nil, &APIError{Method: method, Message: "telegram token is not configured"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &APIError{Method: method, Message: fmt.Sprintf("encode %s payload failed", method)}
	}

	ctx, cancel := context.WithTimeout(ctx, doCallTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/%s", c.config.BaseURL, c.config.Token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &APIError{Method: method, Message: fmt.Sprintf("build %s request failed", method)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Never include err.Error() here — it can contain the request URL,
		// which contains the bot token.
		return nil, &APIError{Method: method, Message: fmt.Sprintf("telegram %s request failed", method)}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil || len(respBody) == 0 {
		return nil, &APIError{Method: method, Message: fmt.Sprintf("empty response from %s", method)}
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, &APIError{Method: method, Message: fmt.Sprintf("telegram %s returned an unparseable response", method)}
	}
	if !envelope.OK {
		apiErr := &APIError{Method: method, Message: fmt.Sprintf("telegram %s failed: %s", method, envelope.Description), ErrorCode: envelope.ErrorCode}
		if envelope.Parameters != nil {
			apiErr.RetryAfter = envelope.Parameters.RetryAfter
		}
		return nil, apiErr
	}
	return envelope.Result, nil
}

// SendPhoto uploads imgData (e.g. a chart PNG) as a photo message — the one
// call in this client that sends binary content rather than a JSON payload,
// since Telegram's sendPhoto only accepts an actual file upload, a URL, or
// a previously-seen file_id, and a freshly rendered chart has none of the
// latter two. No retry-on-429 loop (unlike Call): a chart is generated
// on demand from a button tap, so the simplest recovery from a rate limit
// is the same tap again, not a hidden multi-second wait inside the request
// that handled it.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, filename string, imgData []byte, caption string, topicID *int64) error {
	if err := c.limiter.wait(ctx, chatID); err != nil {
		return err
	}
	if strings.TrimSpace(c.config.Token) == "" {
		return &APIError{Method: "sendPhoto", Message: "telegram token is not configured"}
	}

	body, contentType, err := buildSendPhotoForm(chatID, filename, imgData, caption, topicID)
	if err != nil {
		return err
	}
	return c.postSendPhoto(ctx, body, contentType)
}

// buildSendPhotoForm encodes sendPhoto's multipart/form-data body: every
// scalar field Telegram accepts alongside the upload itself, plus the image
// bytes as the "photo" file part.
func buildSendPhotoForm(chatID int64, filename string, imgData []byte, caption string, topicID *int64) (*bytes.Buffer, string, error) {
	encodeErr := &APIError{Method: "sendPhoto", Message: "encode sendPhoto form failed"}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return nil, "", encodeErr
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return nil, "", encodeErr
		}
		if err := w.WriteField("parse_mode", "HTML"); err != nil {
			return nil, "", encodeErr
		}
	}
	if topicID != nil {
		if err := w.WriteField("message_thread_id", strconv.FormatInt(*topicID, 10)); err != nil {
			return nil, "", encodeErr
		}
	}
	part, err := w.CreateFormFile("photo", filename)
	if err != nil {
		return nil, "", encodeErr
	}
	if _, err := part.Write(imgData); err != nil {
		return nil, "", encodeErr
	}
	if err := w.Close(); err != nil {
		return nil, "", encodeErr
	}
	return &body, w.FormDataContentType(), nil
}

// postSendPhoto does the actual HTTP round trip and envelope parsing —
// split out of SendPhoto only to keep that function's own complexity below
// the linter's threshold; the two are never called separately.
func (c *Client) postSendPhoto(ctx context.Context, body *bytes.Buffer, contentType string) error {
	ctx, cancel := context.WithTimeout(ctx, doCallTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/sendPhoto", c.config.BaseURL, c.config.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return &APIError{Method: "sendPhoto", Message: "build sendPhoto request failed"}
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Never include err.Error() here — it can contain the request URL,
		// which contains the bot token.
		return &APIError{Method: "sendPhoto", Message: "telegram sendPhoto request failed"}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil || len(respBody) == 0 {
		return &APIError{Method: "sendPhoto", Message: "empty response from sendPhoto"}
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return &APIError{Method: "sendPhoto", Message: "telegram sendPhoto returned an unparseable response"}
	}
	if !envelope.OK {
		apiErr := &APIError{Method: "sendPhoto", Message: fmt.Sprintf("telegram sendPhoto failed: %s", envelope.Description), ErrorCode: envelope.ErrorCode}
		if envelope.Parameters != nil {
			apiErr.RetryAfter = envelope.Parameters.RetryAfter
		}
		return apiErr
	}
	return nil
}

type InlineButton struct {
	Text         string  `json:"text"`
	CallbackData *string `json:"callback_data,omitempty"`
	URL          *string `json:"url,omitempty"`
}

type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

func button(text, callbackData string) InlineButton {
	cd := callbackData
	return InlineButton{Text: text, CallbackData: &cd}
}

// urlButton opens url in the user's browser/Telegram client instead of
// firing a callback_query — used for t.me deep links (see dmDeepLink),
// since a bot can't programmatically open a chat on the user's behalf.
func urlButton(text, url string) InlineButton {
	u := url
	return InlineButton{Text: text, URL: &u}
}
