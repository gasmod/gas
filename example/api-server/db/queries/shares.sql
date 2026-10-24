-- name: CreateShare :one
INSERT INTO shares (file_id, user_id, token, recipient_email, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, file_id, user_id, token, recipient_email, expires_at, created_at;

-- name: GetShareByToken :one
SELECT s.id, s.file_id, s.user_id, s.token, s.recipient_email, s.expires_at, s.created_at,
       f.name AS file_name, f.size AS file_size, f.content_type AS file_content_type, f.storage_key AS file_storage_key
FROM shares s
JOIN files f ON f.id = s.file_id
WHERE s.token = $1;

-- name: ListSharesByFile :many
SELECT id, file_id, user_id, token, recipient_email, expires_at, created_at
FROM shares
WHERE file_id = $1
ORDER BY created_at DESC;
