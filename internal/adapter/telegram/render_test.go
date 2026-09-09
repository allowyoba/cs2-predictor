package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs2predictor/internal/platform/common"
)

func TestEscapeHTML_EscapesOnlyAmpLtGt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Na'Vi & FaZe", "Na'Vi &amp; FaZe"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"Alex \"the best\"", `Alex "the best"`}, // quotes are NOT escaped in Telegram's HTML mode
		{"plain text", "plain text"},
		{"Александр", "Александр"}, // Cyrillic passes through untouched
	}
	for _, tc := range cases {
		if got := escapeHTML(tc.in); got != tc.want {
			t.Errorf("escapeHTML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// newRespondTestHandler builds a minimal UpdateHandler wired to a
// recording+scriptable HTTP server, for testing respond()/send() directly
// without going through the full command/callback dispatch.
func newRespondTestHandler(t *testing.T, handlerFunc http.HandlerFunc) (*UpdateHandler, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["__path"] = r.URL.Path
		calls = append(calls, body)
		if handlerFunc != nil {
			handlerFunc(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	return &UpdateHandler{Client: client}, &calls
}

func TestRespond_SendTargetAlwaysSendsNewMessage(t *testing.T) {
	h, calls := newRespondTestHandler(t, nil)
	target := sendTarget(common.ChatID{Value: -1}, nil)

	if err := h.respond(context.Background(), target, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	if !strings.Contains((*calls)[0]["__path"].(string), "sendMessage") {
		t.Fatalf("expected sendMessage, got path %v", (*calls)[0]["__path"])
	}
	if (*calls)[0]["parse_mode"] != "HTML" {
		t.Fatalf("expected parse_mode HTML, got %v", (*calls)[0]["parse_mode"])
	}
}

func TestRespond_EditTargetEditsInPlace(t *testing.T) {
	h, calls := newRespondTestHandler(t, nil)
	msgID := int64(42)
	target := replyTarget{chatID: common.ChatID{Value: -1}, messageID: &msgID}

	if err := h.respond(context.Background(), target, "updated", nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	if !strings.Contains((*calls)[0]["__path"].(string), "editMessageText") {
		t.Fatalf("expected editMessageText, got path %v", (*calls)[0]["__path"])
	}
	if (*calls)[0]["message_id"] != float64(msgID) {
		t.Fatalf("message_id = %v, want %d", (*calls)[0]["message_id"], msgID)
	}
}

// TestRespond_NotModifiedIsTreatedAsSuccess covers a double-tap on the same
// button: editMessageText with byte-identical content fails with Telegram's
// "message is not modified" — respond must swallow that, not surface it as
// an error the caller has to handle.
func TestRespond_NotModifiedIsTreatedAsSuccess(t *testing.T) {
	h, _ := newRespondTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`))
	})
	msgID := int64(1)
	target := replyTarget{chatID: common.ChatID{Value: -1}, messageID: &msgID}

	if err := h.respond(context.Background(), target, "same text", nil); err != nil {
		t.Fatalf("expected 'message is not modified' to be swallowed as success, got %v", err)
	}
}

// TestRespond_EditUnavailableFallsBackToSendingNew covers a message that
// can no longer be edited (deleted, or older than Telegram's 48h edit
// window): respond must fall back to sending a brand-new message instead
// of just failing, so navigation still works even off a stale screen.
func TestRespond_EditUnavailableFallsBackToSendingNew(t *testing.T) {
	first := true
	h, calls := newRespondTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if first {
			first = false
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
	})
	msgID := int64(99)
	target := replyTarget{chatID: common.ChatID{Value: -1}, messageID: &msgID}

	if err := h.respond(context.Background(), target, "fallback text", nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls (failed edit + fallback send), got %d: %+v", len(*calls), *calls)
	}
	if !strings.Contains((*calls)[0]["__path"].(string), "editMessageText") {
		t.Fatalf("call 1 should be the failed edit attempt, got %v", (*calls)[0]["__path"])
	}
	if !strings.Contains((*calls)[1]["__path"].(string), "sendMessage") {
		t.Fatalf("call 2 should be the fallback send, got %v", (*calls)[1]["__path"])
	}
}

// TestRespond_OtherEditErrorsPropagate ensures respond doesn't swallow
// genuine failures (e.g. a transport error) beyond the two specific,
// recognized non-error cases above.
func TestRespond_OtherEditErrorsPropagate(t *testing.T) {
	h, _ := newRespondTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	})
	msgID := int64(1)
	target := replyTarget{chatID: common.ChatID{Value: -1}, messageID: &msgID}

	if err := h.respond(context.Background(), target, "text", nil); err == nil {
		t.Fatal("expected an unrecognized edit error to propagate, not be swallowed")
	}
}
