-- name: CreateAPIKey :one
INSERT INTO api_keys (user_id, key_hash, name)
VALUES ($1, $2, $3)
RETURNING id, user_id, name, created_at;

-- name: DeleteAPIKey :exec
DELETE FROM api_keys WHERE id = $1 AND user_id = $2;

-- name: GetAPIKeyByHash :one
SELECT ak.id, ak.user_id, ak.key_hash, ak.name, ak.created_at,
       u.email AS user_email
FROM api_keys ak
JOIN users u ON u.id = ak.user_id
WHERE ak.key_hash = $1;

-- name: ListAPIKeysByUser :many
SELECT id, user_id, name, created_at
FROM api_keys
WHERE user_id = $1
ORDER BY created_at DESC;
