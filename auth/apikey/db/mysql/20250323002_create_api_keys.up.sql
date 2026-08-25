CREATE TABLE __gas_auth_api_keys
(
    id         VARCHAR(255) PRIMARY KEY,
    subject    VARCHAR(255) NOT NULL,
    name       VARCHAR(255) NOT NULL DEFAULT '',
    key_hash   VARCHAR(255) NOT NULL UNIQUE,
    key_prefix VARCHAR(255) NOT NULL,
    scopes     TEXT         NOT NULL DEFAULT '',
    metadata   JSON         NOT NULL,
    last_used  DATETIME,
    expires_at DATETIME,
    created_at DATETIME     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx___gas_auth_api_keys_subject ON __gas_auth_api_keys (subject);
CREATE INDEX idx___gas_auth_api_keys_key_hash ON __gas_auth_api_keys (key_hash);
