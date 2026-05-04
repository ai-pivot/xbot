# Architecture

## Package Map

```
cmd/xbot-cli/     CLI entry point, app wiring, subscription management
cmd/runner/       Remote runner process (sandbox execution)
agent/            Agent loop, LLM orchestration, middleware pipeline, tool execution
channel/          Channel adapters: CLI (BubbleTea), Feishu, QQ, Web
llm/              LLM client abstraction (OpenAI, Anthropic), retry, streaming
memory/           Pluggable memory: letta/ (archival+core), flat/ (in-memory)
plugin/           Plugin system: extensible tools, hooks, context enrichers
tools/            Built-in tools, sandbox, hooks, MCP integration
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
| 5 | ProjectContextMiddleware | `05_project_context` | Load AGENT.md from CWD |
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
| `Sandbox` | `tools/sandbox.go` | Run, Sync, Resolve |
| `Channel` | `channel/channel.go` | Start, Stop, Send |
| `MessageMiddleware` | `agent/middleware.go` | Process(mc) |
| `MemoryProvider` | `memory/memory.go` | Core + Archival memory |
| `AgentBackend` | `agent/backend.go` | Abstract local/remote agent execution |

## Concurrency Model

- Agent main loop: one goroutine per chat (`chatProcessLoop`)
- Commands: serialized via message queue (non-concurrent commands)
- Tool calls: controlled by `maxConcurrency` (global semaphore) + read/write split
- LLM calls: per-tenant semaphore (`llm/semaphore.go`)
- Background tasks: goroutine + WaitGroup, drained on shutdown

## Run State Components

The `runState` struct (`agent/engine_run.go`) orchestrates a single `Run()` execution. Three extracted components manage state that was previously scattered as inline fields:

### TokenTracker (`agent/token_tracker.go`)

Manages token accounting for a single Run. Replaces scattered `lastPromptTokens`/`lastCompletionTokens`/`lastMsgCountAtLLMCall` fields.

- **RecordLLMCall(prompt, completion, msgCount)** — Called after each LLM API response. Stores exact token counts from the API.
- **ResetAfterCompress(newTokens, msgCount)** — Called after context compression. Resets to locally-estimated counts.
- **EstimateTotal(messages, model)** — Returns estimated total context size. Strategy varies by data source: API+completion+tool_delta, API+completion, restored-from-DB, or local-estimate-fallback.
- **DetectTruncation(messages, model)** — Detects if messages were truncated (Ctrl+K / rewind) since last LLM call. Re-estimates if so.
- **SaveState(saveFn)** — Persists token state to DB for next Run restoration.

### CompressPipeline (`agent/compress_pipeline.go`)

Encapsulates the compress→persist→cleanup pipeline that was duplicated across `runCompression`, `handleInputTooLong`, and `context_window_exceeded`.

- **ApplyCompress(ctx, params)** → Executes: CM.Compress → AccumulateUsage → SyncMessages → EstimateTokens → TokenTracker.ResetAfterCompress → Persistence.RewriteAfterCompress → CleanOffload/MaskStores.
- Returns `CompressPipelineResult{NewMessages, NewTokenCount, CompressOutput}`.

### PersistenceBridge (`agent/persist_bridge.go`)

Manages incremental session persistence. Replaces the inline `lastPersistedCount` field and scattered `session.AddMessage` calls.

- **IncrementalPersist(messages)** — Persists messages after the watermark. Skips system messages, strips `<system-reminder>` tags.
- **RewriteAfterCompress(sessionView, totalMsgCount)** — Clears session and re-adds compressed messages. Used after compression.
- **MarkAllPersisted(count)** — Updates watermark without writing (for bg task notifications).
- **ComputeEngineMessages(messages)** — Returns messages produced during this Run (for RunOutput.EngineMessages).
- **IsPersisted(idx)** — Checks if a message at index has been persisted (for observation masking in-place updates).

### Invariant Validation (`agent/runstate_invariant.go`)

Debug-mode state consistency checker, called at key transition points:

- **ValidateInvariants()** — Checks: (1) persistence watermark ≤ len(messages), (2) promptTokens > 0 iff hadLLMCall || restoredFromDB, (3) msgCountAtCall ≤ len(messages).
- Called via `validateInvariantsAt(ctx, point)` at: post_llm_call, post_llm_call_input_too_long, post_compress, post_compress_window_exceeded, post_persist.

## AgentBackend

The `AgentBackend` interface (`agent/backend.go`) abstracts where the agent loop runs:

- **LocalBackend** (`agent/backend_local.go`): In-process `agent.Agent`. CLI creates the agent,
  runs `agent.Run()`, and executes tools locally. This is the default mode (no `--server` flag / `serve` subcommand).
- **RemoteBackend** (`agent/backend_remote.go`): Connects to a remote xbot server via WebSocket.
  Agent loop and tool execution run server-side; CLI is a display/input layer.

### Backend/Transport Architecture

**Backend** (`agent/backend_impl.go`) is the single unified `AgentBackend` implementation. It operates in two modes:
- **Local**: `agent *Agent` is non-nil — calls Agent directly, zero JSON serialization
- **Remote**: `transport Transport` is non-nil — delegates to Transport for communication

All Backend methods use `dispatch[Req, Res]()` generic helpers that encapsulate the local/remote dispatch. Backend methods themselves contain zero `if agent != nil` branches. The `dispatch` generic is the single place where local-vs-remote branching occurs.

**Transport** (`agent/transport.go`) is a pure communication interface (~10 methods):
```go
type Transport interface {
    Start/Stop/Close()
    Call(method, payload) → response  // sole communication method
    SendMessage(msg) / Subscribe(chatID)
    OnOutbound/OnProgress/OnReconnect/OnConnStateChange/OnPluginWidgets
    ConnState/IsRemote/ServerURL
}
```

**RemoteTransport** (`agent/transport_remote.go`) implements Transport over WebSocket (extracted from the old RemoteBackend). Adding new transports (gRPC, MCP) only requires implementing ~10 methods.

**Request types** (`agent/req_types.go`) are typed structs (e.g. `getSettingsReq{Namespace, SenderID}`) used with `dispatch[Req, Res]()` for compile-time field checking — no loose `map[string]any`.

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
- `docs/agent/tools.md` — built-in tools, hooks, sandbox types
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming
- `docs/agent/gotchas.md` — cross-cutting pitfalls
