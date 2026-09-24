CREATE TABLE templates (
    id bigserial PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE template_versions (
    template_id bigint NOT NULL REFERENCES templates(id),
    version integer NOT NULL CHECK (version > 0),
    status text NOT NULL CHECK (status IN ('draft', 'published')),
    source text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    PRIMARY KEY (template_id, version),
    CHECK ((status = 'published') = (published_at IS NOT NULL))
);

CREATE FUNCTION prevent_published_template_changes() RETURNS trigger AS $$
BEGIN
    IF OLD.status = 'published' THEN
        RAISE EXCEPTION 'published template versions are immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER template_versions_immutable
BEFORE UPDATE OR DELETE ON template_versions
FOR EACH ROW EXECUTE FUNCTION prevent_published_template_changes();

CREATE TABLE report_jobs (
    id char(26) PRIMARY KEY,
    template_id bigint NOT NULL REFERENCES templates(id),
    template_version integer NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
    data jsonb NOT NULL,
    data_sha256 char(64) NOT NULL,
    requested_by text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    error text,
    storage_key text,
    sha256 char(64),
    pages integer,
    renderer_version text,
    FOREIGN KEY (template_id, template_version) REFERENCES template_versions(template_id, version)
);

CREATE INDEX report_jobs_created_at_idx ON report_jobs (created_at DESC);
CREATE INDEX report_jobs_status_idx ON report_jobs (status);

INSERT INTO templates (slug, name) VALUES ('certificate-of-analysis', 'Certificate of Analysis');

INSERT INTO template_versions (template_id, version, status, source, published_at)
SELECT id, 1, 'published', $typst$
#import "common/lab.typ": *
#let data = json("report.json")

#lab-report(title: "Certificate of Analysis")
#section("Sample Information")
#field("Sample ID", data.sample.id)
#field("Description", data.sample.description)
#section("Results")
#results-table(data.results)
$typst$, now()
FROM templates WHERE slug = 'certificate-of-analysis';
