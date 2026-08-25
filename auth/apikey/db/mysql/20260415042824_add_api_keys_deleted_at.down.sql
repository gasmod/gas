DROP INDEX idx___gas_auth_api_keys_subject_deleted ON __gas_auth_api_keys;

ALTER TABLE __gas_auth_api_keys
    DROP COLUMN deleted_at;
