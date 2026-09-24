ALTER TABLE template_versions
    ADD COLUMN data_schema jsonb NOT NULL DEFAULT '{"additionalProperties":true,"type":"object"}'::jsonb,
    ADD COLUMN schema_sha256 char(64) NOT NULL DEFAULT '82ef96cebaf5fbe16269fd18b0240d78f5b9b90a4155a17eb797115b09148ecf';

ALTER TABLE template_versions
    ADD CONSTRAINT template_versions_schema_sha256_check
    CHECK (schema_sha256 ~ '^[0-9a-f]{64}$');

COMMENT ON COLUMN template_versions.data_schema IS 'Generated report input contract; permissive for versions published before contract analysis';
