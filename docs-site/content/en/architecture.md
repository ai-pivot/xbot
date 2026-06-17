---
title: "Architecture"
weight: 50
---

# xbot Architecture

## Global Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          User Message Entry                          │
│  Feishu / QQ / NapCat / Web / CLI / Cron / !bang_command           │
└─────────────┬───────────────────────────────────────────────────────┘
              │ InboundMessage (cap=64)
              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Agent.processMessage()                        │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐  │
│  │ CommandRegistry│ → │ Pipeline     │ → │ engine.Run()          │  │
│  │ slash/! cmds   │    │ middleware    │    │ LLM ↔ Tool loop      │  │
│  └──────────────┘    └──────────────┘    └──────────────────────┘  │
│         │                    │                     │                 │
│         │              system prompt           context mgmt         │
│         │              + memory injection      ├─ Offload (Layer 1) │
│         │              + tool catalog          ├─ Masking (Layer 2) │
│         │              + user message          ├─ ContextEdit (L3)  │
│         │                                     └─ Compress          │
│         ▼                                                           │
│  OutboundMessage → Dispatcher → Channel.Send()                      │
└─────────────────────────────────────────────────────────────────────┘
              │
     ┌────────┴────────┐
     ▼                 ▼
  SQLite (.xbot/xbot.db)    Vector DB (.xbot/vectordb/)
  sessions/memory/config    Archival memory embeddings
```

---

## 1. Overall Architecture

### 1.1 Entry Points

xbot has three independent executables:

| Binary | Entry File | Purpose |
|--------|-----------|---------|
| **xbot** (server) | `main.go` | Main service: LLM Agent engine + multi-channel message gateway |
| **xbot-cli** | `cmd/xbot-cli/main.go` | Standalone terminal chat UI (single user, direct filesystem access) |
| **xbot-runner** | `cmd/runner/main.go` | Remote sandbox runner: connects to server over WebSocket |

### 1.2 Server Startup Flow

```
config.Load() → createLLM() → bus.NewMessageBus()
→ storage.MigrateIfNeeded() → OAuth init
→ tools.InitSandbox() → agent.New(cfg)        ← sandbox initialized before Agent (sync.Once)
→ register tools → IndexGlobalTools()
→ channel.NewDispatcher(msgBus) → register channels
→ start Agent event loop and channels
```

### 1.3 Package Map

```
xbot/
├── main.go                  # Server entry point
├── cmd/
│   ├── xbot-cli/            # Standalone CLI client
│   └── runner/              # Remote sandbox runner
├── config/                  # Configuration system (JSON + env vars)
├── bus/                     # Message bus (InboundMessage / OutboundMessage)
├── channel/                 # Channel layer (abstract + implementations + Dispatcher)
├── agent/                   # Agent core engine
├── llm/                     # LLM abstraction (OpenAI / Anthropic / Proxy)
├── session/                 # Multi-tenant session management
├── memory/                  # Memory system (flat / letta)
├── storage/
│   ├── sqlite/              # SQLite data service layer
│   └── vectordb/            # Vector database (chromem-go)
├── tools/                   # Tool system (including Worktree, config, tui_control)
├── plugin/                  # Plugin system (RPC bridge, runtimes, extension points)
├── internal/
│   ├── runnerclient/        # Runner client (runner-side)
│   └── runnerproto/         # Runner protocol definitions
├── serverapp/               # Server core, RPC handlers
├── oauth/                   # OAuth service
├── event/                   # Event system (webhook receive + trigger routing)
├── cron/                    # Scheduled task scheduler
├── crypto/                  # Encryption utilities
├── prompt/                  # Embedded prompt templates (go:embed)
├── version/                 # Version info
├── agents/                  # [runtime] Agent role definitions (~/.xbot/agents/), not a source package
└── web/                     # Frontend static assets (React 19 + Vite + TailwindCSS 4 + Tiptap)
```

### 1.4 Message Flow

```
User Message
  │
  ▼
Channel Implementation (feishu / qq / napcat / web / cli)
  │ Parse platform message → construct bus.InboundMessage
  │ Inject into bus.Inbound (cap=64 buffer)
  ▼
Agent.processMessage()
  │ 1. Normalize SenderID (singleUser mode → "default")
  │ 2. CommandRegistry.Match() — slash/! command dispatch
  │    ├─ Concurrent()=true → goroutine (does not consume semaphore)
  │    └─ Concurrent()=false → msgCh serial queue (cap=32)
  │ 3. Get or create TenantSession
  │ 4. Fetch message history + eager-save user message
  │ 5. Pipeline.Run() → assemble system prompt + history + user message
  │ 6. engine.Run()
  ▼
engine.Run() — core loop (see §2.2)
  │
  ▼
OutboundMessage → bus.Outbound → Dispatcher.Run()
  │
  ▼
Channel.Send(msg) → platform API
```

### 1.5 Channel Abstraction

**Interface**:
```go
type Channel interface {
    Name() string
    Start() error
    Stop()
    Send(msg bus.OutboundMessage) (string, error)
}
```

**Dispatcher**: reads from `bus.Outbound`, routes by `msg.Channel` to the corresponding Channel. Supports observer pattern and `SendDirect()` for synchronous sends.

| Channel | Protocol | Reconnect Strategy |
|---------|----------|--------------------|
| Feishu | Lark SDK (WebSocket) | SDK managed |
| QQ | QQ Bot WebSocket | Exponential backoff (1s→60s), fast disconnect detection (3 in 5s → 60s cooldown), Intent downgrade, Resume support |
| NapCat | OneBot 11 WebSocket | Exponential backoff (same as QQ), 30s long-connection reset |
| Web | HTTP + WebSocket | Client-side reconnect; server-side WS Ping 30s, ReadDeadline 60s, offline ringBuffer(50) |
| CLI | Terminal | N/A |

### 1.6 Slash Commands & Bang Commands

**CommandRegistry** (`agent/command.go`): linear scan matching in registration order, first match wins.

**Bang commands** (`!` prefix): execute shell commands directly inside the sandbox, marked `Concurrent()=true` (does not consume semaphore). Timeout 120s, output > 4000 chars written to file.

---

## 2. Agent / Loop Architecture

### 2.1 Agent Struct

`Agent` (`agent/agent.go`) is the system core, holding:

- `bus` — message bus reference
- `multiSession` — multi-tenant session management
- `tools` — tool registry
- `contextManager` — context manager (interface, hot-swappable at runtime)
- `offloadStore` — large result offload storage
- `maskStore` — observation mask storage
- `contextEditor` — context editor
- `llmFactory` — user-custom LLM factory
- `skills/agents` — skill/agent directories
- `registryManager` — marketplace/sharing
- `settingsSvc` — user settings
- `interactiveSubAgents` — interactive SubAgent session pool
- `hookChain` — tool execution hook chain (shared with SubAgents; replaced by `agent/hooks.Manager`)
- `bgTaskMgr` — background task manager
- `pluginRegistry` — plugin registry

### 2.2 Engine Run Loop

`engine.Run()` entry in `agent/engine.go` is the unified agent loop, shared by main Agent and SubAgents. Internal sub-functions split into `agent/engine_run.go` (runState, compress, LLM calls, etc.), tool execution and SubAgent spawning in `agent/engine_wire.go`.

**RunConfig differentiated injection**:
- **Main Agent**: ToolExecutor=full (session MCP + hooks), ProgressNotifier=sendMessage, ContextManager=global, Memory=enabled, EnableReadWriteSplit=true
- **SubAgent**: ToolExecutor=simplified, ProgressNotifier=nil, ContextManager=independent phase1, Memory=optional

**Core loop logic** (`engine.go`):
```
for i := 0; i < maxIter; i++ {
    ① maybeCompress()              ← 75% token threshold + 5 round cooldown
    ② LLM Generate()               ← perAttemptCtx 120s independent timeout
    ③ No tool_calls → exit loop
    ④ Tool execution (read/write split parallel or serial)
    ⑤ MaybeOffload()               ← Layer 1: large result offload
    ⑥ InvalidateStaleReads()       ← detect stale Read offloads
    ⑦ PurgeStaleMessages()         ← replace stale offloads with warnings
    ⑧ DynamicContext injection     ← stale detection results
    ⑨ System Reminder injection    ← strip old reminder each round
    ⑩ Incremental persist: Session.AddMessage()
}
```

**Token count recovery**: from in-memory `lastPromptTokens` (in-process), or from `tenant_state` table (after restart). Written back to DB + memory after Run completes.

**Read/write split parallel** (`EnableReadWriteSplit`):
- Phase 0: SubAgent concurrent (constrained by SubAgentSem)
- Phase 1: Read-only tools parallel (Read/Grep/Glob/WebSearch/ChatHistory, maxParallel=8)
- Phase 2: Write tools serial

**Read-only tool set** (hardcoded `readOnlyTools` map; new additions require manual updates).

### 2.3 Pipeline Middleware Chain

Executed in priority order, assembling system prompt parts into the `SystemParts` map, finally concatenated by key dictionary order:

| Order | Middleware | Priority | SystemParts Key | Responsibility |
|-------|-----------|----------|-----------------|----------------|
| 1 | SystemPromptMiddleware | 0 | `00_base` | Render prompt.md (hot reload) |
| 2 | ProjectHintMiddleware | 1 | `05_project_hint` | Inject known project knowledge cards |
| 3 | SkillsCatalogMiddleware | 100 | `10_skills` | Available skills catalog |
| 4 | AgentsCatalogMiddleware | 110 | `15_agents` | Available agents catalog |
| 5 | MemoryMiddleware | 120 | `20_memory` | Long-term memory (Recall) |
| 6 | SenderInfoMiddleware | 130 | `30_sender` | Sender name |
| 7 | LanguageMiddleware | 135 | `32_language` | Language instructions |
| 8 | UserMessageMiddleware | 200 | — | Timestamp + guide text |

**CacheHint**: system prompt marked `"static"`, converted to `cache_control: {type: "ephemeral"}` under Anthropic.

### 2.4 Multi-Tenant Session Management

**MultiTenantSession** (`session/multitenant.go`): isolates sessions by `(channel, chatID)`, internal cache supports 24h TTL expiry, **no capacity limit**.

**TenantSession** (`session/tenant.go`): holds tenantID, MemoryProvider, SessionMCPManager, CWD state.

**Tenant isolation**:
- IM users: `(channel, chatID)` → positive tenantID
- SubAgent: SHA-256 hash → negative tenantID

### 2.5 SubAgent Mechanism

**One-shot SubAgent**: created via `bus.InboundMessage{Channel="agent", AllowedTools, SystemPrompt, RoleName}`. Optional capabilities: `memory`, `send_message`, `spawn_agent`.

**Interactive SubAgent**: persistent sessions stored in `sync.Map`, supporting multi-turn dialog, 30 min inactivity auto-cleanup. Key format: `channel:chatID/roleName[:instance]`.

---

## 3. Context Architecture

### 3.1 Message Types

```go
type ChatMessage struct {
    Role             string     // "system" / "user" / "assistant" / "tool"
    Content          string     // LLM-visible content
    ReasoningContent string     // DeepSeek/OpenAI reasoning models
    ToolCallID/Name/Arguments   // Tool call/result identifiers
    ToolCalls        []ToolCall // Assistant's tool call list
    Detail           string     // Full content (persisted to DB, used by Web/CLI display)
    DisplayOnly      bool       // Display only (not loaded into LLM context)
    CacheHint        string     // "static" = cacheable across requests
}
```

### 3.2 Four-Layer Context Management Strategy

The four layers form a **defense-in-depth** strategy, triggered at different points in `engine.Run()`:

```
Single tool result too large
    ↓ exceeds 2000 tokens or 10240 bytes
Layer 1: Offload → replace with 📂 [offload:ol_xxxx] + summary
    ↓ auto-marked STALE when file modified

Too many old tool results
    ↓ triggered after each compress (token reaches 60%)
Layer 2: Masking → replace with 📂 [masked:mk_xxxx] + summary
    ↓ grouped by consecutive tool groups, keep recent active groups

Overall context too large
    ↓ totalTokens >= 75% maxTokens
Layer 3: Compress → LLM structured compression

LLM-initiated editing
    ↓ any time
Layer 4: ContextEdit → precise delete/truncate/replace
```

#### Layer 1: Offload (Large Result Offload)

- **Trigger**: `MaybeOffload()` — single tool result exceeds threshold
- **Storage**: filesystem `.xbot/offload_store/{sessionKey}/id.json`
- **Summary generation**: rule-based summary per tool type (Read/Grep/Shell/Glob/default)
- **Read special**: computes ContentHash (SHA-256) for stale detection
- **Stale detection**: re-read file after each tool execution, compare hash; mismatch → replace with expiry warning
- **Recall**: `offload_recall` tool, supports pagination (offset/limit, max 16000 runes)
- **Anti-recursion**: results from offload_recall and recall_masked are not offloaded again
- **Read offset protection**: Read results with offset/limit are not offloaded
- **SubAgent path**: SubAgent offload data stored under parent session directory (RootSessionKey)

#### Layer 2: Observation Masking

- **Trigger**: `MaskOldToolResults()` — called inside `maybeCompress()` (token reaches 60%)
- **Grouping strategy**: assistant[tool_calls] + consecutive tool messages = 1 group
- **Retention strategy**: keepGroups dynamically computed by ratio (3/5/8/12 groups)
- **Active file protection**: files touched in last 3 turns are not masked
- **Collapse optimization**: pure tool groups (assistant.Content empty) collapsed to a single message pair, reducing message count
- **Storage**: in-memory `ObservationMaskStore`, FIFO eviction, dual limit (200 entries / 2MB chars)
- **Recall**: `recall_masked` tool
- **Post-compress cleanup**: `CleanOldEntries(cutoff)` removes records prior to compression point

#### Layer 3: Context Edit

- **Tool**: `context_edit` — called by LLM proactively
- **Operations**: `delete_turn` / `delete` / `truncate` / `replace` (regex support)
- **Safety**: cannot edit system messages, cannot delete last 3 messages

### 3.3 Context Compression

**Trigger conditions** (four paths):

| Path | Condition | Method |
|------|-----------|--------|
| Auto compress | `totalTokens >= 75% maxTokens` + 5 rounds cooldown | `cm.Compress()` |
| Input too long | LLM returns InputTooLong error | `cm.ManualCompress()` |
| Context window exceeded | `finish_reason = context_window_exceeded` | `cm.Compress()` |
| Manual /compress | User command | `cm.ManualCompress()` |

**Phase 1 compression flow**:
1. Find tail cut point (last user/assistant message)
2. Select messages to compress from tail backwards by token budget
3. LLM call (max 10 multi-turn rounds, may call memory tools to organize memories)
4. Build results: LLMView + SessionView (excluding system/display_only)
5. **Persistence**: `Session.Clear()` + per-message `AddMessage(SessionView)`
6. **Cleanup**: delete offload files and mask entries prior to compression point

### 3.4 Message Persistence

**SessionService**: appends to `session_messages` table.

| Timing | Method |
|--------|--------|
| User message | eager-save before Run() |
| After each iteration | incremental persist `messages[lastPersistedCount:]` (skip system + strip reminder) |
| After compression | `Session.Clear()` + per-message `AddMessage(SessionView)` |
| After masking | `UpdateMessageContent(idx)` in-place update |
| After cancel | save interrupted iteration progress |
| After Run() returns | save final assistant reply |

**GetHistory**: uses user messages as turn boundaries, fetches messages starting from the Nth user message.

---

## 4. Storage Architecture

### 4.1 SQLite

Single SQLite file, WAL mode, `MaxOpenConns=1` (SQLite single-writer model), `BusyTimeout=5000ms`. Schema Version 21.

| Table | Purpose |
|-------|---------|
| `tenants` | Tenants (channel+chatID → tenantID) |
| `session_messages` | Session message history |
| `tenant_state` | Tenant state (last_consolidated, token counts) |
| `long_term_memory` | Long-term memory (flat mode) |
| `event_history` | Event history (Letta recall, FTS5 index) |
| `user_profiles` | User profiles |
| `core_memory_blocks` | Core memory blocks (Letta persona/human/working_context) |
| `archival_memory` | Archival memory (vector index BLOB) |
| `cron_jobs` | Scheduled tasks |
| `runners` | Runner management |
| `shared_registry` | Skill/Agent marketplace |
| `web_users` | Web user authentication |
| `user_settings` | User settings |
| `user_llm_configs` | User custom LLM configuration |
| `user_token_usage` | User token usage statistics |
| `schema_version` | Schema version |

**Data migration**: `storage.MigrateIfNeeded()` tracks version via `schema_version` table, executing incremental migrations step by step.

**DB path**: Server: `{workDir}/.xbot/xbot.db`, CLI: `~/.xbot/xbot.db`. **Data is not shared between the two modes.**

### 4.2 Memory System

**MemoryProvider interface**:
```go
type MemoryProvider interface {
    Recall(ctx, query) (string, error)
    Memorize(ctx, MemorizeInput) (MemorizeResult, error)
    Close() error
}
```

| Mode | Recall | Memorize | Use Case |
|------|--------|----------|----------|
| `flat` | Returns long_term_memory full text directly | LLM compresses conversation → writes to long_term_memory | Simple scenarios |
| `letta` | Returns Core Memory structured blocks | LLM rethink → update Core + write to Archival | Complex long-term memory |

**Memorize trigger timing** (⚠️ NOT triggered after every conversation):
- `/new` command (session switch) — `ArchiveAll=true`
- SubAgent exit — `ArchiveAll=true`
- During compression, called by LLM via memory tools

**Letta Memory**:
- **Core Memory**: 3 structured blocks (persona / human / working_context), injected into system prompt
- **Archival Memory**: embedding vector storage, retrieved on-demand via tools, with deduplication (search similarity > 0.5)
- **Recall Memory**: time-range-based session history search (FTS5)

### 4.3 Vector Database

Uses `chromem-go` embedded vector database, persisted to `.xbot/vectordb/`. Supports OpenAI and Ollama embedding providers, with automatic truncation of overlong content.

---

## 5. Tool System

### 5.1 Tool Categories

| Category | Tools |
|----------|-------|
| **File Operations** | Read, Edit, Glob, Grep, Cd, DownloadFile |
| **Command Execution** | Shell |
| **Search** | WebSearch, ChatHistory |
| **Memory** | save_memory (flat), core_memory_*, archival_memory_*, recall_memory_search (letta) |
| **Context Management** | offload_recall, recall_masked, context_edit |
| **SubAgent & Collaboration** | SubAgent (one-shot + Interactive), CreateChat, SendMessage, Worktree |
| **Skill/Agent** | Skill, manage_tools, search_tools, load_tools |
| **AI-Native Config** | config (AI reads/writes config), tui_control (AI operates TUI) |
| **Cards** | card_create, card_add_content, card_add_interactive, card_add_container, card_preview, card_send |
| **Tasks** | todo_write/todo_list (cross-session persistent), cron, task_start/task_status/task_cancel |
| **Management** | Logs, ask_user, OAuth, EventTrigger |
| **Feishu MCP** | 20+ tools (Bitable, Knowledge Base, Docs, Drive) |

### 5.2 Hooks System

The old `ToolHook`/`HookChain` has been replaced by `Manager` in `agent/hooks/`.

**17 lifecycle events**: SessionStart, SessionEnd, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, PermissionRequest, PermissionDenied, SubAgentStart, SubAgentStop, AgentStop, AgentError, PreCompact, PostCompact, CronFired, WebhookReceived

**4 handler types**: command (shell command), http (HTTP POST), mcp_tool (MCP tool call), callback (Go function)

**Decision priority** (multi-handler conflicts): `deny > defer > ask > allow`

**Configuration**: three-layer merge (`~/.xbot/hooks.json` → `<project>/.xbot/hooks.json` → `<project>/.xbot/hooks.local.json`), project-level can be committed to git.

### 5.3 Tool Activation/Expiration

**coreTools vs sessionActivated**:

| Attribute | coreTools | sessionActivated |
|-----------|-----------|-----------------|
| Appear in tool definitions | Always | When activated and not expired |
| Expiry | Never | Auto-expire after 5 consecutive rounds unused |
| Flat mode | Same as above | Register = RegisterCore, never expires |

- `TickSession()`: increment round on each new user message
- `TouchTool()`: refresh active time before tool execution
- `DeactivateSession()`: cleanup when session evicted from cache

### 5.4 MCP Integration

**Global MCP**: `GetMCPCatalog()` tool name prefix `mcp_{server}_{tool}`.
**Session MCP**: `SessionMCPManager` per-user independent, lazy-loaded (connect on first access), unload after 30 min inactivity.

**Stub mechanism**: unactivated MCP tools return `Parameters()` = nil (LLM invisible); activated = expose full schema. `search_tools` can fetch full schema for semantic search.

### 5.5 Sandbox System

**Sandbox interface**: Name, Workspace, Exec, ReadFile, WriteFile, Stat, ReadDir, MkdirAll, Close.

**SandboxRouter** (`tools/sandbox_router.go`): unified sandbox entry, routes per-user to different backends. Implements `Sandbox` and `SandboxResolver` interfaces. Routing rules (per-user, determined by `user_settings.active_runner`):
- `active_runner == "__docker__"` → DockerSandbox (if enabled)
- `active_runner == specific remote runner name` → corresponding RemoteSandbox connection (if connected)
- Fallback: Remote → Docker → None

Supports simultaneously holding Docker and Remote instances (dual-mode), routes independently per user. Users can switch active runner in settings panel.

**Routing strategy**: Remote Runner → Docker → None priority.

**Remote Runner / Multi-Runner**: RemoteSandbox supports multiple simultaneous runner connections, each with independent name and token. Runners connect via WebSocket, token auth (`subtle.ConstantTimeCompare`), `runners` table manages tokens. Users select runner by name in settings (`active_runner`). Sync configuration supports syncing skills/agents directories to runner side.

**DockerSandbox**: per-user independent container, stop (not rm) for reuse on next session, DinD mode path translation.

**RemoteSandbox**: WebSocket server waits for runner connections, token auth (`subtle.ConstantTimeCompare`), request-response pattern, supports stdio streaming output and runner-local LLM.

---

## 6. Configuration System

### 6.1 Config Loading Priority

```
1. .env file (godotenv)              ← lowest
2. config.json (LoadFromFile, merge non-zero fields)
3. Env var overrides (applyEnvOverrides)  ← highest
```

### 6.2 XBOT_HOME & Path Resolution

```go
func XbotHome() string {
    dir := os.Getenv("XBOT_HOME")       // priority 1: env var
    if dir == "" {
        dir = filepath.Join(home, ".xbot")  // priority 2: ~/.xbot
    }
    os.MkdirAll(dir, 0o755)
    return dir
}
```

**Used for**: `config.json`, `xbot.db`, `offload_store/`, `skills/`, `agents/`.

### 6.3 Runtime Configuration

- **User settings**: `user_settings` table, modified via Web/Feishu/TUI settings panels
- **LLM subscriptions**: `user_llm_subscriptions` table (Server mode) or `config.json` `subscriptions` array (CLI mode) — single source of truth for LLM config. Supports Model Tiers (vanguard/balance/swift) and runtime switching
- **AI-Native config**: `config` tool lets Agent read/write config directly; `tui_control` tool lets Agent operate the TUI
- **Context mode**: hot-swappable at runtime (`SetContextMode`)
- **MaxConcurrency**: adjustable at runtime (settings panel)

---

## 7. Error Handling & Fault Tolerance

### 7.1 LLM Retry Strategy

| Parameter | Default | Env Var |
|-----------|---------|---------|
| Max attempts | 5 | `LLM_RETRY_ATTEMPTS` |
| Initial delay | 1s | `LLM_RETRY_DELAY` |
| Max delay | 30s | `LLM_RETRY_MAX_DELAY` |
| Single call timeout | 120s | `LLM_RETRY_TIMEOUT` |

- **Backoff**: exponential backoff + random jitter + 429 extra backoff (`2^(min(n,4))` s)
- **perAttemptCtx**: each retry creates a fresh 120s timeout context, does not inherit parent ctx deadline, but propagates cancellation signal
- **Retryable**: 429 / 5xx / network errors / timeout; non-retryable: context.Canceled / 4xx
- **Input too long**: detects `InputTooLong`, forces compression, bypasses retry framework
- **Retry notification**: pushes progress message via `RetryNotifyFunc`: `⚠️ LLM request failed (rate limit), retrying 2/5 ...`

### 7.2 Per-Tenant LLM Concurrency

| LLM Type | Default Concurrency Cap |
|----------|------------------------|
| Global LLM | 5 |
| User Custom LLM | 3 |
| SubAgent | 3 |

Controlled by `LLMSemaphoreManager` per-tenant semaphore. Released immediately after Generate completes (no defer).

### 7.3 Tool Execution Failure Handling

- **execErr**: error converted to tool message `"Error: <details>\nPlease fix the issue and try again."`, loop continues
- **IsError**: hint `"Do NOT retry the same command. Analyze the error, fix the root cause."`
- **Timeout**: Shell default 120s (max 600s); on timeout auto-converts to background task (BgTaskManager.Adopt/Start)
- **OAuth**: matches OAuth flow, returns alternative content, enters wait-for-user-interaction state

### 7.4 Message Bus

Inbound/Outbound both have cap=64 buffered channels. **No backpressure handling** — senders block when full. Web channel's `sendToClient()` uses select + default non-blocking write, degrading to offline ringBuffer(50) when full.

---

## 8. Security Model

### 8.1 Encrypted Storage

Tokens stored as plaintext by default (DB colocated with server process). If `XBOT_ENCRYPTION_KEY` is set, encryption still applies (AES-256-GCM, backward compatible with old data).

Encrypted data: OAuth access_token/refresh_token, user LLM api_key.

### 8.2 Authentication

| Mechanism | Implementation |
|-----------|---------------|
| Web login | bcrypt hash, in-memory sessions (30 days), HttpOnly + SameSite=Lax |
| Runner token | 256-bit random, `subtle.ConstantTimeCompare`, dual verification (tokenStore + authToken) |
| Admin | Single ADMIN_CHAT_ID (fallback to STARTUP_NOTIFY_CHAT_ID), Logs tool permission check |
| AllowFrom | Exact match whitelist (comma-separated), empty = allow all |
| OAuth CSRF | 16-byte random state token |

### 8.3 Input Validation

- **PathGuard** (`internal/runnerclient/pathguard.go`): `filepath.EvalSymlinks` resolve symlinks then check prefix
- **Docker container names**: regex validation `^[a-z0-9][a-z0-9_.-]{0,127}$`
- **Process groups**: `Setpgid=true`, kill entire process group on timeout

---

## 9. Performance & Rate Limiting

### 9.1 Five-Layer Throttling

```
Layer 1: Global concurrency semaphore (MaxConcurrency=3)
Layer 2: Per-Chat queue (cap=32, drop when full)
Layer 2.5: Per-user semaphore (custom LLM users, cap=1)
Layer 3: LLM Per-Tenant semaphore (Global=5, Personal=3, SubAgent=3)
Layer 4: LLM retry + single timeout (120s × 5 = max 600s)
Layer 5: Tool timeout (120s) + background task (24h safety ceiling)
```

### 9.2 Timeout Reference

| Component | Timeout |
|-----------|---------|
| Shell tool | default 120s, max 600s |
| Docker commands | 30s (slow ops 120s) |
| Remote exec | 60s + 5s |
| Remote WebSocket | Ping 30s, Pong 60s |
| Web HTTP | Read 10s, Write 60s, Idle 120s |
| Web WebSocket | Ping 30s, ReadDeadline 60s |
| Background tasks | 24h safety ceiling, 50KB output truncation |
| LLM single call | 120s |
| LLM single call (Anthropic HTTP Client) | 300s |
