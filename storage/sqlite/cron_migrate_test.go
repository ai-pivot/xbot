package sqlite

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateFromJSON_DelaySecondsExpiredPreserved (m3) verifies that
// MigrateFromJSON keeps expired delay_seconds one-shot jobs — their
// "relative time" semantics mean "N seconds from creation": if the job expired
// while the server was down, it must fire on the first tick, not be dropped.
// Only at-based one-shot jobs (absolute time point) may be skipped when
// expired. Mirrors the cleanupExpiredJobs rule (AGENTS.md Cron Scheduler).
func TestMigrateFromJSON_DelaySecondsExpiredPreserved(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".xbot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now()
	json := `{
	"job-delay-expired": {"id": "job-delay-expired", "message": "remind me", "channel": "web", "chat_id": "c1",
     "delay_seconds": 30, "created_at": "` + now.Add(-10*time.Minute).UTC().Format(time.RFC3339) + `"},
	"job-at-expired": {"id": "job-at-expired", "message": "old meeting", "channel": "web", "chat_id": "c1",
     "at": "` + now.Add(-1*time.Hour).Format("2006-01-02T15:04:05") + `",
     "created_at": "` + now.Add(-2*time.Hour).UTC().Format(time.RFC3339) + `"},
	"job-recurring": {"id": "job-recurring", "message": "tick", "channel": "web", "chat_id": "c1",
     "every_seconds": 60, "created_at": "` + now.Add(-time.Hour).UTC().Format(time.RFC3339) + `"}
}`
	if err := os.WriteFile(filepath.Join(dir, ".xbot", "cron.json"), []byte(json), 0o644); err != nil {
		t.Fatalf("write cron.json: %v", err)
	}

	db := openTestDB(t)
	svc := NewCronService(db)
	if err := svc.MigrateFromJSON(dir); err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}

	jobs, err := svc.ListAllJobs()
	if err != nil {
		t.Fatalf("ListAllJobs: %v", err)
	}
	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ID] = true
	}
	if !ids["job-delay-expired"] {
		t.Errorf("expired delay_seconds job was skipped during migration — it must be preserved to fire on the first tick")
	}
	if ids["job-at-expired"] {
		t.Errorf("expired at-based job must be skipped during migration (absolute time point has passed)")
	}
	if !ids["job-recurring"] {
		t.Errorf("recurring job must be migrated")
	}
}
