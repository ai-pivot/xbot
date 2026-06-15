package cron

import (
	"testing"
	"time"

	"xbot/storage/sqlite"
)

// helperOpenDB creates a fresh SQLite DB for testing.
func helperOpenDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test_cron.db"
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// --- Fix 1: OneShot delay_seconds job preserved after restart (not deleted) ---

func TestCleanupExpiredJobs_OneShotDelayPreservedAfterRestart(t *testing.T) {
	db := helperOpenDB(t)
	cronSvc := sqlite.NewCronService(db)
	s := NewScheduler(cronSvc)

	// Simulate a delay_seconds one-shot job created 60s ago,
	// with next_run = created + 30s (already expired by 30s).
	created := time.Now().Add(-60 * time.Second)
	nextRun := created.Add(30 * time.Second) // 30s ago → expired

	job := &sqlite.CronJob{
		ID:           "job_delay_test",
		Message:      "test delay",
		DelaySeconds: 30,
		CreatedAt:    created,
		NextRun:      nextRun,
		OneShot:      true,
	}
	if err := cronSvc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// cleanupExpiredJobs simulates what happens on restart
	s.cleanupExpiredJobs()

	// FIXED: the job should be preserved so checkAndFire triggers it immediately
	got, err := cronSvc.GetJob("job_delay_test")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got == nil {
		t.Fatal("BUG: delay_seconds one-shot job was deleted by cleanupExpiredJobs — should be preserved for immediate fire")
	}
	t.Logf("FIXED: delay_seconds one-shot job preserved, next_run=%v (will fire on first tick)", got.NextRun)
}

// --- Fix 1b: At-based one-shot job still removed after restart (correct behavior) ---

func TestCleanupExpiredJobs_AtOneShotRemovedAfterRestart(t *testing.T) {
	db := helperOpenDB(t)
	cronSvc := sqlite.NewCronService(db)
	s := NewScheduler(cronSvc)

	// An At-based one-shot job scheduled for the past
	job := &sqlite.CronJob{
		ID:        "job_at_test",
		Message:   "test at",
		At:        "2026-06-15T08:00:00",
		CreatedAt: time.Now().Add(-4 * time.Hour),
		NextRun:   time.Now().Add(-2 * time.Hour), // 2h ago → expired
		OneShot:   true,
	}
	if err := cronSvc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	s.cleanupExpiredJobs()

	got, err := cronSvc.GetJob("job_at_test")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got != nil {
		t.Errorf("At-based one-shot job should be removed after expiry, but still exists")
	}
	t.Logf("CORRECT: At-based one-shot job removed (scheduled moment passed)")
}

// --- Fix 2: every_seconds eliminates accumulated drift ---

func TestEverySecondsNoDrift(t *testing.T) {
	db := helperOpenDB(t)
	cronSvc := sqlite.NewCronService(db)

	var firedCount int
	s := NewScheduler(cronSvc)
	s.SetInjectFunc(func(channel, chatID, senderID, content string) {
		firedCount++
	})

	// Create an every-10-second job starting at 10:00:10
	baseTime := time.Date(2026, 6, 15, 10, 0, 0, 0, time.Local)
	job := &sqlite.CronJob{
		ID:           "job_drift_test",
		Message:      "tick",
		EverySeconds: 10,
		CreatedAt:    baseTime,
		NextRun:      baseTime.Add(10 * time.Second),
		OneShot:      false,
	}
	if err := cronSvc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Simulate ticks where tick4 is 2s late (system busy).
	// With the fix, next_run should be based on job.NextRun, not fire time.
	ticks := []time.Time{
		baseTime.Add(5 * time.Second),  // 10:00:05 → not yet
		baseTime.Add(10 * time.Second), // 10:00:10 → fire! next should be 10:00:20
		baseTime.Add(15 * time.Second), // 10:00:15 → not yet
		baseTime.Add(22 * time.Second), // 10:00:22 → fire (2s late), next should still be 10:00:30 (no drift!)
		baseTime.Add(27 * time.Second), // 10:00:27 → not yet
		baseTime.Add(30 * time.Second), // 10:00:30 → fire! on schedule
	}

	for _, tick := range ticks {
		s.checkAndFire(tick)
	}

	if firedCount != 3 {
		t.Errorf("expected 3 fires, got %d", firedCount)
	}

	got, _ := cronSvc.GetJob("job_drift_test")
	// With the fix: next_run = 10:00:30 + 10 = 10:00:40 (NO drift)
	// Without the fix: next_run would be 10:00:30 + 10 = 10:00:40 only if tick6 was on time
	// But tick4=10:00:22 → old code: next=10:00:32, then tick at 10:00:32 would fire → next=10:00:42
	expectedNext := baseTime.Add(40 * time.Second)
	if !got.NextRun.Equal(expectedNext) {
		t.Errorf("next_run should be %v (no drift), got %v", expectedNext, got.NextRun)
	}
	t.Logf("FIXED: next_run=%v — no accumulated drift", got.NextRun)
}

// --- Recurring job skips missed executions after restart (expected behavior) ---

func TestCleanupExpiredJobs_RecurringSkipsMissedRuns(t *testing.T) {
	db := helperOpenDB(t)
	cronSvc := sqlite.NewCronService(db)
	s := NewScheduler(cronSvc)

	// Simulate an every-5-min job that was supposed to run 12 min ago.
	pastRun := time.Now().Add(-12 * time.Minute)

	job := &sqlite.CronJob{
		ID:           "job_recurring_test",
		Message:      "test recurring",
		EverySeconds: 300, // 5 minutes
		CreatedAt:    time.Now().Add(-20 * time.Minute),
		NextRun:      pastRun,
		OneShot:      false,
	}
	if err := cronSvc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	s.cleanupExpiredJobs()

	got, err := cronSvc.GetJob("job_recurring_test")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got == nil {
		t.Fatal("job should not be deleted, but it's gone")
	}

	// next_run should be ~5 min from now (missed executions skipped —
	// this is expected behavior matching traditional cron semantics)
	if got.NextRun.Before(time.Now().Add(4 * time.Minute)) {
		t.Errorf("expected next_run ~5min in future, got %v", got.NextRun)
	}
	t.Logf("CORRECT: recurring next_run=%v (missed executions skipped, matches cron semantics)",
		got.NextRun)
}

// --- Integration: delay_seconds job fires immediately after restart ---

func TestDelaySecondsFiresOnFirstTickAfterRestart(t *testing.T) {
	db := helperOpenDB(t)
	cronSvc := sqlite.NewCronService(db)

	var firedMessages []string
	s := NewScheduler(cronSvc)
	s.SetInjectFunc(func(channel, chatID, senderID, content string) {
		firedMessages = append(firedMessages, content)
	})

	// Create a delay_seconds job that's already expired
	job := &sqlite.CronJob{
		ID:           "job_fire_test",
		Message:      "fire me!",
		DelaySeconds: 30,
		CreatedAt:    time.Now().Add(-60 * time.Second),
		NextRun:      time.Now().Add(-30 * time.Second), // 30s ago
		OneShot:      true,
	}
	if err := cronSvc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Step 1: cleanupExpiredJobs (should preserve, not delete)
	s.cleanupExpiredJobs()

	got, _ := cronSvc.GetJob("job_fire_test")
	if got == nil {
		t.Fatal("job should be preserved by cleanupExpiredJobs")
	}

	// Step 2: first checkAndFire tick (simulates first runLoop iteration)
	s.checkAndFire(time.Now())

	if len(firedMessages) != 1 {
		t.Errorf("expected 1 fire on first tick, got %d", len(firedMessages))
	}

	// Job should now be removed (one-shot after firing)
	got, _ = cronSvc.GetJob("job_fire_test")
	if got != nil {
		t.Errorf("one-shot job should be removed after firing")
	}
	t.Logf("FIXED: delay_seconds job fired immediately on first tick after restart")
}
