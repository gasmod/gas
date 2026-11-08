CREATE TABLE __gas_auth_api_keys
(
    id         TEXT PRIMARY KEY,
    subject    TEXT        NOT NULL,
    name       TEXT        NOT NULL DEFAULT '',
    key_hash   TEXT        NOT NULL UNIQUE,
    key_prefix TEXT        NOT NULL,
    scopes     TEXT[]      NOT NULL DEFAULT '{}',
    metadata   JSONB       NOT NULL DEFAULT '{}',
    last_used  TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx___gas_auth_api_keys_subject ON __gas_auth_api_keys (subject);
CREATE INDEX idx___gas_auth_api_keys_key_hash ON __gas_auth_api_keys (key_hash);
