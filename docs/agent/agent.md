# agent/ — Agent Loop & Orchestration

## Core Files

| File | Purpose |
|------|---------|
| `agent.go` | Agent struct, lifecycle, HandleMessage, Run loop (~2366 lines) |
| `engine.go` | Engine interface, SubAgent progress, nested context |
| `engine_run.go` | runState struct, Run() loop, LLM/tool execution, compress/persist orchestration (~1561 lines) |
| `engine_wire.go` | Dependency injection: buildSubAgentRunConfig, HookChain/LLMFactory inheritance (~1282 lines) |
| `context.go` | MessageContext, PromptData, initPipelines() |
| `middleware.go` | MessagePipeline, MessageMiddleware interface |
| `middleware_builtin.go` | Built-in middleware implementations |
| `interactive.go` | Interactive SubAgent: multi-turn sessions, inspect/tail (~870 lines) |
| `compress.go` | Context compression via LLM (~600 lines) |
| `compress_pipeline.go` | CompressPipeline: unified compress→persist→cleanup flow |
| `token_tracker.go` | TokenTracker: token accounting per Run |
| `persist_bridge.go` | PersistenceBridge: incremental session persistence |
| `runstate_invariant.go` | ValidateInvariants: debug-mode state consistency checks |
| `reminder.go` | System reminder injection (<system-reminder>) |
| `skills.go` | SkillStore: directory scan, TTL cache, catalog generation |
| `agents.go` | AgentStore: subagent role discovery, catalog generation |
| `llm_factory.go` | LLMFactory: user custom LLM creation/caching |
| `registry.go` | RegistryManager: skill/agent publishing, installation |
| `settings.go` | SettingsService: channel/user level settings |

## Pipeline Registration

Middleware registered in `agent/context.go:initPipelines()`.
Execution order defined by Priority field (see `architecture.md` for full table).

## SubAgent Architecture

SubAgents bypass the pipeline. System prompt built in `buildSubAgentRunConfig` (`engine_wire.go`).
Inherits parent's: HookChain (same pointer), LLMFactory, skill catalog, tool context extras.

Max nesting depth: 6. Three levels: main → SubAgent → SubSubAgent.

## Interactive SubAgent Architecture

Interactive SubAgents maintain persistent multi-turn sessions via `InteractiveAgent` structs stored in `Agent.interactiveSubAgents` (sync.Map). Key flows:

- **Spawn**: `SpawnInteractiveSession` → creates session, eager-saves user message + final assistant reply to `ia.messages` + DB
- **Send**: `SendToInteractiveSession` → eager-saves user message, runs agent loop, eager-saves final assistant reply
- **Inspect/Tail**: read-only access to `ia.messages`
- **Unload**: `destroyInteractiveSession` → saves memory, cleans DB, removes from map

### Interactive Session Key Format

`interactiveKey = channel:chatID/roleName:instance`

### Remote Mode Persistence

In remote mode, SubAgent messages must be eagerly persisted (not deferred):
- User messages saved immediately in `SpawnInteractiveSession`/`SendToInteractiveSession` before `Run()`
- Final assistant reply appended to `ia.messages` and persisted after `Run()` completes
- `GetOrCreateSession` may return stale tenant after server restart — call `Clear()` to reset
- Placeholder sessions must include user message (not just system prompt)

### OutboundMessage Routing

SubAgent outbound messages go to **parent's channel/chatID** (never the agent session view). The CLI detects these and routes accordingly.

## Interactive SubAgent Pitfalls

- **Never hold `ia.mu` while calling Run()** — deadlock via nested SpawnInteractiveSession → cleanupExpiredSessions
- SubAgent errors invisible as Go error — must embed in Content
- Progress tree corruption from stale closures — rebuild ProgressNotifier from current ctx
- **`handleFinalResponse` must set `ThinkingContent`** on the prompt data — otherwise PhaseDone assistant synthesis has empty content
- **Stream content updates must snapshot** — `StreamContentFunc`/`ReasoningStreamContentFunc` must update `lastProgressSnapshot` for CLI to render

## Interactive SubAgent Lifecycle: "Never Outlive Creator"

All SubAgents must be cleaned up when their **direct creator** finishes or is cancelled. This requires both context cancellation AND `cancelChildSessions` in every completion path.

### Context Chain (the enforcement mechanism)

Every interactive SubAgent wraps its own `runCtx` with `bgSessionCtxKey` + `bgParentKey` markers:

- **bg mode** (`SpawnInteractiveSession`): `runCtx` derives from parent's ctx (nested) or `a.agentCtx` (first-level). Always marked.
- **foreground mode** (`SpawnInteractiveSession`): `fgRunCtx` wraps `subCtx` with markers. `fgRunCancel()` called after `Run()` returns.
- **send path** (`SendToInteractiveSession`): `runCtx` derived from `asyncBase` + markers + own key as `bgParentKey`.

When a child SubAgent spawns during a parent's `Run()`, the child detects `bgSessionCtxKey` on the parent's runCtx → derives its own context from the parent's → parent's `runCancel()` cancels the child.

### cancelChildSessions (the cleanup mechanism)

Every completion path calls `cancelChildSessions(key)` to remove child entries from `interactiveSubAgents`:

- **bg natural completion**: `runCancel()` at L776 cancels children via context → children self-clean via cancelled path (L883 `cancelChildSessions` + L884 `destroyInteractiveSession`). Parent does NOT call `cancelChildSessions` (session preserved for future send).
- **bg cancelled (unload/shutdown)**: L883 `cancelChildSessions(key)` + `destroyInteractiveSession(key)` — cascading.
- **foreground natural/error completion**: `fgRunCancel()` + `cancelChildSessions(key)` after `Run()` returns.
- **send natural completion**: `runCancel()` + `cancelChildSessions(key)` after `Run()` returns.
- **send cancelled (unload/shutdown)**: `cancelChildSessions(key)` in cancelled path.

### parentKey propagation

- `placeholder.parentKey` set from `ctx.Value(bgParentKey{})` at spawn time (L482-484).
- When foreground placeholder is replaced with full `ia` on completion, `parentKey` and `groupID` are preserved.
- `cancelChildSessions(parentKey)` matches children by their `parentKey` field.

## Pending Message Delivery (Running SubAgent)

When a SubAgent is running (`ia.running=true`), `action=send` no longer rejects the message. Instead:
1. Message queued in `ia.pendingMessages` with a `replyCh chan error`
2. SubAgent's `DrainBgNotifications` callback (set via `wirePendingMessageDrain`) drains pending messages between iterations
3. Each message becomes a `QueuedUserMessage` notification, injected as a synthetic tool result by `injectQueuedUserMessage`
4. SubAgent sees the message as a `delivered_message` tool result with explicit "已送达确认" content
5. `ReplyFn` signals the sender via `replyCh`, unblocking the caller

All 4 `Run()` call sites in `interactive.go` set `cfg.DrainBgNotifications = ia.wirePendingMessageDrain(key)`.

## Context Management

- `Pipeline.Assemble()` safely deduplicates system messages (used to panic) (`middleware.go:170`)
- Cd tool: must update both `tc.CurrentDir` and `cfg.InitialCWD` (`engine_test.go:1514`)
- Dynamic context injection detects CWD changes via `dynamic_context.go`

## Observation Masking

Long tool results auto-masked with `masked:mk_xxxx` placeholders.
Use `recall_masked` tool to retrieve. Configurable thresholds in `observation_masking.go`.
