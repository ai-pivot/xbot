package serverapp

import (
	"path/filepath"
	"testing"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
	"xbot/storage/sqlite"
)

// TestCronTasksCallbackSessionScoped reproduces the bug: the web task panel's
// cron list (/api/cron/list → callbacks.CronTasks) returned ALL cron jobs
// (ListJobs) instead of the requested session's jobs — the bg-task panel got
// session isolation (ListAllForSession, user request 2026-08-30) but cron did
// not, so every session's task panel showed every other session's cron jobs.
func TestCronTasksCallbackSessionScoped(t *testing.T) {
	dir := t.TempDir()
	ag, err := agent.New(agent.Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	db := ag.MultiSession().DB()
	svc := sqlite.NewCronService(db)
	now := time.Now()
	// One job for session A (web:chat-a), one for session B (web:chat-b).
	if err := svc.AddJob(&sqlite.CronJob{
		ID: "job-a", Message: "session A job", Channel: "web", ChatID: "chat-a",
		CronExpr: "0 9 * * *", CreatedAt: now, NextRun: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("AddJob A: %v", err)
	}
	if err := svc.AddJob(&sqlite.CronJob{
		ID: "job-b", Message: "session B job", Channel: "web", ChatID: "chat-b",
		CronExpr: "0 9 * * *", CreatedAt: now, NextRun: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("AddJob B: %v", err)
	}

	callbacks := buildWebCallbacks(&config.Config{}, ag, db)

	// Request session A's panel — must see ONLY job-a.
	got, err := callbacks.CronTasks("cli_user", web.SessionSelector{Channel: "web", ChatID: "chat-a"})
	if err != nil {
		t.Fatalf("CronTasks: %v", err)
	}
	jobs, ok := got.([]*sqlite.CronJob)
	if !ok {
		t.Fatalf("expected []*sqlite.CronJob, got %T", got)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-a" {
		t.Fatalf("session-scoped cron list must return exactly job-a (chat-a only), got %d job(s): %+v", len(jobs), jobs)
	}

	// Request session B's panel — must see ONLY job-b.
	got, err = callbacks.CronTasks("cli_user", web.SessionSelector{Channel: "web", ChatID: "chat-b"})
	if err != nil {
		t.Fatalf("CronTasks (chat-b): %v", err)
	}
	jobs, ok = got.([]*sqlite.CronJob)
	if !ok {
		t.Fatalf("expected []*sqlite.CronJob, got %T", got)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-b" {
		t.Fatalf("session-scoped cron list must return exactly job-b (chat-b only), got %d job(s): %+v", len(jobs), jobs)
	}
}
