package serverapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// TestBackgroundTasksFailsClosedOnIdentityResolveError verifies the
// BackgroundTasks callback fails CLOSED: when IdentityResolver.Resolve errors
// (DB failure, closed connection, ...), a non-admin caller must NOT fall
// through to ListAll() (the admin view — every session's background task).
// The old code only narrowed to ListAllForSession when
// `err == nil && uid > 0 && role != "admin"`, so any Resolve error granted
// the full task list.
func TestBackgroundTasksFailsClosedOnIdentityResolveError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(filepath.Join(dir, "xbot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Build the resolver while the DB is live, then close it — every
	// Resolve call now errors (auto-create INSERT hits the closed pool).
	resolver := agent.NewIdentityResolver(db.Conn())
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	manager := tools.NewBackgroundTaskManager()
	foreign := manager.Start("web:web-victim", "web-victim", "sleep", func(ctx context.Context, output func(string)) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	})
	t.Cleanup(func() { _ = manager.Kill(foreign.ID) })
	own := manager.Start("web:web-2", "web-2", "echo", func(ctx context.Context, output func(string)) (int, error) {
		time.Sleep(time.Minute)
		return 0, nil
	})
	t.Cleanup(func() { _ = manager.Kill(own.ID) })

	ag := &agent.Agent{}
	ag.SetBgTaskManager(manager)
	ag.SetIdentityResolver(resolver)

	callbacks := buildWebCallbacks(&config.Config{}, ag, nil)
	tasks, err := callbacks.BackgroundTasks("web-2", web.SessionSelector{Channel: "web", ChatID: "web-2"})
	if err == nil {
		t.Fatalf("BackgroundTasks must fail closed on identity resolve error; got tasks=%v (fail-open would leak %d session's tasks)", tasks, 2)
	}
}
