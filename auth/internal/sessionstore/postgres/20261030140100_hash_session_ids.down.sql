-- The stored hashes can never match a raw ID, so every session is deleted.
DELETE FROM __gas_auth_sessions;

ALTER TABLE __gas_auth_sessions RENAME COLUMN id_hash TO id;
