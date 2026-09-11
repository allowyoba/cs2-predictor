package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The request path is exactly /bot<token>/<method>, and the success
// envelope's "result" is returned.
func TestCall_Success(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "secret-token"}, server.Client())
	result, err := client.Call(context.Background(), "sendMessage", map[string]any{"chat_id": -1, "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/botsecret-token/sendMessage" {
		t.Fatalf("path = %q, want /botsecret-token/sendMessage", gotPath)
	}
	if !strings.Contains(string(result), `"message_id":42`) {
		t.Fatalf("result = %s", result)
	}
}

// Ground truth: a transport-level failure (HTTP 500, no body) never exposes
// the configured token in the resulting error message.
func TestCall_TransportErrorNeverExposesToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "very-secret-token"}, server.Client())
	_, err := client.Call(context.Background(), "sendMessage", map[string]any{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "very-secret-token") {
		t.Fatalf("error message leaked the token: %v", err)
	}
}

func TestAPIError_TopicAndReplyDetection(t *testing.T) {
	topicErr := &APIError{Message: "telegram sendPoll failed: Bad Request: message thread not found"}
	if !topicErr.IsTopicUnavailable() {
		t.Error("expected topic-unavailable detection")
	}
	replyErr := &APIError{Message: "telegram sendMessage failed: Bad Request: message to be replied not found"}
	if !replyErr.IsReplyUnavailable() {
		t.Error("expected reply-unavailable detection")
	}
}

// Ground truth: a 429 is retried automatically, honoring retry_after, and
// succeeds once Telegram stops rate-limiting.
func TestCall_RetriesOn429ThenSucceeds(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "t"}, server.Client())
	var slept []time.Duration
	client.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }

	result, err := client.Call(context.Background(), "sendMessage", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept = %v, want [1s]", slept)
	}
	if !strings.Contains(string(result), `"message_id":1`) {
		t.Fatalf("result = %s", result)
	}
}

// A 429 that never clears gives up after maxRetryAttempts rather than
// retrying forever.
func TestCall_GivesUpAfterMaxRetriesOn429(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "t"}, server.Client())
	client.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Call(context.Background(), "sendMessage", map[string]any{})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if attempts != maxRetryAttempts+1 {
		t.Fatalf("attempts = %d, want %d", attempts, maxRetryAttempts+1)
	}
}

// A non-429 failure (e.g. a validation error) must never be retried.
func TestCall_DoesNotRetryNon429Errors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "t"}, server.Client())
	client.sleep = func(context.Context, time.Duration) error {
		t.Fatal("should not sleep/retry for a non-429 error")
		return nil
	}

	if _, err := client.Call(context.Background(), "sendMessage", map[string]any{}); err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry)", attempts)
	}
}

// retry_after is capped at maxRetryWait so a pathological value can't block
// a scheduler goroutine indefinitely.
func TestCall_CapsExcessiveRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3600}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL, Token: "t"}, server.Client())
	var slept time.Duration
	client.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }

	if _, err := client.Call(context.Background(), "sendMessage", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if slept != maxRetryWait {
		t.Fatalf("slept = %v, want capped at %v", slept, maxRetryWait)
	}
}

// A stalled connection must not hang a request-handling path forever: Call
// applies its own deadline rather than trusting every caller to have set
// one (this bot is webhook-driven, so nothing here is a long-poll call that
// would legitimately need longer).
func TestCall_AppliesItsOwnTimeoutToAStalledRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Long enough to outlast the test's shrunk timeout below, short
		// enough not to hang the suite if this regresses.
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	original := doCallTimeout
	doCallTimeout = 20 * time.Millisecond
	t.Cleanup(func() { doCallTimeout = original })

	client := NewClient(Config{BaseURL: server.URL, Token: "t"}, server.Client())
	start := time.Now()
	_, err := client.Call(context.Background(), "sendMessage", map[string]any{})
	if err == nil {
		t.Fatal("expected the stalled request to time out")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Call took %v, want it bounded by doCallTimeout (with retries)", elapsed)
	}
}
