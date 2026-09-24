ALTER TABLE report_jobs
    ADD COLUMN retry_parent_id char(26) REFERENCES report_jobs(id) ON DELETE RESTRICT,
    ADD COLUMN retry_idempotency_key text,
    ADD COLUMN processing_worker_id text,
    ADD COLUMN processing_lease_until timestamptz;

ALTER TABLE report_jobs
    ADD CONSTRAINT report_jobs_retry_not_self CHECK (retry_parent_id IS NULL OR retry_parent_id <> id),
    ADD CONSTRAINT report_jobs_retry_key_shape CHECK (
        retry_idempotency_key IS NULL OR
        (retry_parent_id IS NOT NULL AND length(retry_idempotency_key) BETWEEN 1 AND 200)
    ),
    ADD CONSTRAINT report_jobs_processing_lease_shape CHECK (
        (processing_worker_id IS NULL) = (processing_lease_until IS NULL)
    );

CREATE INDEX report_jobs_retry_parent_idx ON report_jobs (retry_parent_id, created_at);
CREATE INDEX report_jobs_requested_by_idx ON report_jobs (requested_by, created_at DESC);
CREATE INDEX report_jobs_template_version_idx ON report_jobs (template_id, template_version, created_at DESC);
CREATE INDEX report_jobs_processing_lease_idx ON report_jobs (processing_lease_until)
WHERE status = 'processing';
CREATE UNIQUE INDEX report_jobs_retry_idempotency_idx
ON report_jobs (retry_parent_id, requested_by, retry_idempotency_key)
WHERE retry_idempotency_key IS NOT NULL;

CREATE FUNCTION prevent_report_lineage_changes() RETURNS trigger AS $$
BEGIN
    IF NEW.retry_parent_id IS DISTINCT FROM OLD.retry_parent_id OR
       NEW.retry_idempotency_key IS DISTINCT FROM OLD.retry_idempotency_key THEN
        RAISE EXCEPTION 'report retry lineage is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER report_lineage_immutable
BEFORE UPDATE ON report_jobs
FOR EACH ROW EXECUTE FUNCTION prevent_report_lineage_changes();

CREATE TABLE report_attempt_events (
    id bigserial PRIMARY KEY,
    report_job_id char(26) NOT NULL REFERENCES report_jobs(id) ON DELETE RESTRICT,
    attempt integer NOT NULL CHECK (attempt >= 0),
    event_type text NOT NULL CHECK (event_type IN ('started', 'retry', 'failed', 'completed', 'recovered')),
    worker_id text,
    actor text,
    message text,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CHECK (worker_id IS NOT NULL OR actor IS NOT NULL)
);

CREATE INDEX report_attempt_events_job_idx
ON report_attempt_events (report_job_id, occurred_at, id);

CREATE FUNCTION prevent_report_attempt_event_changes() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'report attempt events are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER report_attempt_events_append_only
BEFORE UPDATE OR DELETE ON report_attempt_events
FOR EACH ROW EXECUTE FUNCTION prevent_report_attempt_event_changes();

CREATE TABLE worker_heartbeats (
    worker_id text PRIMARY KEY CHECK (length(btrim(worker_id)) > 0),
    started_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    concurrency integer NOT NULL CHECK (concurrency > 0),
    renderer_version text NOT NULL,
    current_jobs integer NOT NULL DEFAULT 0 CHECK (current_jobs >= 0),
    CHECK (last_seen_at >= started_at)
);

CREATE INDEX worker_heartbeats_last_seen_idx ON worker_heartbeats (last_seen_at DESC);
