ALTER TABLE template_versions ADD COLUMN storage_key text;

DROP TRIGGER template_versions_immutable ON template_versions;

UPDATE template_versions tv
SET storage_key = 'templates/' || t.slug || '/v' || tv.version || '/main.typ'
FROM templates t
WHERE t.id = tv.template_id;

ALTER TABLE template_versions ALTER COLUMN storage_key SET NOT NULL;

CREATE TRIGGER template_versions_immutable
BEFORE UPDATE OR DELETE ON template_versions
FOR EACH ROW EXECUTE FUNCTION prevent_published_template_changes();
