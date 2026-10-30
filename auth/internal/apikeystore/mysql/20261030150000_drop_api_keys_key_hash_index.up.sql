-- key_hash is UNIQUE, and the unique constraint has an index of its own,
-- so this second index only slowed writes.
DROP INDEX idx___gas_auth_api_keys_key_hash ON __gas_auth_api_keys;
