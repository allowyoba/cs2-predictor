package telegram

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
)

// WebhookHandler is the POST /telegram/webhook entry point — a direct port
// of TelegramWebhookController: constant-time secret check (missing header
// treated as empty, so a blank configured secret always rejects), 204 on
// success, 401 on a bad/missing secret.
type WebhookHandler struct {
	config  Config
	updates *UpdateHandler
}

func NewWebhookHandler(config Config, updates *UpdateHandler) *WebhookHandler {
	return &WebhookHandler{config: config, updates: updates}
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	expected := []byte(h.config.WebhookSecret)
	actual := []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	if len(expected) == 0 || subtle.ConstantTimeCompare(expected, actual) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// A non-nil error here means an unexpected failure escaped the
	// per-command error handling (expected errors like "access denied" are
	// already caught and replied-to inside Handle) — respond 5xx so
	// Telegram retries delivery of this update later.
	if err := h.updates.Handle(r.Context(), update); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
