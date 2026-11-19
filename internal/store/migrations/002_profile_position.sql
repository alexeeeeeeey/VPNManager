-- +goose Up
ALTER TABLE profiles ADD COLUMN source_position INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE profiles DROP COLUMN source_position;
