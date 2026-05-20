package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

// WorktreeTool provides git worktree management for multi-agent workspace isolation.
type WorktreeTool struct{}

func (t *WorktreeTool) Name() string { return "Worktree" }

func (t *WorktreeTool) Description() string {
	return `Manage git worktrees for multi-agent workspace isolation.

When multiple agents work on the same git repository, this tool creates isolated worktrees
so agents don't conflict on the same files. Also supports listing and cleanup.

## Actions

### init
Create a new git worktree for the current agent. Registers it in the global registry.
- Every session gets its own worktree — all agents are equal peers.
- Returns the worktree path. You should then Cd to it.

Parameters: {"action": "init", "role": "peer", "instance": "debug", "task": "fix auth bug"}

### cleanup
Remove the worktree for this agent and deregister from the registry.

Parameters: {"action": "cleanup"}

### status
List all active worktrees in the current repo, including peers.

Parameters: {"action": "status"}`
}

type WorktreeParams struct {
	Action   string `json:"action"`
	Role     string `json:"role,omitempty"`
	Instance string `json:"instance,omitempty"`
	Task     string `json:"task,omitempty"`
}

func (t *WorktreeTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: init, cleanup, or status", Required: true},
		{Name: "role", Type: "string", Description: "Agent role: peer, child, or primary (for init)", Required: false},
		{Name: "instance", Type: "string", Description: "Agent instance ID (for init)", Required: false},
		{Name: "task", Type: "string", Description: "Short task description for branch name (for init)", Required: false},
	}
}

func (t *WorktreeTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params WorktreeParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("worktree: parse params: %w", err)
	}

	switch params.Action {
	case "init":
		return t.executeInit(ctx, params)
	case "cleanup":
		return t.executeCleanup(ctx)
	case "status":
		return t.executeStatus(ctx)
	default:
		return nil, fmt.Errorf("worktree: unknown action %q (valid: init, cleanup, status)", params.Action)
	}
}

func (t *WorktreeTool) executeInit(ctx *ToolContext, params WorktreeParams) (*ToolResult, error) {
	cwd := ctx.CurrentDir
	if cwd == "" {
		cwd = ctx.WorkspaceRoot
	}
	if cwd == "" {
		cwd = ctx.WorkingDir
	}

	repoPath, err := GitRepoRoot(cwd)
	if err != nil {
		return nil, fmt.Errorf("worktree init: %w", err)
	}

	// Experimental feature gate: worktree creation requires config opt-in.
	if !ctx.AutoWorktreeEnabled {
		return NewResult(
			"⚠️ Worktree 是实验性功能，默认关闭。\n" +
				"如需启用自动 worktree 隔离，请在 /settings 中设置 `auto_worktree = true`，\n" +
				"或在 config.json 中设置：\n" +
				`  "agent": {"experimental": {"auto_worktree": true}}` +
				"\n\n当前已启用 peer 感知——你可以看到其他 agent 并与之通信。"), nil
	}

	sessionKey := ctx.Channel + ":" + ctx.ChatID
	role := params.Role
	if role == "" {
		role = "peer"
	}
	instance := params.Instance
	if instance == "" {
		instance = ctx.ChatID
	}

	if existing := GlobalWorktreeRegistry.GetBySession(sessionKey); existing != nil {
		return NewResult(fmt.Sprintf("Already registered as %q in worktree: %s (branch: %s)",
			existing.Role, existing.WorktreeDir, existing.Branch)), nil
	}

	// All sessions get a worktree — no primary concept.
	dirty, err := gitIsDirty(repoPath)
	if err != nil {
		return nil, fmt.Errorf("worktree init: check dirty: %w", err)
	}
	dirtyWarning := ""
	if dirty {
		dirtyWarning = "\n⚠️ 主工作区有未提交更改，worktree 将从远程主分支最新状态创建（不含未提交更改）。"
	}

	branch := generateBranchName(role, instance, params.Task)

	GlobalWorktreeRegistry.mu.Lock()
	worktreePath, err := createWorktree(repoPath, branch)
	GlobalWorktreeRegistry.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("worktree init: %w", err)
	}

	entry := &WorktreeEntry{
		SessionKey:  sessionKey,
		Role:        role,
		RepoPath:    repoPath,
		WorktreeDir: worktreePath,
		Branch:      branch,
		Status:      "working",
	}
	if err := GlobalWorktreeRegistry.Register(entry); err != nil {
		removeWorktree(repoPath, worktreePath, branch)
		return nil, fmt.Errorf("worktree init: register: %w", err)
	}

	if ctx.SetCurrentDir != nil {
		ctx.SetCurrentDir(worktreePath)
	}

	msg := fmt.Sprintf("Worktree created successfully.\n"+
		"- Repo: %s\n- Worktree: %s\n- Branch: %s\n- Role: %s%s\n\n"+
		"You are now working in an isolated worktree. Other agents in the same repo will not see your changes until merge.",
		repoPath, worktreePath, branch, role, dirtyWarning)

	peers := GlobalWorktreeRegistry.GetPeers(repoPath, sessionKey)
	if len(peers) > 0 {
		msg += "\n\nActive peers in this repo:"
		for _, p := range peers {
			msg += fmt.Sprintf("\n- %s (%s) on branch %s [%s]", p.SessionKey, p.Role, p.Branch, p.Status)
		}
		msg += "\n\nUse SendMessage to communicate with peers when merging."
	}

	return NewResult(msg), nil
}

func (t *WorktreeTool) executeCleanup(ctx *ToolContext) (*ToolResult, error) {
	sessionKey := ctx.Channel + ":" + ctx.ChatID
	entry := GlobalWorktreeRegistry.GetBySession(sessionKey)
	if entry == nil {
		return NewResult("Not registered in worktree registry. Nothing to clean up."), nil
	}

	if entry.WorktreeDir == "" {
		GlobalWorktreeRegistry.Deregister(sessionKey)
		return NewResult(fmt.Sprintf("Deregistered primary agent for repo %s.", entry.RepoPath)), nil
	}

	if err := removeWorktree(entry.RepoPath, entry.WorktreeDir, entry.Branch); err != nil {
		return NewResult(fmt.Sprintf("Warning: failed to remove worktree: %v\nRegistry entry has been cleaned up.", err)), nil
	}

	GlobalWorktreeRegistry.Deregister(sessionKey)

	// Auto-cd back to the main repo path so the agent doesn't stay in a deleted worktree.
	if ctx.SetCurrentDir != nil {
		ctx.SetCurrentDir(entry.RepoPath)
	}
	// Also update ToolContext.CurrentDir for immediate effect.
	ctx.CurrentDir = entry.RepoPath

	return NewResult(fmt.Sprintf("Worktree cleaned up and deregistered.\n- Removed: %s\n- Branch deleted: %s\n\n已自动 cd 回主工作区: %s",
		entry.WorktreeDir, entry.Branch, entry.RepoPath)), nil
}

func (t *WorktreeTool) executeStatus(ctx *ToolContext) (*ToolResult, error) {
	cwd := ctx.CurrentDir
	if cwd == "" {
		cwd = ctx.WorkspaceRoot
	}
	if cwd == "" {
		cwd = ctx.WorkingDir
	}

	repoPath, err := GitRepoRoot(cwd)
	if err != nil {
		return NewResult(fmt.Sprintf("Not in a git repository: %v", err)), nil
	}

	entries := GlobalWorktreeRegistry.ListRepo(repoPath)
	if len(entries) == 0 {
		return NewResult(fmt.Sprintf("No active worktree agents for repo: %s", repoPath)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Active worktree agents for repo %s:\n", repoPath)
	for _, e := range entries {
		worktreeInfo := "main project"
		if e.WorktreeDir != "" {
			worktreeInfo = e.WorktreeDir
		}
		fmt.Fprintf(&sb, "- [%s] %s | branch: %s | dir: %s | status: %s\n",
			e.Role, e.SessionKey, e.Branch, worktreeInfo, e.Status)
	}

	return NewResult(sb.String()), nil
}
