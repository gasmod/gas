ALTER TABLE __gas_auth_api_keys
    ADD COLUMN deleted_at DATETIME NULL;

CREATE INDEX idx___gas_auth_api_keys_subject_deleted
    ON __gas_auth_api_keys (subject, deleted_at);
