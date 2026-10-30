DROP INDEX IF EXISTS idx___gas_auth_api_keys_subject_active;

ALTER TABLE __gas_auth_api_keys
    DROP COLUMN deleted_at;
