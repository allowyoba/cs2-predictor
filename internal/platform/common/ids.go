// Package common holds cross-cutting types and ports shared by every domain
// package: identifiers, the cluster-lock/idempotency/outbox ports, and
// notification DTOs. It has no dependency on any adapter or the app package.
package common

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// EventID identifies a tournament event. Generated app-side via NewEventID,
// never DB-generated.
type EventID struct{ Value uuid.UUID }

func NewEventID() EventID { return EventID{Value: uuid.New()} }

// MatchID identifies a single match within an event.
type MatchID struct{ Value uuid.UUID }

func NewMatchID() MatchID { return MatchID{Value: uuid.New()} }

// TeamID identifies a team.
type TeamID struct{ Value uuid.UUID }

func NewTeamID() TeamID { return TeamID{Value: uuid.New()} }

// PollID identifies a prediction poll.
type PollID struct{ Value uuid.UUID }

func NewPollID() PollID { return PollID{Value: uuid.New()} }

// RequestID identifies a pending confirmable request (e.g. an unsubscribe
// awaiting a second manager's approval).
type RequestID struct{ Value uuid.UUID }

func NewRequestID() RequestID { return RequestID{Value: uuid.New()} }
func (r RequestID) String() string {
	return strings.ReplaceAll(r.Value.String(), "-", "")
}

// ParseRequestID accepts both the compact (no-dashes) form String() produces,
// used when a RequestID is embedded in Telegram callback_data with no room
// to spare, and a standard dashed UUID. uuid.Parse handles both forms.
func ParseRequestID(s string) (RequestID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return RequestID{}, err
	}
	return RequestID{Value: id}, nil
}

// ChatID wraps Telegram's native chat id. Group/supergroup chat ids are
// negative in Telegram's own numbering.
type ChatID struct{ Value int64 }

// UserID wraps Telegram's native user id.
type UserID struct{ Value int64 }

func (u UserID) String() string { return strconv.FormatInt(u.Value, 10) }
func (c ChatID) String() string { return strconv.FormatInt(c.Value, 10) }

// LocaleCode is the bot's supported UI locale. The zero value is not a valid
// locale — use RU or EN. The app defaults to RU wherever a locale isn't
// yet known.
type LocaleCode string

const (
	LocaleRU LocaleCode = "RU"
	LocaleEN LocaleCode = "EN"
)

// Tag returns the IETF language tag used for i18n resource lookup and
// java.time.format-style locale-aware formatting.
func (l LocaleCode) Tag() string {
	if l == LocaleEN {
		return "en-US"
	}
	return "ru-RU"
}

// Language returns the ISO 639-1 alpha-2 code for this locale — the form
// data providers use for stream languages, as opposed to Tag's IETF form.
func (l LocaleCode) Language() string {
	if l == LocaleEN {
		return "en"
	}
	return "ru"
}

// LocaleFrom matches value case-insensitively against either the enum name
// ("RU"/"EN") or the IETF tag ("ru-RU"/"en-US"), defaulting to RU if value is
// empty or unrecognized.
func LocaleFrom(value string) LocaleCode {
	switch value {
	case "EN", "en", "en-US", "en-us":
		return LocaleEN
	default:
		return LocaleRU
	}
}
