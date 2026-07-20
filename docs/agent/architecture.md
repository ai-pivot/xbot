# Architecture

## Package Map

```
cmd/xbot-cli/     CLI entry point, app wiring, subscription management
cmd/runner/       Remote runner process (sandbox execution)
agent/            Agent loop, LLM orchestration, middleware pipeline
channel/          Channel adapters: CLI (BubbleTea), Feishu, QQ, Web
llm/              LLM client abstraction (OpenAI, Anthropic), retry, streaming
memory/           Pluggable memory: letta/ (archival+core), flat/ (in-memory)
plugin/           Plugin system: extensible tools, hooks, context enrichers
runner/           Runner management: tool providers, session binding, skill/agent discovery
tools/            Tool interface + Registry + ToolProvider; sandbox abstraction
session/          Multi-tenant session management
storage/          SQLite persistence, vector DB for archival memory
config/           JSON config (~/.xbot/config.json), env var overrides
prompt/           Go embed templates for system prompt construction
event/            Event routing and trigger system
cron/             Cron scheduler for timed tasks
oauth/            OAuth 2.0 server and providers
```

## Message Flow

```
User Message → Bus.Inbound → Dispatcher → Channel.HandleMessage
  → Agent.HandleMessage → chatProcessLoop → runState.Run()
    → Pipeline.Assemble(system prompt) → LLM.Generate()
    → executeToolCalls() → toolExecutor() → hooks.Manager.Emit(PreToolUse) → Tool.Execute() → hooks.Manager.Emit(PostToolUse)
    → results → LLM.Generate() → ... (loop up to maxIterations)
    → ExtractFinalReply() → Bus.Outbound → Dispatcher → Channel.Send()
```

## System Prompt Pipeline

`agent/middleware.go` — `MessagePipeline` executes ordered `MessageMiddleware` chain.
Registered in `agent/context.go:initPipelines()`.

| Priority | Middleware | Key | Purpose |
|----------|-----------|-----|---------|
| 0 | SystemPromptMiddleware | `00_base` | Render prompt.md template |
| 5 | ProjectContextMiddleware | `04_global_context` + `05_project_context` | Load AGENTS.md (global + project) |
| 100 | SkillsCatalogMiddleware | `10_skills` | Inject skill names+descriptions |
| 110 | AgentsCatalogMiddleware | `15_agents` | Inject subagent catalog |
| 115 | PermissionControlMiddleware | `14_perm_control` | OS user permission control |
| 120 | MemoryMiddleware | `20_memory` | Core memory (persona/human/working_context) |
| 130 | SenderInfoMiddleware | `30_sender` | Sender name |
| 135 | LanguageMiddleware | `32_language` | Language preference |
| 150 | PluginEnricherMiddleware | `plugin_enrichers` | Plugin context enrichers |
| 200 | UserMessageMiddleware | — | Timestamp + user message wrapping |

## Tool Execution

```
LLM Response → executeToolCalls() → execOne() → toolExecutor()
  → Manager.Emit(PreToolUse) → tool.Execute() → Manager.Emit(PostToolUse)
```

Two modes (`agent/engine_run.go`):
- **Normal**: all serial
- **ReadWrite split**: read tools parallel (max 8), write tools serial, SubAgent concurrent

## Key Interfaces

| Interface | File | Purpose |
|-----------|------|---------|
| `LLM` | `llm/interface.go` | Generate, ListModels, Stream |
| `Tool` | `tools/interface.go` | Execute, Definition, Name |
| `ToolProvider` | `tools/tool_provider.go` | Unified tool source: Name, ListTools, GetTool, Priority |
| `Sandbox` | `tools/sandbox.go` | Run, Sync, Resolve (migrating to runner) |
| `Channel` | `channel/channel.go` | Start, Stop, Send |
| `MessageMiddleware` | `agent/middleware.go` | Process(mc) |
| `MemoryProvider` | `memory/memory.go` | Core + Archival memory |
| `Transport` | `agent/transport.go` | Pure transmission: Call(method, payload) → (response, error) |
| `Client` | `agent/client.go` | Unified RPC client: all methods = Transport.Call() |
| `RunnerManager` | `runner/manager.go` | Runner CRUD, session binding, ResolveSession |
| `ServerCore` | `serverapp/server_core.go` | Shared server core: Agent + RPCTable + Bus (local & remote) |

## Subscription System

LLM 配置通过**订阅（Subscription）**系统管理，不再使用全局单一 `llm` 字段。

- **CLI 模式**: 订阅存储在 `~/.xbot/config.json` 的 `subscriptions` 数组中
- **Server 模式**: 订阅存储在 `user_llm_subscriptions` 表中，为单一真相来源
- **Model Tiers**: 支持 Vanguard / Balance / Swift 三层模型分级，可按场景选用
- **Tier Fallback**: 未配置的层自动回退：vanguard → balance → swift
- **运行时切换**: `Ctrl+N` LLM 面板或 `/set-model <model>` 命令实时切换模型（跨订阅）

`GetLLMForModel` 必须同时检查 CLI 配置订阅和 DB 订阅。`user_llm_subscriptions` 的字段（provider, model, base_url, api_key, max_output_tokens, thinking_mode）是订阅级作用域，**不得**出现在 `user_settings` 表中。

## Concurrency Model

- Agent main loop: one goroutine per chat (`chatProcessLoop`)
- Run and Rewind share a per-session operation gate. A Run holds it for the
  whole turn; Rewind uses a non-blocking acquire and returns busy while the
  session is processing.
- Commands: serialized via message queue (non-concurrent commands)
- Tool calls: controlled by `maxConcurrency` (global semaphore) + read/write split
- LLM calls: per-tenant semaphore (`llm/semaphore.go`)
- Background tasks: goroutine + WaitGroup, drained on shutdown

## Append-only History and Rewind

`session_messages` is the canonical append-only history. Ordinary messages and
context controls share a monotonic `history_id`; controls include compression,
prune, context edit, AskUser question/answer, and mask records.

- `SessionService.Replay` builds the active LLM context from history. Versioned
  compress/prune snapshots are checkpoints, so replay starts at the newest valid
  checkpoint and interprets only its suffix. Legacy snapshots fall back to full
  replay.
- Public history is a display projection ordered by `history_id`: all raw
  messages and every compression marker are returned. Internal controls are not
  exposed. `compacted_by` describes the relationship but does not hide source
  messages.
- Rewind accepts only a persisted, non-display-only user `history_id`, deletes
  that node and its future, and restores the remaining prompt-token state in
  the same SQLite transaction before resetting pending/checkpoint state and
  emitting a `history_rewound` session event. Live progress caches and channel-qualified
  WS/SSE replay/pending buffers are cleared before the event is broadcast; a
  slow WebSocket that cannot accept the barrier is disconnected for replay.
- History ownership is resolved through the canonical channel/chat session. An
  Agent child session recursively validates every child and its real parent
  ownership. Runner-token CLI sessions use the token's canonical user and
  atomically claim or verify a new named CLI tenant; tokens are not global
  session credentials.
- WS replay sequence numbers are scoped to the explicit channel/chat route.
  Reconnect first restores that route and cursor; an evicted replay suffix emits
  `resync_required`, which makes TUI/Web reload history, active progress, TODOs,
  and pending AskUser state from authoritative APIs.
- History validation and control append operations are serialized per tenant
  and run on one SQLite `BEGIN IMMEDIATE` connection, so a second DB handle
  cannot rewind between validation and append. Compound writes such as AskUser
  tool pairs plus the pending-question control use one transaction as well.
- Interactive Agent history uses `channel="agent"` plus the full canonical
  session key. UI continuations call `continue_interactive_session`; they never
  enter the generic inbound Agent loop.

## Run State Components

The `runState` struct (`agent/engine_run.go`) orchestrates a single `Run()` execution. Three extracted components manage state that was previously scattered as inline fields:

### TokenTracker (`agent/token_tracker.go`)

Manages token accounting for a single Run. Compression decisions use only token
counts returned by the LLM API or restored from persisted state; there is no
local token estimate in this path.

- **RecordLLMCall(prompt, completion)** — Stores exact API usage after a successful LLM response.
- **ResetAfterCompress()** — Clears all token state before the compressed API count is recorded/restored.
- **GetPromptTokens()** — Returns the prompt count and its source (`api`, `restored`, or `no_data`). `no_data` never triggers compression.
- **SaveState(saveFn)** — Persists token state to DB for next Run restoration.

### CompressPipeline (`agent/compress_pipeline.go`)

Encapsulates the compress→persist→cleanup pipeline that was duplicated across `runCompression`, `handleInputTooLong`, and `context_window_exceeded`.

- **ApplyCompress(ctx, params)** → Executes: CM.Compress → accumulate exact API usage → sync messages → reset token state → append a compression snapshot → clean offload/mask stores.
- Returns `CompressPipelineResult{NewMessages, NewTokenCount, CompressOutput}`.

### PersistenceBridge (`agent/persist_bridge.go`)

Manages incremental session persistence. Replaces the inline `lastPersistedCount` field and scattered `session.AddMessage` calls.

- **IncrementalPersist(messages)** — Persists messages after the watermark. Skips system messages, strips `<system-reminder>` tags.
- **IncrementalPersistAndAskQuestion(messages, metadata)** — Atomically appends the AskUser tool exchange and pending-question control.
- **RewriteAfterCompress(sessionView, totalMsgCount)** — Appends a versioned compression snapshot without rewriting original history.
- **AppendPrune(sessionView, totalMsgCount)** — Appends a versioned aggressive-prune snapshot.
- **MarkAllPersisted(count)** — Updates watermark without writing (for bg task notifications).
- **ComputeEngineMessages(messages)** — Returns messages produced during this Run (for RunOutput.EngineMessages).
- **IsPersisted(idx)** — Checks if a message at index has been persisted (for observation masking in-place updates).

### Invariant Validation (`agent/runstate_invariant.go`)

Debug-mode state consistency checker, called at key transition points:

- **ValidateInvariants()** — Checks that the persistence watermark never exceeds the active message count and that token state is internally consistent.
- Called via `validateInvariantsAt(ctx, point)` at: post_llm_call, post_llm_call_input_too_long, post_compress, post_compress_window_exceeded, post_persist.

## AgentBackend

The `AgentBackend` interface (`agent/backend.go`) abstracts where the agent loop runs.
There is only one implementation: `Backend` (`agent/backend_impl.go`).

### Backend/Transport Architecture

**Backend is a pure typed RPC client.** Every method is 1-3 lines:
```go
func (b *Backend) GetSettings(ns, sid string) (map[string]string, error) {
    var r map[string]string
    return r, b.call(MethodGetSettings, getSettingsReq{Namespace: ns, SenderID: sid}, &r)
}
```

There is NO business logic branching — zero `if agent != nil` in RPC methods.
The `call()` / `callVoid()` helpers marshal the request, call `transport.Call()`,
and unmarshal the response. Backend never knows whether it's local or remote.

**Transport is the execution layer** (`agent/transport.go`):
- `localTransport` (`agent/local_transport.go`) — handler table dispatches to `*Agent` directly.
  Uses generic helpers `rpc0`/`rpc1`/`rpcVoid`/`rpcVoid0` to eliminate JSON boilerplate.
- `RemoteTransport` (`agent/transport_remote.go`) — WebSocket JSON-RPC to xbot server.

```
Backend.GetSettings(ns, sid)
  → b.call("get_settings", req, &result)
    → transport.Call("get_settings", payload)
      ├─ localTransport: handler table → agent.settingsSvc.GetSettings(...)
      └─ RemoteTransport: WS RPC → server handler → agent.settingsSvc.GetSettings(...)
```

**Request types** (`agent/req_types.go`) define typed structs + RPC method name constants
(`MethodXxx`) for compile-time safety.

**Adding a new RPC method** requires 3 lines of code:
1. Constant in `req_types.go`: `MethodFooBar = "foo_bar"`
2. Handler in `local_transport.go`: `h[MethodFooBar] = rpc1(func(r fooBarReq) (result, error) { ... })`
3. Method in `backend_impl.go`: `func (b *Backend) FooBar(...) { b.call(MethodFooBar, req, &result) }`

### Server / CLI Entry

Server entry can be launched from the root binary or via `xbot-cli serve`. Both paths call `serverapp/`.

The `serverapp/` package:
- `server.go` — `Run()` startup, channel registration, graceful shutdown
- `rpc.go` — generic RPC dispatch helpers (`rpc0`, `rpc1`, `rpc1void`, etc.)
- `rpc_table.go` — RPC method registry + auth helpers (`requireAdmin`, `ownOrAdmin`)
- `rpc_*.go` — handler groups by domain (settings, llm, subscription, session, tasks)
- `callbacks.go` — shared Runner/Registry/LLM callback builders
- `setting_handlers.go` — runtime setting registry for server-side effects

Adding a new CLI RPC: define a typed handler method on `rpcContext` in the appropriate `rpc_*.go`,
then register it with one line in `buildRPCTable()`. No switch-case to update.

### Remote Connection

CLI connects to server's web channel WebSocket endpoint with query params:
- `?client_type=cli&token=<runner_token>` — token-based auth
- Server validates token against `runner_tokens` table
- RemoteTransport uses the same WS protocol as web browser clients

## Per-Package Details

- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/subscription.md` — subscription system, LLM resolution, session isolation, all switch scenarios
- `docs/agent/tools.md` — built-in tools, hooks, sandbox types, AI-native config tools
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters, deterministic rendering
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming
- `docs/agent/worktree.md` — git worktree multi-agent workspace isolation
- `docs/agent/hooks.md` — hooks lifecycle, handler types, configuration
- `docs/agent/plugin.md` — plugin system, runtimes, RPC bridge
