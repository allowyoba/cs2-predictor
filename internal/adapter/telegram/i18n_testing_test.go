package telegram

import (
	"sync"
	"testing"

	"cs2predictor/internal/platform/common"
)

// Tests assert on message KEYS, not on the copy those keys resolve to.
// Asserting on the Russian text directly made every wording change break
// unrelated tests, with a failure message that named the old string rather
// than the key that moved — and it silently accepted a screen rendering
// the right words from the wrong key.

var (
	sharedTextsOnce sync.Once
	sharedTexts     *Texts
	sharedTextsErr  error
)

// ru resolves a message key in Russian, the locale every test fixture
// uses. Failing here means the key itself is gone.
func ru(t *testing.T, key string, args ...any) string {
	t.Helper()
	return text(t, common.LocaleRU, key, args...)
}

func text(t *testing.T, locale common.LocaleCode, key string, args ...any) string {
	t.Helper()
	sharedTextsOnce.Do(func() { sharedTexts, sharedTextsErr = LoadTexts() })
	if sharedTextsErr != nil {
		t.Fatalf("load texts: %v", sharedTextsErr)
	}
	resolved := sharedTexts.Get(key, locale, args...)
	if resolved == key {
		t.Fatalf("message key %q does not resolve — the assertion below would pass on a raw key", key)
	}
	return resolved
}
