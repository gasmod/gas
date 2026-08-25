CREATE TABLE IF NOT EXISTS __gas_migrations
(
    version         VARCHAR(255) PRIMARY KEY,
    service         TEXT         NOT NULL,
    description     TEXT         NOT NULL,
    migrate_version TEXT         NOT NULL,
    module_version  TEXT         NOT NULL,
    applied_at      DATETIME     NOT NULL DEFAULT NOW(),
    dirty           BOOLEAN      NOT NULL DEFAULT FALSE
)
