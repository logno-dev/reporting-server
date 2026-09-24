ALTER TABLE template_versions DROP CONSTRAINT template_versions_status_check;
ALTER TABLE template_versions
    ADD CONSTRAINT template_versions_status_check
    CHECK (status IN ('draft', 'approved', 'published'));

ALTER TABLE template_versions
    ADD COLUMN sample_data jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN created_by text,
    ADD COLUMN approved_at timestamptz,
    ADD COLUMN approved_by text,
    ADD COLUMN published_by text;

CREATE UNIQUE INDEX template_versions_one_candidate_idx
ON template_versions (template_id)
WHERE status IN ('draft', 'approved');
