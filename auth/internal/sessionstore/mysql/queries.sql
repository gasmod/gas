-- name: GetSession :one
SELECT id_hash, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active
FROM __gas_auth_sessions
WHERE id_hash = ?;

-- name: InsertSession :exec
INSERT INTO __gas_auth_sessions (id_hash, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ExtendSession :exec
UPDATE __gas_auth_sessions
SET expires_at  = ?,
    last_active = ?
WHERE id_hash = ?;

-- name: RotateSession :execrows
UPDATE __gas_auth_sessions
SET id_hash     = sqlc.arg(new_id_hash),
    ip_address  = sqlc.arg(ip_address),
    user_agent  = sqlc.arg(user_agent),
    expires_at  = sqlc.arg(expires_at),
    last_active = sqlc.arg(last_active)
WHERE id_hash = sqlc.arg(old_id_hash);

-- name: DeleteSession :execrows
DELETE
FROM __gas_auth_sessions
WHERE id_hash = ?;

-- name: ListSessionsBySubject :many
SELECT id_hash, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active
FROM __gas_auth_sessions
WHERE subject = ?
ORDER BY created_at DESC, id_hash DESC;

-- name: DeleteSessionOfSubject :execrows
DELETE
FROM __gas_auth_sessions
WHERE id_hash = ?
  AND subject = ?;

-- name: DeleteOtherSessionsOfSubject :exec
DELETE
FROM __gas_auth_sessions
WHERE subject = ?
  AND id_hash <> ?;

-- name: DeleteSessionsBySubject :exec
DELETE
FROM __gas_auth_sessions
WHERE subject = ?;

-- name: DeleteExpiredSessions :execrows
DELETE
FROM __gas_auth_sessions
WHERE expires_at <= ?
   OR created_at <= ?;
