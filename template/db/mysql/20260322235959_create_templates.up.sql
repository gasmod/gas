CREATE TABLE __gas_templates
(
    id         BIGINT AUTO_INCREMENT PRIMARY KEY,
    namespace  VARCHAR(255) NOT NULL,
    name       VARCHAR(255) NOT NULL,
    content    MEDIUMBLOB   NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT NOW(),
    updated_at DATETIME     NOT NULL DEFAULT NOW(),
    UNIQUE KEY uq___gas_templates_namespace_name (namespace, name)
);

CREATE INDEX idx___gas_templates_namespace ON __gas_templates (namespace);
