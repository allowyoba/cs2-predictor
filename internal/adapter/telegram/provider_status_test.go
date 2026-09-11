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
	got := formatCompetitionProviderStatus(texts, common.LocaleRU, app.HealthDown,
		map[string]any{"lastSuccess": "2026-09-01T00:00:00Z", "lastFailure": "pandascore: timeout"})
	if !strings.Contains(got, "PandaScore") || !strings.Contains(got, "DOWN") {
		t.Fatalf("expected provider name and status, got %q", got)
	}
	if !strings.Contains(got, "pandascore: timeout") {
		t.Fatalf("expected the last-failure detail, got %q", got)
	}
	if !strings.Contains(got, "🔴") {
		t.Fatalf("expected the down icon, got %q", got)
	}
}

func TestFormatEnrichmentProviderStatus_HealthyProviderShowsUpIcon(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	success := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	st := &enrichment.SyncState{Provider: enrichment.SourceValveVRS, LastSuccessAt: &success}
	got := formatEnrichmentProviderStatus(texts, common.LocaleRU, enrichment.SourceValveVRS, st)
	if !strings.Contains(got, "🟢") {
		t.Fatalf("expected the up icon for a healthy provider, got %q", got)
	}
	if !strings.Contains(got, "08.09.2026") {
		t.Fatalf("expected the formatted last-success date, got %q", got)
	}
}
