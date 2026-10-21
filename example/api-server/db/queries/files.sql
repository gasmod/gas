-- name: CreateFile :one
INSERT INTO files (user_id, name, size, content_type, storage_key)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, name, size, content_type, storage_key, created_at;

-- name: DeleteFile :exec
DELETE FROM files WHERE id = $1 AND user_id = $2;

-- name: GetFileByID :one
SELECT id, user_id, name, size, content_type, storage_key, created_at
FROM files
WHERE id = $1;

-- name: ListFilesByUser :many
SELECT id, user_id, name, size, content_type, storage_key, created_at
FROM files
WHERE user_id = $1
ORDER BY created_at DESC;
