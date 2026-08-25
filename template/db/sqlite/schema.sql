CREATE TABLE __gas_templates
(
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace  TEXT NOT NULL,
    name       TEXT NOT NULL,
    content    BLOB NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (namespace, name)
);

CREATE INDEX idx___gas_templates_namespace ON __gas_templates (namespace);
