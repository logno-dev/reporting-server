CREATE TABLE storage_profiles (
    id char(26) PRIMARY KEY,
    name text NOT NULL UNIQUE,
    backend_type text NOT NULL CHECK (backend_type IN ('filesystem', 's3')),
    state text NOT NULL CHECK (state IN ('active', 'disabled')),
    public_config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(public_config) = 'object'),
    encrypted_credentials bytea,
    request_rate_limit integer CHECK (request_rate_limit IS NULL OR request_rate_limit > 0),
    transfer_concurrency integer CHECK (transfer_concurrency IS NULL OR transfer_concurrency > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION prevent_storage_profile_changes() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'storage profiles are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER storage_profiles_immutable
BEFORE UPDATE OR DELETE ON storage_profiles
FOR EACH ROW EXECUTE FUNCTION prevent_storage_profile_changes();

CREATE TABLE storage_defaults (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile_id char(26) NOT NULL REFERENCES storage_profiles(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE storage_objects (
    id char(26) PRIMARY KEY,
    object_type text NOT NULL CHECK (object_type IN ('template', 'report')),
    logical_key text NOT NULL,
    report_job_id char(26) UNIQUE REFERENCES report_jobs(id) ON DELETE CASCADE,
    template_id bigint,
    template_version integer,
    sha256 char(64),
    size_bytes bigint CHECK (size_bytes IS NULL OR size_bytes >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (template_id, template_version) REFERENCES template_versions(template_id, version) ON DELETE CASCADE,
    CHECK (
        (object_type = 'report' AND report_job_id IS NOT NULL AND template_id IS NULL AND template_version IS NULL)
        OR
        (object_type = 'template' AND report_job_id IS NULL AND template_id IS NOT NULL AND template_version IS NOT NULL)
    ),
    UNIQUE (template_id, template_version),
    UNIQUE (object_type, logical_key)
);

CREATE TABLE storage_object_placements (
    id char(26) PRIMARY KEY,
    object_id char(26) NOT NULL REFERENCES storage_objects(id) ON DELETE CASCADE,
    profile_id char(26) NOT NULL REFERENCES storage_profiles(id),
    storage_key text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'available', 'failed', 'deleting')),
    sha256 char(64),
    size_bytes bigint CHECK (size_bytes IS NULL OR size_bytes >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    verified_at timestamptz,
    error text,
    UNIQUE (object_id, profile_id)
);

CREATE INDEX storage_object_placements_profile_idx
ON storage_object_placements (profile_id, state);

CREATE TABLE storage_migrations (
    id char(26) PRIMARY KEY,
    source_profile_id char(26) NOT NULL REFERENCES storage_profiles(id),
    destination_profile_id char(26) NOT NULL REFERENCES storage_profiles(id),
    state text NOT NULL CHECK (state IN ('draft', 'running', 'paused', 'completed', 'canceled', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    error text,
    CHECK (source_profile_id <> destination_profile_id)
);

CREATE TABLE storage_migration_items (
    migration_id char(26) NOT NULL REFERENCES storage_migrations(id) ON DELETE CASCADE,
    object_id char(26) NOT NULL REFERENCES storage_objects(id) ON DELETE CASCADE,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'completed', 'skipped', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    started_at timestamptz,
    completed_at timestamptz,
    error text,
    PRIMARY KEY (migration_id, object_id)
);

CREATE INDEX storage_migration_items_work_idx
ON storage_migration_items (migration_id, state);
