-- name: GetKeyByHash :one
SELECT *
FROM __gas_auth_api_keys
WHERE key_hash = $1
  AND deleted_at IS NULL;

-- name: UpdateLastUsed :exec
UPDATE __gas_auth_api_keys
SET last_used = $2
WHERE id = $1;

-- name: InsertKey :exec
INSERT INTO __gas_auth_api_keys (id, subject, name, key_hash, key_prefix, scopes, metadata, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: SoftDeleteKeyByID :exec
UPDATE __gas_auth_api_keys
SET deleted_at = $2
WHERE id = $1
  AND deleted_at IS NULL;

-- name: SoftDeleteKeysBySubject :exec
UPDATE __gas_auth_api_keys
SET deleted_at = $2
WHERE subject = $1
  AND deleted_at IS NULL;

-- name: HardDeleteKeyByID :exec
DELETE
FROM __gas_auth_api_keys
WHERE id = $1;

-- name: HardDeleteKeysBySubject :exec
DELETE
FROM __gas_auth_api_keys
WHERE subject = $1;

-- name: ListKeysBySubject :many
SELECT *
FROM __gas_auth_api_keys
WHERE subject = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: ListAllKeysBySubject :many
SELECT *
FROM __gas_auth_api_keys
WHERE subject = $1
ORDER BY created_at DESC;
