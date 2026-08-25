-- name: GetKeyByHash :one
SELECT *
FROM __gas_auth_api_keys
WHERE key_hash = ?
  AND deleted_at IS NULL;

-- name: UpdateLastUsed :exec
UPDATE __gas_auth_api_keys
SET last_used = ?
WHERE id = ?;

-- name: InsertKey :exec
INSERT INTO __gas_auth_api_keys (id, subject, name, key_hash, key_prefix, scopes, metadata, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SoftDeleteKeyByID :exec
UPDATE __gas_auth_api_keys
SET deleted_at = ?
WHERE id = ?
  AND deleted_at IS NULL;

-- name: SoftDeleteKeysBySubject :exec
UPDATE __gas_auth_api_keys
SET deleted_at = ?
WHERE subject = ?
  AND deleted_at IS NULL;

-- name: HardDeleteKeyByID :exec
DELETE
FROM __gas_auth_api_keys
WHERE id = ?;

-- name: HardDeleteKeysBySubject :exec
DELETE
FROM __gas_auth_api_keys
WHERE subject = ?;

-- name: ListKeysBySubject :many
SELECT *
FROM __gas_auth_api_keys
WHERE subject = ?
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: ListAllKeysBySubject :many
SELECT *
FROM __gas_auth_api_keys
WHERE subject = ?
ORDER BY created_at DESC;
