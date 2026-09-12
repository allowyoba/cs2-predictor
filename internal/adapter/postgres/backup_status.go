package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/platform/common"
)

// BackupStatusRepository reads db_backup_log — written only by the deploy
// pipeline (ansible/roles/database_backup), never by this application; see
// that table's own migration comment for why.
type BackupStatusRepository struct {
	pool *pgxpool.Pool
}

func NewBackupStatusRepository(pool *pgxpool.Pool) *BackupStatusRepository {
	return &BackupStatusRepository{pool: pool}
}

var _ common.BackupStatusRepository = (*BackupStatusRepository)(nil)

func (r *BackupStatusRepository) RecentBackups(ctx context.Context, limit int) ([]common.BackupRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT label, storage, size_bytes, created_at FROM db_backup_log ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []common.BackupRecord
	for rows.Next() {
		var rec common.BackupRecord
		if err := rows.Scan(&rec.Label, &rec.Storage, &rec.SizeBytes, &rec.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
