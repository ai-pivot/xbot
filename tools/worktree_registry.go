package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorktreeEntry describes a single active worktree managed by xbot.
type WorktreeEntry struct {
	SessionKey  string // "cli:/path/to/repo:debug" or "agent:role/instance"
	Role        string // "primary" | "peer" | "child"
	RepoPath    string // absolute path to the main git repo
	WorktreeDir string // absolute path to the worktree (empty for primary)
	Branch      string // branch name
	CreatedAt   time.Time
	Status      string // "working" | "merge-ready" | "done"
}

// WorktreeRegistry is a process-level registry of active worktrees.
// It is the single source of truth for peer discovery and is shared
// between WorktreeTool (writer) and DynamicContextInjector (reader).
type WorktreeRegistry struct {
	mu     sync.RWMutex
	byRepo map[string][]*WorktreeEntry // repoPath → entries
	bySess map[string]*WorktreeEntry   // sessionKey → entry
}

// GlobalWorktreeRegistry is the singleton registry used by all components.
var GlobalWorktreeRegistry = &WorktreeRegistry{
	byRepo: make(map[string][]*WorktreeEntry),
	bySess: make(map[string]*WorktreeEntry),
}

var (
	loadedMu sync.Mutex
	loaded   = make(map[string]bool)
)

// ensureLoaded lazily loads persisted registry data for a repo.
func (r *WorktreeRegistry) ensureLoaded(repoPath string) {
	loadedMu.Lock()
	if loaded[repoPath] {
		loadedMu.Unlock()
		return
	}
	loaded[repoPath] = true
	loadedMu.Unlock()

	r.mu.Lock()
	r.loadRepoLocked(repoPath)
	r.mu.Unlock()
}

// Register adds an entry to the registry. Returns error if sessionKey already exists.
func (r *WorktreeRegistry) Register(entry *WorktreeEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.loadRepoLocked(entry.RepoPath)

	if _, exists := r.bySess[entry.SessionKey]; exists {
		return fmt.Errorf("worktree: session %q already registered", entry.SessionKey)
	}

	r.bySess[entry.SessionKey] = entry
	r.byRepo[entry.RepoPath] = append(r.byRepo[entry.RepoPath], entry)
	r.saveRepoLocked(entry.RepoPath)
	return nil
}

// Deregister removes an entry and cleans up empty repo buckets.
func (r *WorktreeRegistry) Deregister(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.bySess[sessionKey]
	if !exists {
		return
	}
	delete(r.bySess, sessionKey)

	entries := r.byRepo[entry.RepoPath]
	for i, e := range entries {
		if e.SessionKey == sessionKey {
			r.byRepo[entry.RepoPath] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(r.byRepo[entry.RepoPath]) == 0 {
		delete(r.byRepo, entry.RepoPath)
	}
	r.saveRepoLocked(entry.RepoPath)
}

// GetPeers returns all entries for a repo, excluding the given sessionKey.
func (r *WorktreeRegistry) GetPeers(repoPath, excludeSessionKey string) []*WorktreeEntry {
	r.ensureLoaded(repoPath)
	r.mu.RLock()
	defer r.mu.RUnlock()

	var peers []*WorktreeEntry
	for _, e := range r.byRepo[repoPath] {
		if e.SessionKey != excludeSessionKey {
			peers = append(peers, cloneEntry(e))
		}
	}
	return peers
}

// GetBySession returns the entry for a given session, or nil.
func (r *WorktreeRegistry) GetBySession(sessionKey string) *WorktreeEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.bySess[sessionKey]
	if !ok {
		return nil
	}
	return cloneEntry(e)
}

// GetPrimary returns the primary entry for a repo, or nil.
func (r *WorktreeRegistry) GetPrimary(repoPath string) *WorktreeEntry {
	r.ensureLoaded(repoPath)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.byRepo[repoPath] {
		if e.Role == "primary" {
			return cloneEntry(e)
		}
	}
	return nil
}

// HasPeers returns true if there are other active entries in the repo.
func (r *WorktreeRegistry) HasPeers(repoPath, excludeSessionKey string) bool {
	r.ensureLoaded(repoPath)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.byRepo[repoPath] {
		if e.SessionKey != excludeSessionKey {
			return true
		}
	}
	return false
}

// ListRepo returns all entries for a repo (including the caller).
func (r *WorktreeRegistry) ListRepo(repoPath string) []*WorktreeEntry {
	r.ensureLoaded(repoPath)
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*WorktreeEntry, len(r.byRepo[repoPath]))
	for i, e := range r.byRepo[repoPath] {
		result[i] = cloneEntry(e)
	}
	return result
}

// UpdateStatus updates the status of an entry.
func (r *WorktreeRegistry) UpdateStatus(sessionKey, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.bySess[sessionKey]; ok {
		e.Status = status
		r.saveRepoLocked(e.RepoPath)
	}
}

func cloneEntry(e *WorktreeEntry) *WorktreeEntry {
	c := *e
	return &c
}

// --- Persistence ---

type registryFile struct {
	Entries []*WorktreeEntry `json:"entries"`
}

func registryPath(repoPath string) string {
	return filepath.Join(worktreeBaseDir(repoPath), "registry.json")
}

// loadRepo loads persisted entries for a repo from disk. Caller must hold r.mu.
func (r *WorktreeRegistry) loadRepoLocked(repoPath string) {
	path := registryPath(repoPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return
	}
	if len(rf.Entries) == 0 {
		return
	}

	for _, e := range rf.Entries {
		if _, exists := r.bySess[e.SessionKey]; exists {
			continue
		}
		if e.WorktreeDir != "" {
			if _, err := os.Stat(e.WorktreeDir); os.IsNotExist(err) {
				continue // orphaned worktree dir gone
			}
		}
		r.bySess[e.SessionKey] = e
		r.byRepo[e.RepoPath] = append(r.byRepo[e.RepoPath], e)
	}
}

// saveRepoLocked persists entries for a repo to disk. Caller must hold r.mu.
func (r *WorktreeRegistry) saveRepoLocked(repoPath string) {
	entries := r.byRepo[repoPath]
	if len(entries) == 0 {
		return
	}

	rf := registryFile{Entries: entries}
	data, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return
	}

	path := registryPath(repoPath)
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	os.Rename(tmpPath, path)
}

// --- Worktree helper functions ---

// GitRepoRoot returns the absolute root of the git repo containing dir.
// Works correctly in both regular repos and git worktrees (uses git-common-dir).
func GitRepoRoot(dir string) (string, error) {
	// Check if this is a git worktree by reading the .git file
	gitFile := filepath.Join(dir, ".git")
	content, err := os.ReadFile(gitFile)
	if err == nil && strings.HasPrefix(string(content), "gitdir:") {
		// It's a worktree — the .git file points to the main repo's metadata.
		// Example: "gitdir: /main/repo/.git/worktrees/name\n"
		gitDirLine := strings.TrimPrefix(string(content), "gitdir:")
		gitDirLine = strings.TrimSpace(gitDirLine)
		// .git/worktrees/name → .git is two levels up
		worktreeMetaDir := filepath.Dir(gitDirLine) // → /main/repo/.git/worktrees
		gitDir := filepath.Dir(worktreeMetaDir)     // → /main/repo/.git
		return filepath.Dir(gitDir), nil            // → /main/repo
	}

	// Regular directory: use git rev-parse
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitIsDirty returns true if the repo at dir has uncommitted changes.
func gitIsDirty(dir string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// worktreeBaseDir returns the base directory for all xbot worktrees.
// Worktrees are placed outside the main repo: {repo}/../.xbot-worktrees/
func worktreeBaseDir(repoPath string) string {
	return filepath.Join(filepath.Dir(repoPath), ".xbot-worktrees")
}

// generateBranchName creates a unique branch name for an agent.
func generateBranchName(role, instance, taskHint string) string {
	sanitize := func(s string) string {
		s = strings.ToLower(s)
		s = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			return '-'
		}, s)
		return strings.Trim(s, "-")
	}

	task := sanitize(taskHint)
	if len(task) > 30 {
		task = task[:30]
	}
	if task == "" {
		task = time.Now().Format("20060102-150405")
	}

	return fmt.Sprintf("agent/%s/%s/%s", sanitize(role), sanitize(instance), task)
}

// createWorktree creates a git worktree and returns its path and branch.
// Uses --detach to work even when the main repo has uncommitted changes.
// The branch is created afterwards in the worktree via checkout -b.
// Caller must hold WorktreeRegistry.mu or ensure serialization externally.
func createWorktree(repoPath, branch string) (worktreePath string, err error) {
	baseDir := worktreeBaseDir(repoPath)
	dirName := strings.TrimPrefix(branch, "agent/")
	dirName = strings.ReplaceAll(dirName, "/", "-")
	worktreePath = filepath.Join(baseDir, dirName)

	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", fmt.Errorf("create worktree base dir: %w", err)
	}

	// Step 1: git worktree add --detach (works even on dirty trees)
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add",
		"--detach", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Step 2: create branch in the new worktree
	brCmd := exec.Command("git", "-C", worktreePath, "checkout", "-b", branch)
	brOut, brErr := brCmd.CombinedOutput()
	if brErr != nil {
		// Rollback: remove the worktree we just created
		removeWorktree(repoPath, worktreePath, "")
		return "", fmt.Errorf("git checkout -b in worktree: %s: %w", strings.TrimSpace(string(brOut)), brErr)
	}

	return worktreePath, nil
}

// removeWorktree removes a git worktree and deletes its branch.
func removeWorktree(repoPath, worktreePath, branch string) error {
	// Remove worktree (--force handles dirty state)
	cmd := exec.Command("git", "-C", repoPath, "worktree", "remove",
		worktreePath, "--force")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Delete branch
	delCmd := exec.Command("git", "-C", repoPath, "branch", "-d", branch)
	delOut, delErr := delCmd.CombinedOutput()
	if delErr != nil {
		// Non-fatal: branch may already be merged/deleted
		_ = delOut
	}

	return nil
}

// pruneOrphanWorktrees cleans up worktree metadata for directories that no longer exist.
// AutoDetectAndInit checks whether the current session needs worktree isolation.
// It is called automatically at session start (before buildPrompt).
//
// - If no other sessions are active in the repo → registers this session as primary (no worktree).
// - If another session is already primary → creates a worktree for this session.
// - Returns the worktree entry (primary or peer), or nil if not a git repo.
func AutoDetectAndInit(workDir, sessionKey string) *WorktreeEntry {
	// Check if in a git repo
	repoPath, err := GitRepoRoot(workDir)
	if err != nil {
		return nil // not a git repo
	}

	// Already registered?
	if entry := GlobalWorktreeRegistry.GetBySession(sessionKey); entry != nil {
		return entry
	}

	// Is there already a primary?
	primary := GlobalWorktreeRegistry.GetPrimary(repoPath)
	if primary == nil {
		// First session → register as primary
		entry := &WorktreeEntry{
			SessionKey: sessionKey,
			Role:       "primary",
			RepoPath:   repoPath,
			Status:     "working",
		}
		if err := GlobalWorktreeRegistry.Register(entry); err != nil {
			return nil
		}
		return entry
	}

	// Another session is primary → create worktree for this session
	branch := generateBranchName("peer", sessionKey, "")
	branch = strings.ReplaceAll(branch, ":", "-") // sessionKey may contain ":"

	// Serialize worktree creation
	GlobalWorktreeRegistry.mu.Lock()
	worktreePath, err := createWorktree(repoPath, branch)
	GlobalWorktreeRegistry.mu.Unlock()
	if err != nil {
		return nil
	}

	entry := &WorktreeEntry{
		SessionKey:  sessionKey,
		Role:        "peer",
		RepoPath:    repoPath,
		WorktreeDir: worktreePath,
		Branch:      branch,
		Status:      "working",
	}
	if err := GlobalWorktreeRegistry.Register(entry); err != nil {
		removeWorktree(repoPath, worktreePath, branch)
		return nil
	}
	return entry
}
