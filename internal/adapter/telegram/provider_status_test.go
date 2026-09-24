package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// fakeSyncStateRepo is a minimal enrichment.SyncStateRepository — only
// State is exercised by providerStatusView.
type fakeSyncStateRepo struct {
	states map[enrichment.Source]*enrichment.SyncState
	err    error
}

func (f *fakeSyncStateRepo) RecordSuccess(context.Context, enrichment.Source) error { return nil }
func (f *fakeSyncStateRepo) RecordFailure(context.Context, enrichment.Source, string) error {
	return nil
}
func (f *fakeSyncStateRepo) State(_ context.Context, provider enrichment.Source) (*enrichment.SyncState, error) {
	if f.err != nil {
		return nil, f.err
	}
	if st, ok := f.states[provider]; ok {
		return st, nil
	}
	return &enrichment.SyncState{Provider: provider}, nil
}

func providerStatusMsg(userID int64) *Message {
	text := "/provider_status"
	return &Message{Chat: Chat{ID: userID, Type: "private"}, From: &User{ID: userID}, Text: &text}
}

func TestProviderStatus_DeniedForNonRootOperator(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.EnrichmentState = &fakeSyncStateRepo{}
	handler.EnrichmentSources = []enrichment.Source{enrichment.SourceValveVRS}

	if err := handler.handlePrivateMessage(context.Background(), providerStatusMsg(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(findSendMessageText(t, *calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected forbidden reply for a non-root user, got %q", findSendMessageText(t, *calls))
	}
}

func TestProviderStatus_RootOperatorSeesNeverSyncedProvider(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.EnrichmentState = &fakeSyncStateRepo{}
	handler.EnrichmentSources = []enrichment.Source{enrichment.SourceHLTV}

	if err := handler.handlePrivateMessage(context.Background(), providerStatusMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, string(enrichment.SourceHLTV)) {
		t.Fatalf("expected the HLTV source named, got %q", body)
	}
	if !strings.Contains(body, ru(t, "providers.never")) {
		t.Fatalf("expected a never-synced provider to say so, got %q", body)
	}
	if !strings.Contains(body, "⚪") {
		t.Fatalf("expected the unknown-status icon for a never-synced provider, got %q", body)
	}
}

func TestProviderStatus_ShowsFailureDetails(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	lastError := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	handler.EnrichmentState = &fakeSyncStateRepo{states: map[enrichment.Source]*enrichment.SyncState{
		enrichment.SourceHLTV: {Provider: enrichment.SourceHLTV, LastErrorAt: &lastError, LastError: "apify: HTTP 401", ConsecutiveFailures: 3},
	}}
	handler.EnrichmentSources = []enrichment.Source{enrichment.SourceHLTV}

	if err := handler.handlePrivateMessage(context.Background(), providerStatusMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, "🔴") {
		t.Fatalf("expected the down-status icon for a failing provider, got %q", body)
	}
	if !strings.Contains(body, "apify: HTTP 401") {
		t.Fatalf("expected the last error text, got %q", body)
	}
	if !strings.Contains(body, "3") {
		t.Fatalf("expected the consecutive-failure count, got %q", body)
	}
}

func TestProviderStatus_StateLookupErrorIsSurfacedNotSwallowed(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.EnrichmentState = &fakeSyncStateRepo{err: context.DeadlineExceeded}
	handler.EnrichmentSources = []enrichment.Source{enrichment.SourceHLTV}

	if err := handler.handlePrivateMessage(context.Background(), providerStatusMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "providers.status_error")) {
		t.Fatalf("expected the status-error text for a failed lookup, got %q", body)
	}
}

func TestProviderStatus_EmptyWhenNothingConfigured(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}

	if err := handler.handlePrivateMessage(context.Background(), providerStatusMsg(1)); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "providers.empty")) {
		t.Fatalf("expected the empty-state text when no gateway/sources are wired, got %q", body)
	}
}

// --- formatting units (no gateway construction needed) ---

func TestFormatCompetitionProviderStatus_RendersStatusAndDetails(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	got := formatCompetitionProviderStatus(texts, common.LocaleRU, app.HealthDown,
		map[string]any{"lastSuccess": "2026-09-01T00:00:00Z"}, now)
	if !strings.Contains(got, "PandaScore") {
		t.Fatalf("expected the provider name, got %q", got)
	}
	if !strings.Contains(got, "🔴") {
		t.Fatalf("expected the down icon, got %q", got)
	}
	// Three hours before "now", said as an age rather than as a timestamp
	// somebody has to subtract in their head.
	if !strings.Contains(got, ru(t, "providers.hours", 3)) {
		t.Fatalf("expected the age of the last success, got %q", got)
	}
}

// A feed's freshness only means something against how often it is supposed
// to run: three hours is an outage for a fifteen-minute poll and normal for
// a weekly fetch. The screen has to say which it is.
func TestProviderStatus_JudgesFreshnessAgainstTheConfiguredInterval(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	now := handler.Clock.Now()
	threeHoursAgo := now.Add(-3 * time.Hour)
	st := &enrichment.SyncState{Provider: enrichment.SourceGRID, LastSuccessAt: &threeHoursAgo}

	handler.EnrichmentIntervals = map[enrichment.Source]time.Duration{enrichment.SourceGRID: 15 * time.Minute}
	overdue := handler.enrichmentRow(common.LocaleRU, enrichment.SourceGRID, st)
	if overdue.severity != severityDown {
		t.Fatalf("a feed three hours late on a fifteen-minute schedule reads as healthy: %q", overdue.text)
	}
	if !strings.Contains(overdue.text, ru(t, "providers.overdue", humanAge(handler.Texts, common.LocaleRU, 165*time.Minute))) {
		t.Fatalf("expected the screen to say how overdue it is, got %q", overdue.text)
	}

	handler.EnrichmentIntervals = map[enrichment.Source]time.Duration{enrichment.SourceGRID: 7 * 24 * time.Hour}
	fine := handler.enrichmentRow(common.LocaleRU, enrichment.SourceGRID, st)
	if fine.severity != severityOK {
		t.Fatalf("the same timestamp on a weekly schedule must be fine: %q", fine.text)
	}

	// The source is named the way somebody says it, not the way the
	// database spells it.
	if strings.Contains(fine.text, string(enrichment.SourceGRID)) && providerName(enrichment.SourceValveVRS) != "Valve VRS" {
		t.Fatalf("provider names are still raw constants: %q", fine.text)
	}
}
