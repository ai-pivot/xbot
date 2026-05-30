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
		loaded: make(map[string]bool),
	}
}

func TestRegisterPeer_AllSessionsArePeer(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)

	entry := reg.GetBySession("cli:repo:session-1")
	require.NotNil(t, entry)
	assert.Equal(t, "peer", entry.Role, "all sessions should be peer (no primary concept)")
	assert.Equal(t, repoPath, entry.RepoPath)
	assert.Equal(t, "", entry.WorktreeDir, "no worktree dir for RegisterPeer mode")
	assert.Equal(t, "", entry.Branch, "no branch for RegisterPeer mode")
}

func TestRegisterPeer_SecondSessionAlsoPeer(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	e1 := reg.GetBySession("cli:repo:session-1")
	e2 := reg.GetBySession("cli:repo:session-2")
	require.NotNil(t, e1)
	require.NotNil(t, e2)
	assert.Equal(t, "peer", e1.Role, "all sessions should be peer")
	assert.Equal(t, "peer", e2.Role, "all sessions should be peer")
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
	assert.Equal(t, "peer", entries[0].Role)
	for _, e := range entries[1:] {
		assert.Equal(t, "peer", e.Role, "all sessions should be peer")
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
	assert.Equal(t, "peer", e1.Role, "first session in repo1 should be peer")
	assert.Equal(t, "peer", e2.Role, "first session in repo2 should be peer")
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
	assert.Equal(t, "peer", e3.Role, "fresh registry should assign peer to first session")
}

func TestCleanupSession_PeerOnly(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// Register two peer-awareness sessions (no physical worktrees)
	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	require.Len(t, reg.ListRepo(repoPath), 2)

	// Cleanup session-2 (peer, no worktree)
	reg.CleanupSession("cli:repo:session-2")

	assert.Nil(t, reg.GetBySession("cli:repo:session-2"), "cleaned session should be gone")
	assert.NotNil(t, reg.GetBySession("cli:repo:session-1"), "other session should remain")
	assert.Len(t, reg.ListRepo(repoPath), 1, "only one session should remain")
}

func TestCleanupSession_PeerOnly_NotRegistered(t *testing.T) {
	reg := newTestRegistry()
	// Should not panic on nonexistent session
	reg.CleanupSession("cli:repo:nonexistent")
}

func TestCleanupSession_WithWorktree(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// Use AutoDetectAndInit to create a real worktree for session-1
	entry, created := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg)
	require.NotNil(t, entry, "AutoDetectAndInit should succeed")
	require.True(t, created, "first init should create a new worktree")
	require.NotEmpty(t, entry.WorktreeDir, "should have a worktree dir")

	// Register a second peer-awareness session
	reg.RegisterPeer("cli:repo:session-2", repoPath)

	// Verify both are registered
	require.Len(t, reg.ListRepo(repoPath), 2)

	// Verify worktree dir exists on disk
	_, err := os.Stat(entry.WorktreeDir)
	require.NoError(t, err, "worktree dir should exist before cleanup")

	// Cleanup session-1 (has physical worktree)
	reg.CleanupSession("cli:repo:session-1")

	// Registry entry should be gone
	assert.Nil(t, reg.GetBySession("cli:repo:session-1"), "cleaned session should be gone from registry")

	// session-2 should remain
	assert.NotNil(t, reg.GetBySession("cli:repo:session-2"), "other session should remain")

	// Worktree dir should be removed from disk
	_, err = os.Stat(entry.WorktreeDir)
	assert.True(t, os.IsNotExist(err), "worktree dir should be removed after CleanupSession")

	// Git worktree should be gone from `git worktree list`
	out, _ := exec.Command("git", "-C", repoPath, "worktree", "list").Output()
	assert.NotContains(t, string(out), entry.WorktreeDir, "worktree should not appear in git worktree list")
}

func TestCleanupSession_AllSessions(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	reg.RegisterPeer("cli:repo:session-1", repoPath)
	reg.RegisterPeer("cli:repo:session-2", repoPath)
	reg.RegisterPeer("cli:repo:session-3", repoPath)

	require.Len(t, reg.ListRepo(repoPath), 3)

	// Cleanup all
	reg.CleanupSession("cli:repo:session-1")
	reg.CleanupSession("cli:repo:session-2")
	reg.CleanupSession("cli:repo:session-3")

	assert.Empty(t, reg.ListRepo(repoPath), "repo should have no sessions after all cleaned up")
}

func TestAutoDetectAndInit_SetsWorktreeDir(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	entry, _ := autoDetectAndInitInto(repoPath, "cli:repo:main-session", reg)
	require.NotNil(t, entry)

	// Worktree dir should be under the base dir
	baseDir := filepath.Join(filepath.Dir(repoPath), ".xbot-worktrees")
	assert.Contains(t, entry.WorktreeDir, baseDir, "worktree should be under base dir")

	// Worktree dir should exist on disk
	info, err := os.Stat(entry.WorktreeDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Worktree should be a valid git worktree
	gitFile := filepath.Join(entry.WorktreeDir, ".git")
	data, err := os.ReadFile(gitFile)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "gitdir:"), ".git file should point to main repo")
}

func TestAutoDetectAndInit_CleanupThenRecreate(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// Create worktree for session-1
	entry1, _ := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg)
	require.NotNil(t, entry1)

	// Cleanup it
	reg.CleanupSession("cli:repo:session-1")
	assert.Nil(t, reg.GetBySession("cli:repo:session-1"))

	// Create a new worktree for session-2 — should succeed without conflict
	entry2, _ := autoDetectAndInitInto(repoPath, "cli:repo:session-2", reg)
	require.NotNil(t, entry2)
	assert.NotEqual(t, entry1.WorktreeDir, entry2.WorktreeDir, "new worktree should be a different dir")

	// Old worktree dir should be gone
	_, err := os.Stat(entry1.WorktreeDir)
	assert.True(t, os.IsNotExist(err), "old worktree dir should be cleaned up")
}

// TestAutoDetectAndInit_IdempotentAcrossRestart simulates a process restart
// by creating a new registry and verifying that the same session returns
// its existing worktree entry without creating a new one.
func TestAutoDetectAndInit_IdempotentAcrossRestart(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg1 := newTestRegistry()

	// Create a worktree with registry 1 (simulates first process)
	entry1, created1 := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg1)
	require.NotNil(t, entry1, "AutoDetectAndInit should succeed on first call")
	require.True(t, created1, "first init should create a new worktree")
	require.NotEmpty(t, entry1.WorktreeDir, "should have a worktree dir")

	// Verify worktree dir exists
	_, err := os.Stat(entry1.WorktreeDir)
	require.NoError(t, err, "worktree dir should exist")

	// Simulate restart: create a fresh registry (process memory is gone,
	// but persisted registry.json is still on disk).
	reg2 := newTestRegistry()

	// The new registry should find the existing entry from disk
	entry2, created2 := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg2)
	require.NotNil(t, entry2, "AutoDetectAndInit should return existing entry after restart")
	assert.False(t, created2, "restart should return existing entry, not create new one")

	// Must return the SAME worktree (not a new one)
	assert.Equal(t, entry1.WorktreeDir, entry2.WorktreeDir,
		"idempotent init should return the existing worktree dir")
	assert.Equal(t, entry1.Branch, entry2.Branch,
		"idempotent init should return the existing branch")

	// Verify no second worktree was created
	baseDir := filepath.Join(filepath.Dir(repoPath), ".xbot-worktrees")
	entries, _ := os.ReadDir(baseDir)
	worktreeCount := 0
	for _, e := range entries {
		if e.IsDir() && strings.Contains(e.Name(), "session-1") {
			worktreeCount++
		}
	}
	assert.Equal(t, 1, worktreeCount,
		"should not create a second worktree for the same session")
}

// TestAutoDetectAndInit_IdempotentInMemory verifies that calling
// autoDetectAndInitInto twice on the same registry returns the same entry.
func TestAutoDetectAndInit_IdempotentInMemory(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	entry1, created1 := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg)
	require.NotNil(t, entry1)
	assert.True(t, created1, "first call should create worktree")

	entry2, created2 := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg)
	require.NotNil(t, entry2)
	assert.False(t, created2, "second call should return existing entry")

	assert.Equal(t, entry1.WorktreeDir, entry2.WorktreeDir,
		"second call should return same worktree")
	assert.Equal(t, entry1.Branch, entry2.Branch,
		"second call should return same branch")
}

// newTestGitRepoWithRemote creates a local git repo ("origin") and a clone
// of it. Returns (originPath, clonePath). The clone has a remote "origin"
// pointing to the local origin repo, and the symbolic-ref HEAD is set up.
func newTestGitRepoWithRemote(t *testing.T) (originPath, clonePath string) {
	t.Helper()
	run := func(dir, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(cleanGitEnv(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}

	// Create the "origin" repo
	originDir := t.TempDir()
	run(originDir, "git", "init", "-b", "master")
	_ = os.RemoveAll(filepath.Join(originDir, ".git", "hooks"))
	run(originDir, "git", "commit", "--allow-empty", "-m", "initial commit on master")

	// Normalize origin path
	out, err := exec.Command("git", "-C", originDir, "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	originPath = strings.TrimSpace(string(out))

	// Clone it — this sets up the remote HEAD symref properly
	cloneDir := t.TempDir()
	run("", "git", "clone", originPath, cloneDir)
	_ = os.RemoveAll(filepath.Join(cloneDir, ".git", "hooks"))

	// Normalize clone path
	out, err = exec.Command("git", "-C", cloneDir, "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	clonePath = strings.TrimSpace(string(out))

	return originPath, clonePath
}

func TestResolveRemoteMainBranch_WithRemote(t *testing.T) {
	_, clonePath := newTestGitRepoWithRemote(t)

	// The clone should detect "origin/master"
	ref := resolveRemoteMainBranch(clonePath)
	assert.Equal(t, "origin/master", ref, "should detect remote main branch")
}

func TestResolveRemoteMainBranch_NoRemote(t *testing.T) {
	repoPath := newTestGitRepo(t)

	// No remote configured — should return ""
	ref := resolveRemoteMainBranch(repoPath)
	assert.Equal(t, "", ref, "repo without remote should return empty")
}

func TestCreateWorktree_BasedOnRemoteMain(t *testing.T) {
	originPath, clonePath := newTestGitRepoWithRemote(t)

	// Add a commit on master in origin AFTER clone
	run := func(dir, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(cleanGitEnv(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}

	// Create a file in origin and commit
	testFile := filepath.Join(originPath, "new-file.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("from remote"), 0644))
	run(originPath, "git", "add", "new-file.txt")
	run(originPath, "git", "commit", "-m", "add new-file on remote master")

	// Now create a worktree from the clone — should fetch and get the new commit
	reg := newTestRegistry()
	entry, created := autoDetectAndInitInto(clonePath, "cli:repo:session-test", reg)
	require.NotNil(t, entry, "AutoDetectAndInit should succeed")
	require.True(t, created, "should create a new worktree")

	// The worktree should contain new-file.txt (fetched from remote)
	data, err := os.ReadFile(filepath.Join(entry.WorktreeDir, "new-file.txt"))
	require.NoError(t, err, "worktree should contain file from remote main branch")
	assert.Equal(t, "from remote", string(data))

	// Clean up
	reg.CleanupSession("cli:repo:session-test")
}

// TestResolveRemoteMainBranch_PicksNewestMultiRemote verifies that when a repo
// has multiple remotes (e.g. "gl" and "origin"), resolveRemoteMainBranch picks
// the remote whose default branch has the newest commit — not just the first
// one alphabetically.
func TestResolveRemoteMainBranch_PicksNewestMultiRemote(t *testing.T) {
	run := func(dir, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(cleanGitEnv(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}

	// Create two bare "origin" repos
	upstreamDir := t.TempDir()
	run("", "git", "init", "--bare", upstreamDir)
	// Create a working repo, push initial commit, then clone twice
	workDir := t.TempDir()
	run(workDir, "git", "clone", upstreamDir, ".")
	_ = os.RemoveAll(filepath.Join(workDir, ".git", "hooks"))
	run(workDir, "git", "commit", "--allow-empty", "-m", "init")
	run(workDir, "git", "push", "origin", "master")
	// Set HEAD so symbolic-ref works
	run(upstreamDir, "git", "symbolic-ref", "HEAD", "refs/heads/master")

	// Now create the test clone
	cloneDir := t.TempDir()
	run("", "git", "clone", upstreamDir, cloneDir)
	_ = os.RemoveAll(filepath.Join(cloneDir, ".git", "hooks"))

	// Normalize
	out, err := exec.Command("git", "-C", cloneDir, "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	clonePath := strings.TrimSpace(string(out))

	// Add a second remote (simulating "gl") that points to a STALE bare repo
	staleDir := t.TempDir()
	run("", "git", "init", "--bare", staleDir)
	run(staleDir, "git", "symbolic-ref", "HEAD", "refs/heads/master")
	// Push the initial commit to the stale remote too
	run(clonePath, "git", "remote", "add", "gl", staleDir)
	run(clonePath, "git", "push", "gl", "master")

	// Now add a NEW commit on the origin remote (simulate upstream advancing)
	run(workDir, "git", "commit", "--allow-empty", "-m", "newer commit on origin")
	run(workDir, "git", "push", "origin", "master")

	// Fetch origin (not gl) so origin has the newer commit
	run(clonePath, "git", "fetch", "origin")

	// Verify: gl/master is older, origin/master is newer
	glDate, _ := exec.Command("git", "-C", clonePath, "log", "-1", "--format=%cI", "gl/master").Output()
	originDate, _ := exec.Command("git", "-C", clonePath, "log", "-1", "--format=%cI", "origin/master").Output()
	assert.True(t, strings.TrimSpace(string(originDate)) >= strings.TrimSpace(string(glDate)),
		"origin/master should be at least as new as gl/master")

	// resolveRemoteMainBranch should pick origin/master (newer), NOT gl/master
	ref := resolveRemoteMainBranch(clonePath)
	assert.Equal(t, "origin/master", ref,
		"should pick the remote with the newest default branch commit, not first alphabetically")
}

func TestAutoDetectAndInit_RejectsWorktreeWorkDir(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// Create a worktree for session-1 first
	entry1, created := autoDetectAndInitInto(repoPath, "cli:repo:session-1", reg)
	require.NotNil(t, entry1)
	require.True(t, created)

	// Now try to create a worktree for session-2 using the worktree dir as workDir.
	// This simulates a stale CWD leak where the agent's CWD is stuck in an old worktree.
	// Should be rejected — we must NOT nest worktrees.
	entry2, created2 := autoDetectAndInitInto(entry1.WorktreeDir, "cli:repo:session-2", reg)
	assert.Nil(t, entry2, "should reject worktree dir as workDir")
	assert.False(t, created2)
}

func TestAutoDetectAndInit_StaleEntryCleanup(t *testing.T) {
	repoPath := newTestGitRepo(t)
	reg := newTestRegistry()

	// Create a worktree for session-stale
	entry1, _ := autoDetectAndInitInto(repoPath, "cli:repo:session-stale", reg)
	require.NotNil(t, entry1)

	// Properly cleanup via registry (removes worktree + git metadata + deregisters)
	reg.CleanupSession("cli:repo:session-stale")
	assert.Nil(t, reg.GetBySession("cli:repo:session-stale"), "entry should be removed after cleanup")

	// Now re-register: should create a brand new worktree since the old one is gone.
	// Simulates a session that was deleted and then recreated with the same key.
	entry2, created := autoDetectAndInitInto(repoPath, "cli:repo:session-stale", reg)
	if entry2 == nil {
		t.Fatal("entry2 should not be nil")
	}
	assert.NotEmpty(t, entry2.WorktreeDir, "should have a valid worktree dir")
	_ = created // may be true or false depending on timing
}
