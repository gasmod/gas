CREATE TABLE __gas_auth_sessions
(
    id          TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,
    metadata    TEXT NOT NULL DEFAULT '{}',
    ip_address  TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at  TEXT NOT NULL,
    last_active TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx___gas_auth_sessions_subject ON __gas_auth_sessions (subject);
CREATE INDEX idx___gas_auth_sessions_expires_at ON __gas_auth_sessions (expires_at);
