CREATE TABLE api_clients (
    id char(26) PRIMARY KEY CHECK (id ~ '^[0-9A-HJKMNP-TV-Z]{26}$'),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    enabled boolean NOT NULL DEFAULT true,
    scopes text[] NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text NOT NULL CHECK (length(btrim(created_by)) > 0),
    disabled_at timestamptz,
    disabled_by text,
    CHECK (cardinality(scopes) > 0),
    CHECK (scopes <@ ARRAY['templates:read', 'reports:submit', 'reports:read', 'reports:download']::text[]),
    CHECK (enabled OR (disabled_at IS NOT NULL AND disabled_by IS NOT NULL))
);

CREATE UNIQUE INDEX api_clients_name_idx ON api_clients (lower(name));
CREATE INDEX api_clients_created_at_idx ON api_clients (created_at DESC);

CREATE TABLE api_keys (
    id char(26) PRIMARY KEY CHECK (id ~ '^[0-9A-HJKMNP-TV-Z]{26}$'),
    client_id char(26) NOT NULL REFERENCES api_clients(id),
    label text NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 120),
    prefix text NOT NULL CHECK (prefix = 'rpt_live_' || id || '_'),
    secret_hash bytea NOT NULL UNIQUE CHECK (octet_length(secret_hash) = 32),
    scopes text[] NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    issued_by text NOT NULL CHECK (length(btrim(issued_by)) > 0),
    expires_at timestamptz,
    revoked_at timestamptz,
    revoked_by text,
    last_used_at timestamptz,
    last_used_ip inet,
    replaces_id char(26) REFERENCES api_keys(id),
    replaced_by_id char(26) REFERENCES api_keys(id),
    CHECK (cardinality(scopes) > 0),
    CHECK (scopes <@ ARRAY['templates:read', 'reports:submit', 'reports:read', 'reports:download']::text[]),
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    CHECK (replaces_id IS NULL OR replaces_id <> id),
    CHECK (replaced_by_id IS NULL OR replaced_by_id <> id)
);

CREATE INDEX api_keys_client_issued_idx ON api_keys (client_id, issued_at DESC);
CREATE INDEX api_keys_active_idx ON api_keys (client_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE api_credential_audit_events (
    id char(26) PRIMARY KEY CHECK (id ~ '^[0-9A-HJKMNP-TV-Z]{26}$'),
    client_id char(26) REFERENCES api_clients(id),
    key_id char(26) REFERENCES api_keys(id),
    action text NOT NULL CHECK (action IN ('client.created', 'client.disabled', 'key.issued', 'key.rotated', 'key.revoked')),
    actor text NOT NULL CHECK (length(btrim(actor)) > 0),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX api_credential_audit_client_idx ON api_credential_audit_events (client_id, occurred_at DESC);

CREATE FUNCTION prevent_api_credential_audit_changes() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'API credential audit events are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER api_credential_audit_append_only
BEFORE UPDATE OR DELETE ON api_credential_audit_events
FOR EACH ROW EXECUTE FUNCTION prevent_api_credential_audit_changes();
