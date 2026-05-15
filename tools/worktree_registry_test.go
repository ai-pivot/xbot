package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestGitRepo creates a temporary git repo and returns its path.
func newTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
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
	return dir
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

func TestRegisterPeer_PersistenceAndReload(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	// Verify persistence file was created
	persistPath := registryPath(repoPath)
	data, err := os.ReadFile(persistPath)
	require.NoError(t, err, "registry should be persisted to disk")
	t.Logf("persisted: %s", data)

	// Load into a fresh registry
	reg2 := newTestRegistry()
	reg2.RegisterPeer("cli:repo:session-3", repoPath) // triggers loadRepoLocked

	// All 3 sessions should be visible
	e1 := reg2.GetBySession("cli:repo:session-1")
	e2 := reg2.GetBySession("cli:repo:session-2")
	e3 := reg2.GetBySession("cli:repo:session-3")
	require.NotNil(t, e1)
	require.NotNil(t, e2)
	require.NotNil(t, e3)
	assert.Equal(t, "primary", e1.Role)
	assert.Equal(t, "peer", e2.Role)
	assert.Equal(t, "peer", e3.Role, "third session after reload should also be peer")
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
