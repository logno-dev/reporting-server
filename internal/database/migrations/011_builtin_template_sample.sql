DROP TRIGGER template_versions_immutable ON template_versions;

UPDATE template_versions tv
SET sample_data = '{
  "sample": {
    "id": "26000123",
    "description": "Example sample"
  },
  "results": [
    {
      "test": "APC",
      "result": "<10"
    },
    {
      "test": "Salmonella",
      "result": "ND"
    }
  ]
}'::jsonb
FROM templates t
WHERE t.id = tv.template_id
  AND t.slug = 'certificate-of-analysis'
  AND tv.version = 1
  AND tv.status = 'published'
  AND tv.sample_data = '{}'::jsonb;

CREATE TRIGGER template_versions_immutable
BEFORE UPDATE OR DELETE ON template_versions
FOR EACH ROW EXECUTE FUNCTION prevent_published_template_changes();
