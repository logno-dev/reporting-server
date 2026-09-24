CREATE TABLE report_outbox (
    job_id char(26) PRIMARY KEY REFERENCES report_jobs(id) ON DELETE CASCADE,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_by text,
    locked_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX report_outbox_available_idx
ON report_outbox (available_at, created_at);
