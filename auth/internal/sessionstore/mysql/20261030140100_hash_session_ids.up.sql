-- Sessions are now looked up by the SHA-256 of their ID, so no stored raw
-- ID can match again: every session is deleted, which signs everyone out.
DELETE FROM __gas_auth_sessions;

-- MySQL takes a default on a JSON or TEXT column only as an expression
-- (8.0.13 and later); these match the Postgres and SQLite defaults.
ALTER TABLE __gas_auth_sessions
    RENAME COLUMN id TO id_hash,
    MODIFY metadata JSON NOT NULL DEFAULT (JSON_OBJECT()),
    MODIFY user_agent TEXT NOT NULL DEFAULT ('');
