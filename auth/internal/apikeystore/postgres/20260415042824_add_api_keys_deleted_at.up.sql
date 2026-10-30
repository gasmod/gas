ALTER TABLE __gas_auth_api_keys
    ADD COLUMN deleted_at TIMESTAMPTZ;

CREATE INDEX idx___gas_auth_api_keys_subject_active
    ON __gas_auth_api_keys (subject)
    WHERE deleted_at IS NULL;
