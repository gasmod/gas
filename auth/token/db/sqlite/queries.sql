-- name: InsertToken :exec
INSERT INTO __gas_auth_tokens (id, subject, token_hash, purpose, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ConsumeTokenByHash :one
DELETE
FROM __gas_auth_tokens
WHERE token_hash = ?
RETURNING id, subject, purpose, expires_at;

-- name: DeleteTokenByHash :execrows
DELETE
FROM __gas_auth_tokens
WHERE token_hash = ?;

-- name: DeleteTokensBySubjectPurpose :exec
DELETE
FROM __gas_auth_tokens
WHERE subject = ?
  AND purpose = ?;

-- name: DeleteExpiredTokens :execrows
DELETE
FROM __gas_auth_tokens
WHERE expires_at < ?;
