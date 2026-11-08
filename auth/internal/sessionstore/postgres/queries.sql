-- name: GetSession :one
SELECT id_hash, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active
FROM __gas_auth_sessions
WHERE id_hash = $1;

-- name: InsertSession :exec
INSERT INTO __gas_auth_sessions (id_hash, subject, metadata, ip_address, user_agent, created_at, expires_at, last_active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ExtendSession :exec
UPDATE __gas_auth_sessions
SET expires_at  = $2,
    last_active = $3
WHERE id_hash = $1;

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
WHERE id_hash = $1;

-- name: DeleteSessionsBySubject :exec
DELETE
FROM __gas_auth_sessions
WHERE subject = $1;

-- name: DeleteExpiredSessions :execrows
DELETE
FROM __gas_auth_sessions
WHERE expires_at <= $1
   OR created_at <= $2;
