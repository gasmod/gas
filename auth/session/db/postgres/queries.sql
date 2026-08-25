-- name: GetSession :one
SELECT id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active
FROM __gas_auth_sessions
WHERE id = $1;

-- name: InsertSession :exec
INSERT INTO __gas_auth_sessions (id, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ExtendSession :exec
UPDATE __gas_auth_sessions
SET expires_at  = $2,
    last_active = $3
WHERE id = $1;

-- name: DeleteSession :exec
DELETE
FROM __gas_auth_sessions
WHERE id = $1;

-- name: DeleteSessionsBySubject :exec
DELETE
FROM __gas_auth_sessions
WHERE subject = $1;

-- name: DeleteExpiredSessions :execrows
DELETE
FROM __gas_auth_sessions
WHERE expires_at < $1;
