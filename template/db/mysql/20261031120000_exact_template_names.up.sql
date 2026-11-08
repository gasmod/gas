-- Strict mode makes the ALTER TABLE fail, and change nothing, where a
-- value would not fit the new column, instead of storing it cut short.
SET SESSION sql_mode = CONCAT_WS(',', NULLIF(@@SESSION.sql_mode, ''), 'STRICT_ALL_TABLES');

ALTER TABLE __gas_templates
    MODIFY namespace VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    MODIFY name      VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
    MODIFY content   LONGBLOB     NOT NULL,
    DROP INDEX idx___gas_templates_namespace;
