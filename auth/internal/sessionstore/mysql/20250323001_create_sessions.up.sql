CREATE TABLE __gas_auth_sessions
(
    id          VARCHAR(255) PRIMARY KEY,
    subject     VARCHAR(255) NOT NULL,
    metadata    JSON         NOT NULL,
    ip_address  VARCHAR(255) NOT NULL DEFAULT '',
    user_agent  TEXT         NOT NULL,
    created_at  DATETIME     NOT NULL DEFAULT NOW(),
    expires_at  DATETIME     NOT NULL,
    last_active DATETIME     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx___gas_auth_sessions_subject ON __gas_auth_sessions (subject);
CREATE INDEX idx___gas_auth_sessions_expires_at ON __gas_auth_sessions (expires_at);
