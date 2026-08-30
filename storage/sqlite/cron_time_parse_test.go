package sqlite

import (
	"testing"
	"time"
)

// TestListJobsByChannelChatIDTolerantTimeParse (i6) verifies
// ListJobsByChannelChatID uses parseSQLiteTime (like ListAllJobs) instead of
// bare time.Parse(time.RFC3339): legacy space-separated timestamps
// ("2026-05-08 20:40:28+08:00", written by older writes/drivers) and other
// driver formats must parse, and an unparseable last_trigger must silently
// leave LastTrigger nil instead of failing the whole listing.
func TestListJobsByChannelChatIDTolerantTimeParse(t *testing.T) {
	db := openTestDB(t)
	svc := NewCronService(db)

	now := time.Now()
	mk := func(id string) *CronJob {
		return &CronJob{
			ID: id, Message: "m", Channel: "web", ChatID: "chat-i6",
			SenderID: "web-1", CronExpr: "0 9 * * *",
			CreatedAt: now, NextRun: now.Add(time.Hour), UserID: 7,
		}
	}
	for _, id := range []string{"i6-legacy", "i6-bad"} {
		if err := svc.AddJob(mk(id)); err != nil {
			t.Fatalf("AddJob %s: %v", id, err)
		}
	}
	// Legacy space-separated format (written by flexTime-era writers/drivers).
	if _, err := db.Conn().Exec(
		`UPDATE cron_jobs SET last_trigger = ? WHERE id = ?`,
		"2026-05-08 20:40:28+08:00", "i6-legacy",
	); err != nil {
		t.Fatalf("seed legacy last_trigger: %v", err)
	}
	// Unparseable garbage.
	if _, err := db.Conn().Exec(
		`UPDATE cron_jobs SET last_trigger = ? WHERE id = ?`,
		"not-a-timestamp", "i6-bad",
	); err != nil {
		t.Fatalf("seed bad last_trigger: %v", err)
	}

	jobs, err := svc.ListJobsByChannelChatID("web", "chat-i6")
	if err != nil {
		t.Fatalf("ListJobsByChannelChatID with legacy/garbage timestamps: %v (must parse via parseSQLiteTime and tolerate garbage)", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	for _, job := range jobs {
		switch job.ID {
		case "i6-legacy":
			if job.LastTrigger == nil {
				t.Errorf("i6-legacy: LastTrigger must parse from the space-separated legacy format, got nil")
			}
		case "i6-bad":
			if job.LastTrigger != nil {
				t.Errorf("i6-bad: unparseable last_trigger must leave LastTrigger nil, got %v", job.LastTrigger)
			}
		}
		if job.CreatedAt.IsZero() || job.NextRun.IsZero() {
			t.Errorf("%s: CreatedAt/NextRun must be zero-value safe (never fail the listing)", job.ID)
		}
	}
}
