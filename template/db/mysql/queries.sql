-- name: GetTemplateContent :one
SELECT content
FROM __gas_templates
WHERE namespace = ?
  AND name = ?;

-- name: ListTemplates :many
SELECT name
FROM __gas_templates
WHERE namespace = ?
ORDER BY name;

-- name: UpsertTemplate :exec
INSERT INTO __gas_templates (namespace, name, content, created_at, updated_at)
VALUES (?, ?, ?, NOW(), NOW())
ON DUPLICATE KEY UPDATE content =
VALUES (content), updated_at = NOW();

-- name: TemplateExists :one
SELECT EXISTS (SELECT 1 FROM __gas_templates WHERE namespace = ? AND name = ?);

-- name: DeleteTemplate :execrows
DELETE
FROM __gas_templates
WHERE namespace = ?
  AND name = ?;
