-- name: GetAppliedMigrations :many
SELECT version,
       service,
       description,
       migrate_version,
       module_version,
       applied_at,
       dirty
FROM __gas_migrations
ORDER BY version;

-- name: GetDirtyMigrations :many
SELECT version,
       service,
       description,
       migrate_version,
       module_version,
       applied_at,
       dirty
FROM __gas_migrations
WHERE dirty = TRUE
ORDER BY version;

-- name: MarkMigrationApplied :exec
INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version)
VALUES ($1, $2, $3, $4, $5);

-- name: MarkMigrationDirty :exec
INSERT INTO __gas_migrations (version, service, description, migrate_version, module_version, dirty)
VALUES ($1, $2, $3, $4, $5, TRUE)
ON CONFLICT (version) DO UPDATE SET dirty = TRUE;

-- name: RemoveMigration :exec
DELETE
FROM __gas_migrations
WHERE version = $1;
