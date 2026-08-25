-- name: InsertToken :exec
INSERT INTO __gas_auth_tokens (id, subject, token_hash, purpose, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ConsumeTokenByHash :one
DELETE
FROM __gas_auth_tokens
WHERE token_hash = $1
RETURNING id, subject, purpose, expires_at;

-- name: DeleteTokenByHash :execrows
DELETE
FROM __gas_auth_tokens
WHERE token_hash = $1;

-- name: DeleteTokensBySubjectPurpose :exec
DELETE
FROM __gas_auth_tokens
WHERE subject = $1
  AND purpose = $2;

-- name: DeleteExpiredTokens :execrows
DELETE
FROM __gas_auth_tokens
WHERE expires_at < $1;
