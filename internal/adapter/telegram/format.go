package telegram

import (
	"fmt"
	"net/url"
	"strings"
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

// streamPlatforms names the hosts worth showing by brand — a link labelled
// "Twitch" tells someone what they are about to open in a way "Трансляция"
// does not. Anything unlisted falls back to its bare hostname.
var streamPlatforms = map[string]string{
	"twitch.tv":      "Twitch",
	"youtube.com":    "YouTube",
	"youtu.be":       "YouTube",
	"kick.com":       "Kick",
	"vk.com":         "VK Video",
	"vkvideo.ru":     "VK Video",
	"live.vkplay.ru": "VK Play",
	"trovo.live":     "Trovo",
	"nimo.tv":        "Nimo TV",
	"huya.com":       "Huya",
	"douyu.com":      "Douyu",
	"bilibili.com":   "Bilibili",
	"afreecatv.com":  "AfreecaTV",
	"facebook.com":   "Facebook",
}

// streamPlatformLabel labels a stream link by its platform, falling back to
// the hostname and finally — for a URL with no usable host — to fallback,
// the caller's localized "Stream" wording.
func streamPlatformLabel(rawURL, fallback string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return fallback
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	if name, ok := streamPlatforms[host]; ok {
		return name
	}
	// A subdomain of a known platform (m.twitch.tv, live.youtube.com) is
	// still that platform.
	for domain, name := range streamPlatforms {
		if strings.HasSuffix(host, "."+domain) {
			return name
		}
	}
	if host == "" {
		return fallback
	}
	return host
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
