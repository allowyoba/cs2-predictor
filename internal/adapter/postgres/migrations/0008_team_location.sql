-- +goose Up
ALTER TABLE team ADD COLUMN IF NOT EXISTS location varchar(8);

-- +goose Down
ALTER TABLE team DROP COLUMN IF EXISTS location;
