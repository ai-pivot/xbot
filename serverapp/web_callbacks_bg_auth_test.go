package serverapp

import (
	"path/filepath"
	"strings"
	"testing"

	"xbot/agent"
	"xbot/channel/web"
	"xbot/config"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// M4: the BackgroundTasks web callback must not trust the caller-supplied
// session selector. A non-admin user passing another user's session
// (channel+chatID) would receive that session's background task list —
// including full shell outputs — purely by naming the session.
// Defense in depth: the web REST layer gates access via canAccessSession,
// but the callback layer serves every entry path and must check ownership
// itself.
func TestBackgroundTasksCallbackDeniesForeignSession(t *testing.T) {
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
	// Close the agent's resources (DB handles) before t.TempDir's cleanup —
	// on Windows an open SQLite handle keeps xbot.db locked and RemoveAll
	// fails with "being used by another process".
	defer ag.Close()

	db := ag.MultiSession().DB()
	if db == nil {
		t.Fatal("agent multi-session DB is nil")
	}
	resolver := agent.NewIdentityResolver(db.Conn())
	ag.SetIdentityResolver(resolver)
	ag.SetBgTaskManager(tools.NewBackgroundTaskManager())

	// web-7 resolves to its canonical user; sessionA is owned by that user,
	// sessionB is owned by a different canonical user.
	uid, role, err := resolver.Resolve("web", "web-7")
	if err != nil {
		t.Fatalf("resolve identity: %v", err)
	}
	if uid <= 0 || role == "admin" {
		t.Fatalf("expected non-admin uid, got (%d, %q)", uid, role)
	}
	tenantSvc := sqlite.NewTenantService(db)
	if _, err := tenantSvc.ClaimOrVerifyTenantOwner("web", "sessionA", uid); err != nil {
		t.Fatalf("claim sessionA: %v", err)
	}
	if _, err := tenantSvc.ClaimOrVerifyTenantOwner("web", "sessionB", uid+100); err != nil {
		t.Fatalf("claim sessionB: %v", err)
	}

	cbs := buildWebCallbacks(&config.Config{}, ag, db)

	// Foreign session: must be denied, not return the other session's tasks.
	_, err = cbs.BackgroundTasks("web-7", web.SessionSelector{Channel: "web", ChatID: "sessionB"})
	if err == nil {
		t.Fatal("BackgroundTasks for a foreign session must be denied, got nil error (cross-session leak)")
	}
	if !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "not owned") {
		t.Errorf("denied error should mention ownership, got: %v", err)
	}

	// Own session: still allowed (regression guard — the fix must not break
	// legitimate access to the caller's own session).
	if _, err := cbs.BackgroundTasks("web-7", web.SessionSelector{Channel: "web", ChatID: "sessionA"}); err != nil {
		t.Errorf("BackgroundTasks for own session should succeed, got: %v", err)
	}

	// Unclaimed session (freshly created, no owner yet — e.g. a web chat
	// created before its first message): web-layer ownership (user_chats)
	// governs access; the callback must not deny an unclaimed tenant.
	if _, err := cbs.BackgroundTasks("web-7", web.SessionSelector{Channel: "web", ChatID: "unclaimed-chat"}); err != nil {
		t.Errorf("BackgroundTasks for an unclaimed session should not be denied by the ownership check, got: %v", err)
	}
}
