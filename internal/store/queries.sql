-- name: Profiles :many
SELECT * FROM profiles
ORDER BY source_id, available DESC, source_position,
  CASE WHEN source_id != '' AND source_position = 0 THEN rowid END,
  name COLLATE NOCASE, id;
-- name: ProfileByID :one
SELECT * FROM profiles WHERE id = ?;
-- name: UpsertProfile :exec
INSERT INTO profiles(id,name,kind,host,source_id,config,available,source_position) VALUES (?,?,?,?,?,?,1,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, host=excluded.host, config=excluded.config, available=1, source_position=excluded.source_position;
-- name: MarkMissing :exec
UPDATE profiles SET available=0 WHERE source_id=?;
-- name: DeleteMissing :exec
DELETE FROM profiles WHERE available=0 AND source_id=? AND id != ?
AND NOT EXISTS (
  SELECT 1 FROM settings, json_each(settings.value, '$.presets') AS preset
  WHERE settings.key='routing_policy' AND json_extract(preset.value, '$.profileId')=profiles.id
);
-- name: DeleteProfile :exec
DELETE FROM profiles WHERE id=?;
-- name: DeleteSourceProfiles :exec
DELETE FROM profiles WHERE source_id=?;
-- name: UpdateProfileConfig :exec
UPDATE profiles SET config=?, kind='JSON' WHERE id=?;
-- name: Subscriptions :many
SELECT * FROM subscriptions ORDER BY name COLLATE NOCASE, id;
-- name: SubscriptionByID :one
SELECT * FROM subscriptions WHERE id=?;
-- name: UpsertSubscription :exec
INSERT INTO subscriptions(id,name,url,refresh_hours) VALUES (?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, refresh_hours=excluded.refresh_hours;
-- name: SubscriptionResult :exec
UPDATE subscriptions SET updated_at=?, attempted_at=?, error=? WHERE id=?;
-- name: DeleteSubscription :exec
DELETE FROM subscriptions WHERE id=?;
-- name: Setting :one
SELECT value FROM settings WHERE key=?;
-- name: SetSetting :exec
INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value;
