package jobs_test

import (
	"testing"
	"time"

	"reporting-server/internal/jobs"
)

func TestHeartbeatOnline(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	if !jobs.HeartbeatOnline(now.Add(-30*time.Second), now) {
		t.Fatal("heartbeat at the threshold should be online")
	}
	if jobs.HeartbeatOnline(now.Add(-30*time.Second-time.Nanosecond), now) {
		t.Fatal("heartbeat older than the threshold should be offline")
	}
}
