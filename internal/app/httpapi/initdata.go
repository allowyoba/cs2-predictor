package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/platform/common"
)

// Telegram Mini App authentication.
//
// A Mini App runs in a WebView the bot does not control, and everything it
// sends can be written by hand. The only thing that cannot be forged is
// Telegram's own signature over the launch parameters, so that signature is
// the whole of the authentication here: no cookies, no sessions, no bearer
// token of our own to leak.
//
// Telegram's scheme (documented under "Validating data received via the
// Mini App"): the query string minus `hash`, sorted by key, joined by "\n",
// HMAC-SHA256'd with a key that is itself HMAC-SHA256("WebAppData", token).
//
// Three things this does that a naive implementation skips, each of which
// is the difference between a check and the appearance of one:
//
//  1. The comparison is constant time. A byte-by-byte compare on a hash
//     leaks it one request at a time.
//  2. auth_date is bounded. A signature stays valid forever otherwise, so
//     initData copied out of somebody's client once works for good.
//  3. The user is read from the signed payload, never from a parameter
//     beside it — signing the launch and then trusting an unsigned
//     `user_id` next to it authenticates nothing.

// ErrInitDataInvalid is returned for anything that fails the check, with no
// detail about which part: a caller probing the difference between "bad
// signature" and "expired" learns something it should not.
var ErrInitDataInvalid = errors.New("invalid init data")

// InitDataUser is the person Telegram says opened the app.
type InitDataUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
}

// InitDataMaxAge bounds how long one launch stays usable. Long enough that
// a slow phone on a stadium network still works, short enough that copied
// initData is worthless by the time it is pasted anywhere.
const InitDataMaxAge = 24 * time.Hour

// VerifyInitData checks Telegram's signature over raw and returns the user
// it names. now is passed in rather than read from the clock so the
// freshness window is testable.
func VerifyInitData(raw, botToken string, now time.Time) (InitDataUser, error) {
	if raw == "" || botToken == "" {
		return InitDataUser{}, ErrInitDataInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return InitDataUser{}, ErrInitDataInvalid
	}
	providedHash := values.Get("hash")
	if providedHash == "" {
		return InitDataUser{}, ErrInitDataInvalid
	}

	// Everything except hash, sorted, "key=value" joined by newlines —
	// Telegram's own definition of the string that was signed.
	pairs := make([]string, 0, len(values))
	for key, list := range values {
		if key == "hash" || len(list) == 0 {
			continue
		}
		pairs = append(pairs, key+"="+list[0])
	}
	sort.Strings(pairs)

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(pairs, "\n")))
	expected := mac.Sum(nil)

	decoded, err := hex.DecodeString(providedHash)
	if err != nil || !hmac.Equal(decoded, expected) {
		return InitDataUser{}, ErrInitDataInvalid
	}

	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return InitDataUser{}, ErrInitDataInvalid
	}
	age := now.Sub(time.Unix(authDate, 0))
	// Both directions: a launch from the future is a clock somebody else
	// controls, which is not a launch this bot should accept either.
	if age > InitDataMaxAge || age < -InitDataMaxAge {
		return InitDataUser{}, ErrInitDataInvalid
	}

	var user InitDataUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return InitDataUser{}, ErrInitDataInvalid
	}
	return user, nil
}

// initDataFromRequest reads the launch parameters a Mini App sends. The
// header is the documented transport; the query string is accepted too,
// because Telegram hands the page its initData in the URL fragment and a
// client that forwards it verbatim is doing the ordinary thing.
func initDataFromRequest(header, query string) string {
	if header != "" {
		return strings.TrimPrefix(header, "tma ")
	}
	return query
}

// authenticatedUser is what a verified launch resolves to.
type authenticatedUser struct {
	ID      common.UserID
	Profile InitDataUser
}

// describeForLog renders the user for a log line without repeating
// anything Telegram would consider personal beyond the id the bot already
// stores everywhere.
func (u authenticatedUser) describeForLog() string {
	return fmt.Sprintf("user %d", u.ID.Value)
}
