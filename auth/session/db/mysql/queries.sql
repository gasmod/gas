-- name: GetSession :one
SELECT id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active
FROM __gas_auth_sessions
WHERE id = ?;

-- name: InsertSession :exec
INSERT INTO __gas_auth_sessions (id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ExtendSession :exec
UPDATE __gas_auth_sessions
SET expires_at  = ?,
    last_active = ?
WHERE id = ?;

-- name: DeleteSession :exec
DELETE
FROM __gas_auth_sessions
WHERE id = ?;

-- name: DeleteSessionsBySubject :exec
DELETE
FROM __gas_auth_sessions
WHERE subject = ?;

-- name: DeleteExpiredSessions :execrows
DELETE
FROM __gas_auth_sessions
WHERE expires_at < ?;
