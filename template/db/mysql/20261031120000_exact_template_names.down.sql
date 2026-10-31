-- Strict mode makes the ALTER TABLE fail, and change nothing, where a
-- name does not fit the database's default character set or a content
-- MEDIUMBLOB, instead of storing it cut short or replaced by `?`.
SET SESSION sql_mode = CONCAT_WS(',', NULLIF(@@SESSION.sql_mode, ''), 'STRICT_ALL_TABLES');

ALTER TABLE __gas_templates
    MODIFY namespace VARCHAR(255) NOT NULL,
    MODIFY name      VARCHAR(255) NOT NULL,
    MODIFY content   MEDIUMBLOB   NOT NULL,
    ADD INDEX idx___gas_templates_namespace (namespace);
