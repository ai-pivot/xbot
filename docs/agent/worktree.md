# Worktree — Multi-Agent Workspace Isolation

## Overview

`tools/worktree_registry.go` + `tools/worktree.go` implement git worktree-based workspace isolation for parallel agents working on the same repo. The system supports three scenarios:

1. **Peer Sessions (sidebar)**: Multiple independent CLI sessions in the same workDir auto-detect each other and create worktrees
2. **SubAgent**: Parent agent creates worktrees for child SubAgents, which inherit WorkspaceRoot isolation
3. **Best-of-N**: Multiple agents run the same task in parallel worktrees, user picks the best result

## Architecture

```
processMessage (agent/agent.go)
  → AutoDetectAndInit()                          [session start: auto peer detection]
  → buildPrompt()                                [system prompt with CWD set]
  → engine.Run()
    → initDynamicInjector()                      [peer awareness via DynamicContextInjector]
    → buildToolContext()                         [IsWorktreeIsolated → path_guard]
    → postToolProcessing()                       [per-iteration: BuildSystemReminder]
      → queries GlobalWorktreeRegistry           [worktree/peer awareness in sys_reminder]
```

## Key Components

### WorktreeRegistry (`tools/worktree_registry.go`)

Process-level `sync.Map`-based registry. Single source of truth for active worktrees.
**Persisted per-repo** to `{repo}/../.xbot-worktrees/registry.json`:
- `Register`/`Deregister`/`UpdateStatus` → atomic save (tmp+rename)
- `ensureLoaded` → lazy load on first access (thread-safe via `loadedMu`)
- Skip orphaned entries whose worktree directory no longer exists
- Each git repo has its own `registry.json`, isolated from other repos

```go
type WorktreeEntry struct {
    SessionKey  string  // "cli:/path/to/repo:debug" or "agent:role/instance"
    Role        string  // "primary" | "peer" | "child"
    RepoPath    string
    WorktreeDir string  // empty for primary
    Branch      string
    Status      string  // "working" | "merge-ready" | "done"
}
```

Singletons: `GlobalWorktreeRegistry` (accessed by both tools and engine).

### WorktreeTool (`tools/worktree.go`)

Built-in tool registered in `DefaultRegistry` as `RegisterCore`. Actions:

| Action | Description |
|--------|-------------|
| `init` | Auto-detect: first agent→primary, subsequent→worktree |
| `cleanup` | Remove worktree + branch + deregister |
| `status` | List active worktrees in current repo |

### AutoDetectAndInit (`tools/worktree_registry.go`)

Called automatically from `processMessage` (agent/agent.go) BEFORE `buildPrompt`. Flow:

1. Check if workDir is a git repo (`GitRepoRoot`)
2. If session already registered → no-op
3. Generate unique branch name from session key + timestamp
4. `createWorktree` → fetch latest remote main branch → `git worktree add --detach <path> <remote/main-branch>` → `git checkout -b <branch>` → register as peer

On success, `tenantSession.SetCurrentDir(worktreePath)` updates CWD before system prompt construction.

### IsWorktreeIsolated (`tools/interface.go`, `tools/path_guard.go`)

ToolContext field that forces path boundary enforcement:

- `isUnrestricted()` returns `false` when `IsWorktreeIsolated=true`
- This means CLI mode (`sandbox="none"`) still enforces `isWithinRoot` checks
- Prevents worktree agents from escaping their worktree directory

### DynamicContextInjector Extension (`agent/dynamic_context.go`)

Extended with `getPeers` callback. `buildPeerContextXML()` queries `GlobalWorktreeRegistry` for peer entries and formats them as `<peers>` XML injected into tool messages.

### BuildSystemReminder Integration (`agent/reminder.go`)

Per-iteration dynamic reminder that queries `GlobalWorktreeRegistry` for worktree/peer info. Injected into sys_reminder (not cached in sys_prompt):

- **Worktree agent**: shows isolation warning, branch, worktree path, and reminds agent to ask user whether to merge back
- **Primary agent with peers**: lists active peers with their paths/branches
- **peer-dirty agent**: warns about shared workspace with no isolation

Uses `sessionKey` parameter to look up the current session in the registry.

### SubAgent WorkspaceRoot Rewrite (`agent/engine_wire.go`)

In `buildSubAgentRunConfig`, if `InitialCWD` contains `.xbot-worktrees`, the SubAgent's `WorkspaceRoot` is rewritten to the worktree path and `IsWorktreeIsolated` is set to `true`.

### Worktree Skill (`tools/embed_skills/worktree/SKILL.md`)

Embedded skill documenting worktree workflows: peer detection, SubAgent mode, Best-of-N, merge protocol, conflict resolution rules.

## Git Constraints

- Worktree paths MUST be outside the main repo: `{repo}/../.xbot-worktrees/{role}-{instance}/`
- Branch naming: `agent/{role}/{instance}/{task-hint}`
- **Every new worktree starts from the latest remote main branch.** `createWorktree` calls `resolveRemoteMainBranch` which (1) fetches from the first remote, (2) detects the default branch via `refs/remotes/<remote>/HEAD` symbolic ref (falls back to checking `main`/`master`), then uses `<remote>/<main-branch>` as the start point. Falls back to local HEAD when no remote is configured.
- `git worktree add` fails on dirty trees → AutoDetectAndInit returns nil (falls back to main project)
- Worktree creation is serialized via `GlobalWorktreeRegistry.mu.Lock()` to prevent `.git/worktrees/` lockfile contention
- **CWD persisted via session_cwd files.** `SetCurrentDir` writes to `~/.xbot/session_cwd/{hash}.txt`. On restart, `loadPersistedCWD` restores the CWD. `SetCWD` (terminal sync from CLI startup) only overwrites when the session has no persisted CWD (new session) or when the persisted directory no longer exists (stale). This ensures user-initiated `Cd` tool paths survive restarts even when they differ from the terminal launch dir.
- **AutoDetectAndInit returns `created` boolean.** Callers use this to distinguish "new worktree just created" (should set CWD to worktree dir) from "existing entry loaded from disk on restart" (should preserve user's persisted CWD).
- **`loaded` map is per-WorktreeRegistry, not package-level.** Each registry instance tracks its own disk-load state independently, fixing test isolation where multiple registries exist in the same process.
- **Experimental: auto_worktree config (default false).** `config.json` → `agent.experimental.auto_worktree`. When enabled, AutoDetectAndInit creates worktrees automatically. When disabled, `RegisterPeer` provides lightweight session tracking for peer awareness without file isolation.
- **Peer awareness always on.** `BuildSystemReminder` shows all peers from WorktreeRegistry regardless of auto_worktree setting. Collaboration rules: respect peer changes, use SendMessage for verification delegation.
- **`SendMessage` supports P2P busy/idle routing.** `Agent.injectPeerMessage` checks `chatCancelCh`: busy → injects fake tool result in current iteration, idle → pushes user message to start new turn. Accessible via `PeerMessageFn` on ToolContext. Worktree sessions auto-restore correct CWD without waiting for first message.

## Path Security

| Sandbox Mode | Without Worktree | With IsWorktreeIsolated |
|-------------|-----------------|------------------------|
| `none` (CLI) | Unrestricted | Enforced (isWithinRoot on worktree dir) |
| `docker` | Container-bound | Container-bound + worktree-root |
| `remote` | Runner-enforced | Runner-enforced |

## Merge Protocol

Agent communication uses structured JSON over SendMessage:

```json
{"protocol": "xbot.merge-coordination.v1", "type": "ready|conflict-proposal|accept|escalate", ...}
```

Conflict resolution:
- No overlap → auto-merge
- Test file conflict → tester version preferred
- Source conflict, agents agree → negotiate
- 3 rounds no consensus OR semantic conflict → escalate to user

## Gotchas

- **Primary registration must NOT check dirty tree.** Only worktree creation requires a clean tree.
- **AutoDetectAndInit depends on `a.workDir`**, not the session's CWD. For CLI sessions this is the repo root.
- **`go:embed embed_skills/*`** picks up the worktree skill directory automatically — no code change needed for new embed skills.
- **`resolveRemoteMainBranch` fetch is best-effort.** Network errors are silently ignored — cached remote refs are used instead. For repos without any remote, `createWorktree` falls back to local HEAD (same as pre-fetch behavior).
