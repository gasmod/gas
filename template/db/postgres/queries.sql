-- name: GetTemplateContent :one
SELECT content
FROM __gas_templates
WHERE namespace = $1
  AND name = $2;

-- name: ListTemplates :many
SELECT name
FROM __gas_templates
WHERE namespace = $1
ORDER BY name;

-- name: UpsertTemplate :exec
INSERT INTO __gas_templates (namespace, name, content, created_at, updated_at)
VALUES ($1, $2, $3, NOW(), NOW())
ON CONFLICT (namespace, name) DO UPDATE SET content    = $3,
                                            updated_at = NOW();

-- name: TemplateExists :one
SELECT EXISTS (SELECT 1 FROM __gas_templates WHERE namespace = $1 AND name = $2);

-- name: DeleteTemplate :execrows
DELETE
FROM __gas_templates
WHERE namespace = $1
  AND name = $2;
