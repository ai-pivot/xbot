package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestGitRepo creates a temporary git repo and returns its path.
// The returned path is normalized via git rev-parse to ensure consistent
// formatting across platforms (e.g. Windows 8.3 short paths vs full paths).
func newTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		// Use cleanGitEnv to strip GIT_DIR etc. leaked from the parent
		// repo's pre-commit hook. Without this, the test's git commands
		// would operate on the *host* repo instead of the temp dir.
		cmd.Env = append(cleanGitEnv(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	run("git", "init")
	// Remove hooks so pre-commit doesn't run in temp repos.
	_ = os.RemoveAll(filepath.Join(dir, ".git", "hooks"))
	run("git", "commit", "--allow-empty", "-m", "init")

	// Normalize via git rev-parse so the returned path matches what
	// GitRepoRoot() returns inside RegisterPeer (avoids Windows 8.3 vs
	// full path mismatch).
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// newTestRegistry creates a fresh WorktreeRegistry for testing.
func newTestRegistry() *WorktreeRegistry {
	return &WorktreeRegistry{
		bySess: make(map[string]*WorktreeEntry),
		byRepo: make(map[string][]*WorktreeEntry),
	}
}

func TestRegisterPeer_FirstSessionBecomesPrimary(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)

	entry := reg.GetBySession("cli:repo:session-1")
	require.NotNil(t, entry)
	assert.Equal(t, "primary", entry.Role, "first session in repo should be primary")
	assert.Equal(t, repoPath, entry.RepoPath)
	assert.Equal(t, "", entry.WorktreeDir, "no worktree dir for RegisterPeer mode")
	assert.Equal(t, "", entry.Branch, "no branch for RegisterPeer mode")
}

func TestRegisterPeer_SecondSessionBecomesPeer(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	e1 := reg.GetBySession("cli:repo:session-1")
	e2 := reg.GetBySession("cli:repo:session-2")
	require.NotNil(t, e1)
	require.NotNil(t, e2)
	assert.Equal(t, "primary", e1.Role, "first session should be primary")
	assert.Equal(t, "peer", e2.Role, "second session should be peer")
}

func TestRegisterPeer_ManySessions(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	for i := 0; i < 5; i++ {
		key := "cli:repo:session-" + string(rune('0'+i))
		reg.RegisterPeer(key, repoPath)
	}

	entries := reg.ListRepo(repoPath)
	require.Len(t, entries, 5)
	assert.Equal(t, "primary", entries[0].Role)
	for _, e := range entries[1:] {
		assert.Equal(t, "peer", e.Role, "all sessions after first should be peer")
	}
}

func TestRegisterPeer_Idempotent(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-1", repoPath) // duplicate

	entries := reg.ListRepo(repoPath)
	assert.Len(t, entries, 1, "duplicate RegisterPeer should be a no-op")
}

func TestRegisterPeer_NotGitRepo(t *testing.T) {
	reg := newTestRegistry()
	dir := t.TempDir() // not a git repo

	reg.RegisterPeer("cli:repo:session-1", dir)

	entry := reg.GetBySession("cli:repo:session-1")
	assert.Nil(t, entry, "non-git dir should not register")
}

func TestRegisterPeer_DifferentRepos(t *testing.T) {
	repo1 := newTestGitRepo(t)
	repo2 := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo1:session-1", repo1)
	reg.RegisterPeer("cli:repo2:session-1", repo2)

	e1 := reg.GetBySession("cli:repo1:session-1")
	e2 := reg.GetBySession("cli:repo2:session-1")
	require.NotNil(t, e1)
	require.NotNil(t, e2)
	assert.Equal(t, "primary", e1.Role, "first session in repo1 should be primary")
	assert.Equal(t, "primary", e2.Role, "first session in repo2 should be primary")
}

func TestRegisterPeer_NotPersistedToDisk(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	// RegisterPeer entries are runtime-only — no persistence file should be created.
	persistPath := registryPath(repoPath)
	_, err := os.ReadFile(persistPath)
	assert.True(t, os.IsNotExist(err), "RegisterPeer entries should NOT be persisted to disk")

	// A fresh registry should NOT see the old entries via loadRepoLocked.
	reg2 := newTestRegistry()
	reg2.RegisterPeer("cli:repo:session-3", repoPath) // triggers loadRepoLocked internally

	// Old entries from reg1 are NOT visible — they were runtime-only.
	assert.Nil(t, reg2.GetBySession("cli:repo:session-1"), "old runtime entries should not survive reload")
	assert.Nil(t, reg2.GetBySession("cli:repo:session-2"), "old runtime entries should not survive reload")

	// New entry IS visible (in memory).
	e3 := reg2.GetBySession("cli:repo:session-3")
	require.NotNil(t, e3)
	assert.Equal(t, "primary", e3.Role, "fresh registry should assign primary to first session")
}

func TestRegisterPeer_GetPrimary(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// No primary yet
	assert.Nil(t, reg.GetPrimary(repoPath))

	// Register first → primary
	reg.RegisterPeer("cli:repo:session-1", repoPath)
	primary := reg.GetPrimary(repoPath)
	require.NotNil(t, primary)
	assert.Equal(t, "primary", primary.Role)
	assert.Equal(t, "cli:repo:session-1", primary.SessionKey)

	// Register more → primary unchanged
	reg.RegisterPeer("cli:repo:session-2", repoPath)
	primary = reg.GetPrimary(repoPath)
	require.NotNil(t, primary)
	assert.Equal(t, "cli:repo:session-1", primary.SessionKey, "primary should remain the first session")
}
