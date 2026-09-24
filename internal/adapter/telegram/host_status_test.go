package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cs2predictor/internal/app"
)

// findEditMessageText returns the text of the first editMessageText call —
// what a callback-driven screen like this one actually sends, since it
// edits the panel in place rather than posting a new message.
func findEditMessageText(t *testing.T, calls []map[string]any) string {
	t.Helper()
	for _, c := range calls {
		if c["__method"] == "editMessageText" {
			text, _ := c["text"].(string)
			return text
		}
	}
	t.Fatalf("no editMessageText call found among %+v", calls)
	return ""
}

func TestHostStatus_DeniedForNonRootOperator(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.HostLimits = app.HostLimits{Memory: 0.9, Disk: 0.85, Load: 4}
	handler.HostUsageReader = func(string) (app.HostUsage, error) {
		return app.HostUsage{MemoryUsed: 0.5}, nil
	}

	cb := teamMatchPrivateCB(2, "hub:host_status")
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(findEditMessageText(t, *calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected forbidden reply for a non-root user, got %q", findEditMessageText(t, *calls))
	}
}

func TestHostStatus_RootOperatorSeesFormattedReadingAgainstLimits(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.HostLimits = app.HostLimits{Memory: 0.9, Disk: 0.85, Load: 4}
	handler.HostUsageReader = func(string) (app.HostUsage, error) {
		return app.HostUsage{MemoryUsed: 0.95, DiskUsed: 0.40, LoadPerCPU: 1.5}, nil
	}

	cb := teamMatchPrivateCB(1, "hub:host_status")
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	body := findEditMessageText(t, *calls)

	// Memory is over its 0.90 limit — breached, with the down icon.
	if !strings.Contains(body, "95%") || !strings.Contains(body, ru(t, "host.breached")) {
		t.Fatalf("expected memory shown as breached at 95%%, got %q", body)
	}
	if !strings.Contains(body, "🔴") {
		t.Fatalf("expected the down icon for the breached resource, got %q", body)
	}
	// Disk is well under its 0.85 limit — OK, with the up icon.
	if !strings.Contains(body, "40%") || !strings.Contains(body, ru(t, "host.ok")) {
		t.Fatalf("expected disk shown as OK at 40%%, got %q", body)
	}
	if !strings.Contains(body, "🟢") {
		t.Fatalf("expected the up icon for a healthy resource, got %q", body)
	}
	// Load is rendered as a bare number, not a percentage.
	if !strings.Contains(body, "1.50") {
		t.Fatalf("expected the load-per-cpu reading rendered as a bare number, got %q", body)
	}
}

func TestHostStatus_UnavailableReadIsHandledGracefully(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.HostUsageReader = func(string) (app.HostUsage, error) {
		return app.HostUsage{}, errors.New("statfs: permission denied")
	}

	cb := teamMatchPrivateCB(1, "hub:host_status")
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	body := findEditMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "host.unavailable")) {
		t.Fatalf("expected the unavailable-reading text on a read error, got %q", body)
	}
	// Must not show a zero-valued reading as if it were real.
	if strings.Contains(body, "0%") {
		t.Fatalf("a failed read must not render as a real 0%% reading, got %q", body)
	}
}

func TestHostStatus_DefaultReaderIsAppReadHostUsage(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.HostLimits = app.HostLimits{Memory: 0.9, Disk: 0.85, Load: 4}
	handler.HostRoot = "/"
	// No HostUsageReader set: falls back to app.ReadHostUsage against the
	// real machine, which either succeeds (CI runs on Linux) or reports the
	// unavailable state — both are handled, neither panics.
	cb := teamMatchPrivateCB(1, "hub:host_status")
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if findEditMessageText(t, *calls) == "" {
		t.Fatalf("expected some response")
	}
}
