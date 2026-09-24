ALTER TABLE storage_migration_items
    ADD COLUMN source_placement_id char(26) REFERENCES storage_object_placements(id),
    ADD COLUMN storage_key text,
    ADD COLUMN expected_sha256 char(64),
    ADD COLUMN expected_size_bytes bigint CHECK (expected_size_bytes IS NULL OR expected_size_bytes >= 0),
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN lease_until timestamptz;

UPDATE storage_migration_items i
SET source_placement_id = p.id,
    storage_key = p.storage_key,
    expected_sha256 = COALESCE(p.sha256, o.sha256),
    expected_size_bytes = COALESCE(p.size_bytes, o.size_bytes)
FROM storage_migrations m, storage_object_placements p, storage_objects o
WHERE m.id = i.migration_id
  AND p.object_id = i.object_id
  AND p.profile_id = m.source_profile_id
  AND p.state = 'available'
  AND o.id = i.object_id;

DELETE FROM storage_migration_items WHERE source_placement_id IS NULL;

ALTER TABLE storage_migration_items
    ALTER COLUMN source_placement_id SET NOT NULL,
    ALTER COLUMN storage_key SET NOT NULL;

CREATE INDEX storage_migration_items_dispatch_idx
ON storage_migration_items (available_at, migration_id)
WHERE state IN ('pending', 'failed');

CREATE TABLE storage_profile_dispatch (
    profile_id char(26) PRIMARY KEY REFERENCES storage_profiles(id),
    next_available_at timestamptz NOT NULL DEFAULT now()
);
