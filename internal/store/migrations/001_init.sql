-- +goose Up
CREATE TABLE subscriptions (
  id TEXT PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  url TEXT NOT NULL UNIQUE,
  refresh_hours INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT '',
  attempted_at INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE profiles (
  id TEXT PRIMARY KEY NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  host TEXT NOT NULL,
  source_id TEXT NOT NULL,
  config TEXT NOT NULL,
  available INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX profiles_by_source ON profiles(source_id);
CREATE TABLE settings (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL);
-- +goose Down
DROP TABLE settings;
DROP TABLE profiles;
DROP TABLE subscriptions;
