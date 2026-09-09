package telegram

import (
	"fmt"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Small display helpers shared by every screen — the counterpart to
// render.go, which is about getting a message out rather than composing
// one.

// mentionHTML renders u as an HTML text-mention link (Telegram's documented
// "tg://user?id=..." anchor syntax) — the HTML-mode equivalent of a
// text_mention entity, and the only way to produce one for a user who may
// not have a @username.
func mentionHTML(u User) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, u.ID, escapeHTML(u.DisplayName()))
}

// ternary is a tiny helper so short locale-dependent literals (a label, a
// unit word) can be written inline instead of as a 4-line if/else — used
// only for one-word/one-phrase literals, never for user-controlled data.
func ternary(cond bool, ifTrue, ifFalse string) string {
	if cond {
		return ifTrue
	}
	return ifFalse
}

func tournamentMode(topTierOnly bool, locale common.LocaleCode) string {
	if locale == common.LocaleRU {
		return ternary(topTierOnly, "только S/A", "все")
	}
	return ternary(topTierOnly, "S/A only", "all")
}

var monthNamesRU = [...]string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

// weekdayNamesRU is indexed by time.Weekday, so Sunday comes first — the
// order the standard library uses, not the order a Russian calendar is
// printed in.
var weekdayNamesRU = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

func shortWeekdayName(d time.Weekday, locale common.LocaleCode) string {
	if locale == common.LocaleRU {
		return weekdayNamesRU[d]
	}
	return d.String()[:3]
}

func chatZone(settings chat.Settings) *time.Location {
	return chat.ZoneOrDefault(settings.Timezone)
}
