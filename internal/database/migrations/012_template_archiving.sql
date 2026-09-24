ALTER TABLE templates
    ADD COLUMN archived_at timestamptz,
    ADD COLUMN archived_by text;

CREATE INDEX templates_active_idx ON templates (name, slug) WHERE archived_at IS NULL;
