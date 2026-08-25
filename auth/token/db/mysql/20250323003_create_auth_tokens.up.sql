CREATE TABLE __gas_auth_tokens
(
    id         VARCHAR(255) PRIMARY KEY,
    subject    VARCHAR(255) NOT NULL,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    purpose    VARCHAR(255) NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT NOW(),
    expires_at DATETIME     NOT NULL
);

CREATE INDEX idx___gas_auth_tokens_token_hash ON __gas_auth_tokens (token_hash);
CREATE INDEX idx___gas_auth_tokens_subject_purpose ON __gas_auth_tokens (subject, purpose);
