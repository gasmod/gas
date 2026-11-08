CREATE TABLE __gas_auth_sessions
(
    id          TEXT PRIMARY KEY,
    subject     TEXT        NOT NULL,
    metadata    JSONB       NOT NULL DEFAULT '{}',
    ip_address  TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    last_active TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx___gas_auth_sessions_subject ON __gas_auth_sessions (subject);
CREATE INDEX idx___gas_auth_sessions_expires_at ON __gas_auth_sessions (expires_at);
