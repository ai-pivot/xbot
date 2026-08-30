package sqlite

import (
	"testing"
	"time"

	"xbot/event"
)

// TestAddJobUserID_ListJobsByUserID verifies that AddJob persists the
// canonical user_id (M1): new rows written by the Cron tool carry the
// caller's user_id so ListJobsByUserID (the web cron panel query) finds them.
// Previously the INSERT omitted the user_id column → every new row kept the
// DEFAULT 0 → the web cron panel (ListJobsByUserID) was permanently empty.
func TestAddJobUserID_ListJobsByUserID(t *testing.T) {
	db := openTestDB(t)
	svc := NewCronService(db)

	now := time.Now()
	job := &CronJob{
		ID:        "job_uid_test",
		Message:   "uid test",
		Channel:   "web",
		ChatID:    "chat-1",
		SenderID:  "web-1",
		CronExpr:  "0 9 * * *",
		CreatedAt: now,
		NextRun:   now.Add(time.Hour),
		UserID:    42,
	}
	if err := svc.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Column-level check: the row must carry user_id=42 (not the DEFAULT 0).
	var uid int64
	if err := db.Conn().QueryRow("SELECT user_id FROM cron_jobs WHERE id = ?", job.ID).Scan(&uid); err != nil {
		t.Fatalf("read user_id: %v", err)
	}
	if uid != 42 {
		t.Errorf("cron_jobs.user_id = %d, want 42", uid)
	}

	// Read-path check: ListJobsByUserID must return the job.
	jobs, err := svc.ListJobsByUserID(42)
	if err != nil {
		t.Fatalf("ListJobsByUserID: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job_uid_test" {
		t.Fatalf("ListJobsByUserID(42) = %d jobs, want 1 (job_uid_test)", len(jobs))
	}

	// Other users must not see it.
	other, err := svc.ListJobsByUserID(7)
	if err != nil {
		t.Fatalf("ListJobsByUserID(7): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("ListJobsByUserID(7) = %d jobs, want 0", len(other))
	}
}

// TestAddTriggerUserID verifies that AddTrigger resolves the canonical user_id
// from the trigger's (channel, sender_id) via user_identities (M1, same root
// cause as cron_jobs): new rows must carry the owner's user_id so per-user
// trigger queries (e.g. user merge preview counting event_triggers by user_id)
// see them.
func TestAddTriggerUserID(t *testing.T) {
	db := openTestDB(t)
	svc := NewTriggerService(db)

	// Seed the identity so canonicalUserID('web', 'web-1') resolves to 42.
	if _, err := db.Conn().Exec(
		"INSERT INTO users (id, display_name, role) VALUES (42, 'U42', 'user')",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Conn().Exec(
		"INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (42, 'web', 'web-1')",
	); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	tr := &event.Trigger{
		ID:         "trg_uid_test",
		Name:       "uid test",
		EventType:  "webhook",
		Channel:    "web",
		ChatID:     "chat-1",
		SenderID:   "web-1",
		MessageTpl: "hello",
		Enabled:    true,
		CreatedAt:  time.Now(),
	}
	if err := svc.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	var uid int64
	if err := db.Conn().QueryRow("SELECT user_id FROM event_triggers WHERE id = ?", tr.ID).Scan(&uid); err != nil {
		t.Fatalf("read user_id: %v", err)
	}
	if uid != 42 {
		t.Errorf("event_triggers.user_id = %d, want 42", uid)
	}
}
