package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/platform/common"
)

// ClusterLock implements common.ClusterLock via pg_try_advisory_lock /
// pg_advisory_unlock, keyed by AdvisoryLockKey(name)
// (see platform/common/javacompat.go).
type ClusterLock struct {
	pool *pgxpool.Pool
}

func NewClusterLock(pool *pgxpool.Pool) *ClusterLock {
	return &ClusterLock{pool: pool}
}

func (l *ClusterLock) Execute(ctx context.Context, name string, action func(ctx context.Context) error) (bool, error) {
	key := common.AdvisoryLockKey(name)

	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	}()

	if err := action(ctx); err != nil {
		return true, err
	}
	return true, nil
}
