CREATE TABLE __gas_migrations
(
    version         TEXT PRIMARY KEY,
    service         TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    migrate_version TEXT    NOT NULL DEFAULT '',
    module_version  TEXT    NOT NULL DEFAULT '',
    applied_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    dirty           INTEGER NOT NULL DEFAULT 0
);
