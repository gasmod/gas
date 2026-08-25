-- name: GetTokenByHash :one
SELECT id, subject, purpose, expires_at
FROM __gas_auth_tokens
WHERE token_hash = ?;

-- name: InsertToken :exec
INSERT INTO __gas_auth_tokens (id, subject, token_hash, purpose, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

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
