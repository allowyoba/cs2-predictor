package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

// fakeBackupStatusRepo is a minimal common.BackupStatusRepository — only
// RecentBackups is exercised by backupStatusView.
type fakeBackupStatusRepo struct {
	records []common.BackupRecord
	err     error
}

func (f *fakeBackupStatusRepo) RecentBackups(_ context.Context, limit int) ([]common.BackupRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.records) > limit {
		return f.records[:limit], nil
	}
	return f.records, nil
}

func backupStatusCB(userID int64) *CallbackQuery {
	data := "hub:backup_status"
	return &CallbackQuery{ID: "cb", From: User{ID: userID, FirstName: "Alex"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: userID, Type: "private"}}, Data: &data}
}

func TestBackupStatus_DeniedForNonRootOperator(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.BackupStatus = &fakeBackupStatusRepo{}

	if err := handler.handleCallback(context.Background(), backupStatusCB(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected forbidden reply for a non-root user, got %q", lastText(*calls))
	}
}

func TestBackupStatus_NilRepositoryShowsEmpty(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}

	if err := handler.handleCallback(context.Background(), backupStatusCB(1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "backup.empty")) {
		t.Fatalf("expected the empty-backups message when BackupStatus is nil, got %q", lastText(*calls))
	}
}

func TestBackupStatus_EmptyHistoryShowsEmpty(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	handler.BackupStatus = &fakeBackupStatusRepo{}

	if err := handler.handleCallback(context.Background(), backupStatusCB(1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "backup.empty")) {
		t.Fatalf("expected the empty-backups message, got %q", lastText(*calls))
	}
}

func TestBackupStatus_RootOperatorSeesRecentBackups(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.TeamMatchOperatorChatIDs = []int64{1}
	size := int64(482_355_712)
	handler.BackupStatus = &fakeBackupStatusRepo{records: []common.BackupRecord{
		{Label: "cs2predictor-20260912-220615", Storage: "s3", SizeBytes: &size, CreatedAt: time.Date(2026, 9, 12, 22, 6, 15, 0, time.UTC)},
		{Label: "cs2predictor-20260911-220615", Storage: "fs", SizeBytes: nil, CreatedAt: time.Date(2026, 9, 11, 22, 6, 15, 0, time.UTC)},
	}}

	if err := handler.handleCallback(context.Background(), backupStatusCB(1)); err != nil {
		t.Fatal(err)
	}
	body := lastText(*calls)
	for _, want := range []string{"cs2predictor-20260912-220615", "460.0", "cs2predictor-20260911-220615", ru(t, "backup.size_unknown")} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the backup status screen, got %q", want, body)
		}
	}
}

func TestFormatByteSize_RendersTheLargestReadableUnit(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		512:           "512 B",
		1024:          "1.0 KB",
		1_048_576:     "1.0 MB",
		482_355_712:   "460.0 MB",
		1_500_000_000: "1.4 GB",
	}
	for bytes, want := range cases {
		if got := formatByteSize(bytes); got != want {
			t.Fatalf("formatByteSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}
