package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// fakeTournamentMetadataRepo is a minimal
// enrichment.TournamentMetadataRepository for the eventDetails test below.
type fakeTournamentMetadataRepo struct {
	byEvent map[common.EventID]enrichment.TournamentMetadata
	err     error
}

func (f *fakeTournamentMetadataRepo) SaveTournamentMetadata(_ context.Context, eventID common.EventID, meta enrichment.TournamentMetadata, _ enrichment.Source) error {
	if f.byEvent == nil {
		f.byEvent = map[common.EventID]enrichment.TournamentMetadata{}
	}
	f.byEvent[eventID] = meta
	return nil
}

func (f *fakeTournamentMetadataRepo) FindTournamentMetadata(_ context.Context, eventID common.EventID, _ enrichment.Source) (*enrichment.TournamentMetadata, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.byEvent[eventID]; ok {
		return &m, nil
	}
	return nil, nil
}

// The event detail card already fetches and caches Liquipedia tournament
// metadata (region/series) via TournamentMetadataSync, but never rendered
// it anywhere — this is the fix: eventDetails now shows it when available.
func TestEventDetails_ShowsLiquipediaRegionAndSeriesWhenCached(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	eventID := common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "IEM Cologne"}}}
	handler.TournamentMetadata = &fakeTournamentMetadataRepo{byEvent: map[common.EventID]enrichment.TournamentMetadata{
		eventID: {Series: "IEM", Region: "Europe"},
	}}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	if err := handler.eventDetails(context.Background(), sendTarget(settings.ChatID, nil), settings, eventID, false); err != nil {
		t.Fatal(err)
	}
	text := findSendMessageText(t, *calls)
	if !strings.Contains(text, "IEM") || !strings.Contains(text, "Europe") {
		t.Fatalf("expected the Liquipedia region/series line, got %q", text)
	}
}

// No cached metadata (Liquipedia disabled, or simply no match yet) must
// omit the line entirely, not show an empty/broken one.
func TestEventDetails_OmitsLiquipediaLineWhenNothingCached(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	eventID := common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "IEM Cologne"}}}
	handler.TournamentMetadata = &fakeTournamentMetadataRepo{}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	if err := handler.eventDetails(context.Background(), sendTarget(settings.ChatID, nil), settings, eventID, false); err != nil {
		t.Fatal(err)
	}
	text := findSendMessageText(t, *calls)
	if strings.Contains(text, "📚") {
		t.Fatalf("expected no Liquipedia line when nothing is cached, got %q", text)
	}
}

func TestEventDetails_NilTournamentMetadataRepoOmitsLineWithoutErroring(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	eventID := common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "IEM Cologne"}}}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	if err := handler.eventDetails(context.Background(), sendTarget(settings.ChatID, nil), settings, eventID, false); err != nil {
		t.Fatal(err)
	}
	text := findSendMessageText(t, *calls)
	if strings.Contains(text, "📚") {
		t.Fatalf("expected no Liquipedia line when the repository isn't configured, got %q", text)
	}
}
