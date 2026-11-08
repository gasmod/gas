CREATE TABLE __gas_auth_tokens
(
    id         TEXT PRIMARY KEY,
    subject    TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    purpose    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);

CREATE INDEX idx___gas_auth_tokens_token_hash ON __gas_auth_tokens (token_hash);
CREATE INDEX idx___gas_auth_tokens_subject_purpose ON __gas_auth_tokens (subject, purpose);
