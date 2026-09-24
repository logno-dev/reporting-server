ALTER TABLE report_jobs
    ADD COLUMN storage_profile_id char(26) REFERENCES storage_profiles(id);

CREATE INDEX report_jobs_storage_profile_idx
ON report_jobs (storage_profile_id)
WHERE storage_profile_id IS NOT NULL;
