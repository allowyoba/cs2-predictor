package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

type adminAlertOutbox struct {
	fakeOutbox
	messages []common.AdminAlertNotification
	err      error
}

func (r *adminAlertOutbox) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if eventType != "telegram.admin-alert" {
		return uuid.Nil, nil
	}
	var n common.AdminAlertNotification
	if err := json.Unmarshal([]byte(payload), &n); err != nil {
		return uuid.Nil, err
	}
	r.messages = append(r.messages, n)
	return uuid.Nil, nil
}

type fakeReleaseStore struct {
	claimed map[string]bool
	err     error
}

func (f *fakeReleaseStore) Claim(_ context.Context, version, commit string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	key := version + "-" + commit
	if f.claimed[key] {
		return false, nil
	}
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	f.claimed[key] = true
	return true, nil
}

func newAlerter(outbox *adminAlertOutbox, releases common.ReleaseAnnouncementStore, chatIDs ...int64) *AdminAlerter {
	return &AdminAlerter{Outbox: outbox, Releases: releases, ChatIDs: chatIDs, Log: slog.Default()}
}

// A restart must not re-announce the build it is already running: the claim
// is what makes the announcement once-per-release rather than once-per-boot.
func TestAdminAlerter_AnnouncesEachReleaseOnceToEveryAdmin(t *testing.T) {
	outbox := &adminAlertOutbox{}
	releases := &fakeReleaseStore{claimed: map[string]bool{}}
	alerter := newAlerter(outbox, releases, 10, 20)

	alerter.AnnounceRelease(context.Background(), "v1.17.0", "abc1234")
	alerter.AnnounceRelease(context.Background(), "v1.17.0", "abc1234")

	if len(outbox.messages) != 2 {
		t.Fatalf("expected one message per admin chat and no repeat, got %+v", outbox.messages)
	}
	for i, want := range []int64{10, 20} {
		if outbox.messages[i].ChatID != want || outbox.messages[i].Kind != common.AdminAlertRelease {
			t.Fatalf("message %d = %+v", i, outbox.messages[i])
		}
		if outbox.messages[i].Version != "v1.17.0" {
			t.Fatalf("expected the version in the payload, got %+v", outbox.messages[i])
		}
	}

	// A new build is a new announcement.
	alerter.AnnounceRelease(context.Background(), "v1.18.0", "def5678")
	if len(outbox.messages) != 4 {
		t.Fatalf("expected the next release to be announced, got %d messages", len(outbox.messages))
	}
}

// Nothing configured means nothing sent — that is the opt-out, and it must
// not cost a database round trip either.
func TestAdminAlerter_NoAdminsConfiguredIsASilentNoop(t *testing.T) {
	outbox := &adminAlertOutbox{}
	releases := &fakeReleaseStore{claimed: map[string]bool{}}
	alerter := newAlerter(outbox, releases)

	alerter.AnnounceRelease(context.Background(), "v1.17.0", "abc1234")
	alerter.ProviderDown(context.Background(), "PANDASCORE", 3, "boom")

	if len(outbox.messages) != 0 {
		t.Fatalf("expected no messages, got %+v", outbox.messages)
	}
	if len(releases.claimed) != 0 {
		t.Fatal("expected no release claim when there is nobody to tell")
	}
}

// An enqueue failure is logged, never propagated: every caller is a
// background job or the startup path, and neither may fail over a
// notification.
func TestAdminAlerter_EnqueueFailureIsSwallowed(t *testing.T) {
	alerter := newAlerter(&adminAlertOutbox{err: errors.New("outbox down")}, &fakeReleaseStore{claimed: map[string]bool{}}, 10)
	alerter.AnnounceRelease(context.Background(), "v1.17.0", "abc1234")
	alerter.ProviderRecovered(context.Background(), "HLTV")
}

// fakeSyncState is an in-memory enrichment.SyncStateRepository.
type fakeSyncState struct {
	states map[enrichment.Source]*enrichment.SyncState
}

func newFakeSyncState() *fakeSyncState {
	return &fakeSyncState{states: map[enrichment.Source]*enrichment.SyncState{}}
}

func (f *fakeSyncState) state(provider enrichment.Source) *enrichment.SyncState {
	if _, ok := f.states[provider]; !ok {
		f.states[provider] = &enrichment.SyncState{Provider: provider}
	}
	return f.states[provider]
}

func (f *fakeSyncState) RecordSuccess(_ context.Context, provider enrichment.Source) error {
	st := f.state(provider)
	st.ConsecutiveFailures = 0
	return nil
}

func (f *fakeSyncState) RecordFailure(_ context.Context, provider enrichment.Source, _ string) error {
	f.state(provider).ConsecutiveFailures++
	return nil
}

func (f *fakeSyncState) State(_ context.Context, provider enrichment.Source) (*enrichment.SyncState, error) {
	copied := *f.state(provider)
	return &copied, nil
}

// The alert fires on the crossing into broken and on the way back out —
// never once per failed call, which during an outage would mean one message
// per retry for as long as it lasts.
func TestObservedSyncState_AlertsOnlyOnTransitions(t *testing.T) {
	outbox := &adminAlertOutbox{}
	alerter := newAlerter(outbox, &fakeReleaseStore{claimed: map[string]bool{}}, 10)
	state := &ObservedSyncState{SyncStateRepository: newFakeSyncState(), Observer: alerter, Threshold: 3}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := state.RecordFailure(ctx, enrichment.SourceHLTV, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if len(outbox.messages) != 1 {
		t.Fatalf("expected exactly one down alert across five failures, got %+v", outbox.messages)
	}
	down := outbox.messages[0]
	if down.Kind != common.AdminAlertProviderDown || down.Provider != "HLTV" || down.Failures != 3 {
		t.Fatalf("down alert = %+v", down)
	}

	if err := state.RecordSuccess(ctx, enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	}
	if len(outbox.messages) != 2 || outbox.messages[1].Kind != common.AdminAlertProviderRecovered {
		t.Fatalf("expected a recovery alert, got %+v", outbox.messages)
	}

	// Healthy runs stay quiet.
	if err := state.RecordSuccess(ctx, enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	}
	if len(outbox.messages) != 2 {
		t.Fatalf("a success on a healthy provider must say nothing, got %+v", outbox.messages)
	}
}

// Failures below the threshold are ordinary flakiness and stay in the logs.
func TestObservedSyncState_StaysQuietBelowTheThreshold(t *testing.T) {
	outbox := &adminAlertOutbox{}
	alerter := newAlerter(outbox, &fakeReleaseStore{claimed: map[string]bool{}}, 10)
	state := &ObservedSyncState{SyncStateRepository: newFakeSyncState(), Observer: alerter, Threshold: 3}

	for i := 0; i < 2; i++ {
		if err := state.RecordFailure(context.Background(), enrichment.SourceGRID, "flaky"); err != nil {
			t.Fatal(err)
		}
	}
	if len(outbox.messages) != 0 {
		t.Fatalf("expected silence below the threshold, got %+v", outbox.messages)
	}
	// ...and a success there must not claim a recovery that never happened.
	if err := state.RecordSuccess(context.Background(), enrichment.SourceGRID); err != nil {
		t.Fatal(err)
	}
	if len(outbox.messages) != 0 {
		t.Fatalf("expected no recovery alert without a preceding outage, got %+v", outbox.messages)
	}
}
