-- +goose Up
-- One row per successful database backup, written directly by the deploy
-- pipeline (ansible/roles/database_backup) via `docker exec ... psql` right
-- after a backup finishes — this application never performs a backup
-- itself, it only displays what the deploy pipeline already recorded (see
-- internal/adapter/telegram/backup_status.go). Failed backups are not
-- logged here: a failed backup already aborts the deploy loudly (the
-- admin-notify pipeline + a failed CI run), so there is no silent-failure
-- gap this table needs to cover — it only needs to answer "when did we
-- last succeed, and how big was it".
CREATE TABLE db_backup_log (
    id         bigserial PRIMARY KEY,
    label      varchar(200) NOT NULL,
    storage    varchar(16) NOT NULL,
    size_bytes bigint,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_db_backup_log_created_at ON db_backup_log(created_at DESC);

-- +goose Down
DROP TABLE db_backup_log;
