package app

import (
	"context"
	"testing"
	"time"

	"log/slog"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The digest scheduler runs every minute against every chat's own local
// time, and the only thing standing between that and a chat being sent the
// same report sixty times is the period claim. These tests cover the
// dispatch decision and that claim.

// fakeReportStore is a ScheduledReportStore backed by a set of claims.
type fakeReportStore struct {
	claims map[string]bool
}

func newFakeReportStore() *fakeReportStore { return &fakeReportStore{claims: map[string]bool{}} }

func (f *fakeReportStore) key(chatID common.ChatID, reportType, periodKey string) string {
	return periodKey + ":" + reportType + ":" + chatID.String()
}
func (f *fakeReportStore) Claimed(_ context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	return f.claims[f.key(chatID, reportType, periodKey)], nil
}
func (f *fakeReportStore) Claim(_ context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	k := f.key(chatID, reportType, periodKey)
	if f.claims[k] {
		return false, nil
	}
	f.claims[k] = true
	return true, nil
}

func newDigestScheduler(t *testing.T, now time.Time, rows []scoring.UserStanding) (*DigestScheduler, *fakeSyncOutbox, *fakeReportStore) {
	t.Helper()
	outbox, store := &fakeSyncOutbox{}, newFakeReportStore()
	return &DigestScheduler{
		Chats:    &fakeSyncChats{active: []chat.Settings{{ChatID: common.ChatID{Value: -1}, Active: true, Timezone: chat.DefaultTimezone}}},
		Scoring:  &fakeScoringForCompletion{leaderboard: rows},
		Store:    store,
		Outbox:   outbox,
		Switches: allNotificationsOn{},
		Lock:     fakeClusterLock{},
		Clock:    fixedClock{now: now},
		RunTx:    identityTx,
		Log:      slog.Default(),
	}, outbox, store
}

// digestStandings is one participant with enough activity to be worth a
// report.
func digestStandings() []scoring.UserStanding {
	return []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "A", Rank: 1, Points: 10, Predictions: 4}}
}

// digestYear is the (arbitrary) year every case here runs in; what matters
// is the day and hour relative to a period boundary.
const digestYear = 2026

// atLocal builds an instant that reads as the given wall-clock time in the
// chat's default timezone — the clock the scheduler actually compares
// against.
func atLocal(t *testing.T, month time.Month, day, hour int) time.Time {
	t.Helper()
	return time.Date(digestYear, month, day, hour, 0, 0, 0, chat.ZoneOrDefault(chat.DefaultTimezone))
}

func TestDigestDispatch_EnqueuesTheMonthlyReportOnceForThePeriod(t *testing.T) {
	scheduler, outbox, _ := newDigestScheduler(t, atLocal(t, time.March, 31, 21), digestStandings())

	scheduler.Dispatch(context.Background())
	// The job runs every minute; the period claim is what keeps that from
	// becoming a stream of identical reports.
	scheduler.Dispatch(context.Background())

	if len(outbox.enqueued) != 1 || outbox.enqueued[0].eventType != "telegram.monthly-digest" {
		t.Fatalf("expected exactly one monthly digest, got %+v", outbox.enqueued)
	}
}

// Mid-month there is nothing to send at all.
func TestDigestDispatch_SendsNothingBeforeTheDeadline(t *testing.T) {
	scheduler, outbox, _ := newDigestScheduler(t, atLocal(t, time.March, 14, 21), digestStandings())

	scheduler.Dispatch(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no digest mid-month, got %+v", outbox.enqueued)
	}
}

// A chat where nobody predicted this month gets no unsolicited empty
// leaderboard — but the period is still claimed, or the scheduler would
// reconsider it every minute until the window closed.
func TestDigestDispatch_ClaimsButStaysSilentForAQuietMonth(t *testing.T) {
	scheduler, outbox, store := newDigestScheduler(t, atLocal(t, time.March, 31, 21), nil)

	scheduler.Dispatch(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no message for a month with no activity, got %+v", outbox.enqueued)
	}
	if claimed, _ := store.Claimed(context.Background(), common.ChatID{Value: -1}, monthlyReportType, "2026-03"); !claimed {
		t.Fatal("the quiet period must still be claimed")
	}
}

// On Dec 31 the December monthly report is folded into the annual one, so
// the chat gets a single message rather than two at the same instant.
func TestDigestDispatch_SendsOnlyTheAnnualReportOnTheLastDayOfTheYear(t *testing.T) {
	scheduler, outbox, _ := newDigestScheduler(t, atLocal(t, time.December, 31, 21), digestStandings())

	scheduler.Dispatch(context.Background())

	if len(outbox.enqueued) != 1 || outbox.enqueued[0].eventType != "telegram.annual-digest" {
		t.Fatalf("expected the annual digest alone, got %+v", outbox.enqueued)
	}
}

// A restart during the deadline hour must not cost the report: the early
// hours of the next day still count as the previous period.
func TestDigestDispatch_RecoversTheReportInTheGraceWindow(t *testing.T) {
	scheduler, outbox, _ := newDigestScheduler(t, atLocal(t, time.April, 1, 3), digestStandings())

	scheduler.Dispatch(context.Background())

	if len(outbox.enqueued) != 1 || outbox.enqueued[0].eventType != "telegram.monthly-digest" {
		t.Fatalf("expected March's digest to still go out in the grace window, got %+v", outbox.enqueued)
	}
}
