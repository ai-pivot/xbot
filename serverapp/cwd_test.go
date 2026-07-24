package serverapp

import (
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/config"
)

func newTestAgentForCWD(t *testing.T) *agent.Agent {
	t.Helper()
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
	ag.SetIdentityResolver(agent.NewIdentityResolver(ag.MultiSession().DB().Conn()))
	return ag
}

// TestWebSessionCWD_EmptyForNewWebSession reproduces the bug: web sessions
// always show pwd = ~ because webSessionCWD returns "" when no CWD is
// persisted. The agent then falls back to cfg.WorkingDir ("." → home dir),
// but the frontend never sees this — CwdProvider gets "" → shows ~.
func TestWebSessionCWD_EmptyForNewWebSession(t *testing.T) {
	ag := newTestAgentForCWD(t)
	chatID := "web-test-chat"

	// A brand-new web session: no persisted CWD, no in-memory CWD.
	cwd := webSessionCWD(ag, "web", chatID)
	if cwd == "" {
		t.Errorf("BUG: webSessionCWD returned empty for new web session — " +
			"frontend shows ~, agent uses home dir, AGENTS.md not found. " +
			"Expected fallback to server workDir")
	}
}

// TestWebSessionCWD_PersistsAfterCd verifies that after the agent's Cd tool
// updates the session CWD, webSessionCWD returns the new value.
func TestWebSessionCWD_PersistsAfterCd(t *testing.T) {
	ag := newTestAgentForCWD(t)
	chatID := "web-test-chat-2"

	// Simulate agent Cd: SetCurrentDir persists to disk + in-memory
	sess, err := ag.MultiSession().GetOrCreateSession("web", chatID)
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	targetDir := t.TempDir()
	sess.SetCurrentDir(targetDir)

	cwd := webSessionCWD(ag, "web", chatID)
	if cwd != targetDir {
		t.Errorf("after Cd, expected %q, got %q", targetDir, cwd)
	}
}

// TestWebSessionCWD_OnlyReturnsAbsolute verifies the CWD is always absolute.
func TestWebSessionCWD_OnlyReturnsAbsolute(t *testing.T) {
	ag := newTestAgentForCWD(t)
	chatID := "web-test-chat-3"

	cwd := webSessionCWD(ag, "web", chatID)
	if cwd != "" && !filepath.IsAbs(cwd) {
		t.Errorf("expected absolute path, got %q", cwd)
	}
}

// Ensure config import is used (BuildRPCTable reference)
var _ = config.Config{}
