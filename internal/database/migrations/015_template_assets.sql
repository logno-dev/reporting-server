ALTER TABLE template_versions
    ADD COLUMN assets_revision bigint NOT NULL DEFAULT 0 CHECK (assets_revision >= 0);

ALTER TABLE storage_objects DROP CONSTRAINT storage_objects_object_type_check;
ALTER TABLE storage_objects DROP CONSTRAINT storage_objects_check;
ALTER TABLE storage_objects
    ADD CONSTRAINT storage_objects_object_type_check
    CHECK (object_type IN ('template', 'report', 'template_asset'));
ALTER TABLE storage_objects
    ADD CONSTRAINT storage_objects_owner_check
    CHECK (
        (object_type = 'report' AND report_job_id IS NOT NULL AND template_id IS NULL AND template_version IS NULL)
        OR
        (object_type = 'template' AND report_job_id IS NULL AND template_id IS NOT NULL AND template_version IS NOT NULL)
        OR
        (object_type = 'template_asset' AND report_job_id IS NULL AND template_id IS NULL AND template_version IS NULL
            AND sha256 IS NOT NULL AND size_bytes IS NOT NULL)
    );

CREATE TABLE template_version_assets (
    template_id bigint NOT NULL,
    template_version integer NOT NULL,
    filename text NOT NULL CHECK (
        filename <> '' AND length(filename) <= 128 AND filename NOT IN ('.', '..')
        AND position('/' IN filename) = 0 AND position(chr(92) IN filename) = 0
    ),
    object_id char(26) NOT NULL REFERENCES storage_objects(id) ON DELETE RESTRICT,
    content_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text,
    PRIMARY KEY (template_id, template_version, filename),
    FOREIGN KEY (template_id, template_version)
        REFERENCES template_versions(template_id, version) ON DELETE CASCADE
);

CREATE INDEX template_version_assets_object_idx ON template_version_assets (object_id);

CREATE FUNCTION enforce_template_asset_manifest() RETURNS trigger AS $$
DECLARE
    owner_template_id bigint;
    owner_version integer;
    version_status text;
    asset_type text;
BEGIN
    owner_template_id := COALESCE(NEW.template_id, OLD.template_id);
    owner_version := COALESCE(NEW.template_version, OLD.template_version);

    SELECT status INTO version_status
    FROM template_versions
    WHERE template_id = owner_template_id AND version = owner_version;

    -- A missing parent is only possible here during its ON DELETE CASCADE.
    IF TG_OP = 'DELETE' AND version_status IS NULL THEN
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE' AND (OLD.template_id, OLD.template_version) IS DISTINCT FROM (NEW.template_id, NEW.template_version) THEN
        RAISE EXCEPTION 'template asset manifest ownership is immutable';
    END IF;

    IF version_status IS DISTINCT FROM 'draft' THEN
        RAISE EXCEPTION 'template assets may only be changed on drafts';
    END IF;

    IF TG_OP <> 'DELETE' THEN
        SELECT object_type INTO asset_type FROM storage_objects WHERE id = NEW.object_id;
        IF asset_type IS DISTINCT FROM 'template_asset' THEN
            RAISE EXCEPTION 'template asset manifest must reference a template asset object';
        END IF;
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER template_version_assets_mutable_draft
BEFORE INSERT OR UPDATE OR DELETE ON template_version_assets
FOR EACH ROW EXECUTE FUNCTION enforce_template_asset_manifest();
