package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"reporting-server/internal/database"
	"reporting-server/internal/jobs"
)

func TestRetryLineageAndStaleRecoveryIntegration(t *testing.T) {
	databaseURL := os.Getenv("REPORT_JOBS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set REPORT_JOBS_TEST_DATABASE_URL to run Postgres report operations tests")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	profileID := ulid.Make().String()
	profileName := "report-ops-test-" + profileID
	if _, err := pool.Exec(ctx, `INSERT INTO storage_profiles(id,name,backend_type,state,public_config) VALUES($1,$2,'filesystem','active','{}')`, profileID, profileName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO storage_defaults(singleton,profile_id) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET profile_id=EXCLUDED.profile_id`, profileID); err != nil {
		t.Fatal(err)
	}
	repository := jobs.NewRepository(pool)
	parentID := ulid.Make().String()
	data := json.RawMessage(`{"sample":{"id":"retry-test"}}`)
	if _, err := repository.Create(ctx, parentID, "certificate-of-analysis", nil, nil, data, "api-client:test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.Retry(ctx, parentID, ulid.Make().String(), "admin@example.test", "not-failed"); !errors.Is(err, jobs.ErrNotFailed) {
		t.Fatalf("retry queued report error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE report_jobs SET status='failed',completed_at=now(),error='render failed' WHERE id=$1`, parentID); err != nil {
		t.Fatal(err)
	}
	childID := ulid.Make().String()
	child, replayed, err := repository.Retry(ctx, parentID, childID, "admin@example.test", "retry-key")
	if err != nil || replayed {
		t.Fatalf("Retry() = %+v, replayed=%v, error=%v", child, replayed, err)
	}
	if child.RetryParentID == nil || *child.RetryParentID != parentID || child.DataSHA256 == "" || child.StorageProfileID == nil || *child.StorageProfileID != profileID {
		t.Fatalf("retry did not preserve lineage/data and pin the current profile: %+v", child)
	}
	replayedJob, replayed, err := repository.Retry(ctx, parentID, ulid.Make().String(), "admin@example.test", "retry-key")
	if err != nil || !replayed || replayedJob.ID != childID {
		t.Fatalf("idempotent Retry() = %+v, replayed=%v, error=%v", replayedJob, replayed, err)
	}
	parent, err := repository.Get(ctx, parentID)
	if err != nil || parent.Status != "failed" || len(parent.RetryChildren) != 1 || parent.RetryChildren[0] != childID {
		t.Fatalf("parent was changed or lineage missing: %+v, %v", parent, err)
	}

	workerID := "integration-worker-" + ulid.Make().String()
	claimed, err := repository.MarkProcessing(ctx, childID, workerID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("MarkProcessing() = %v, %v", claimed, err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `UPDATE report_jobs SET processing_lease_until=$2 WHERE id=$1`, childID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Heartbeat(ctx, workerID, now.Add(-time.Minute), 1, "test", 1); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecoverStale(ctx, childID, "admin@example.test", now); !errors.Is(err, jobs.ErrNotRecoverable) {
		t.Fatalf("fresh worker recovery error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_heartbeats SET last_seen_at=$2 WHERE worker_id=$1`, workerID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecoverStale(ctx, childID, "admin@example.test", now); err != nil {
		t.Fatalf("stale recovery failed: %v", err)
	}
	detail, err := repository.Detail(ctx, childID)
	if err != nil || detail.Status != "queued" || len(detail.AttemptsTimeline) != 2 || detail.AttemptsTimeline[1].Type != "recovered" {
		t.Fatalf("recovered detail = %+v, %v", detail, err)
	}
}
