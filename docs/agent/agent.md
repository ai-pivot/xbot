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

## Background Notification Cancellation

Agent-level bg notifications are queued by session (`map[sessionKey][]BgNotification`), not in one shared slice. `wireBgNotificationDrain` and `drainAndProcessNotifications` only take the current session's bucket, so other sessions are never scanned/re-appended during a drain.

When Ctrl+C cancels a turn, `handleCancelledRun` does not start a fresh bg-notification turn. It takes same-session pending notifications and records them into the interrupted turn as synthetic assistant tool-call + tool-result pairs, then appends a `user_cancelled` synthetic tool observation. This preserves completed bg work and records the user interruption in context without waking a new turn after the cancel ack. Notifications already drained into the Run are already persisted by the normal synthetic-tool path; cancel clears only their `drainedThisRun` tracking metadata.

## Turn Identity & Restart Resume (turn reuse + iteration continuation)

A restart-interrupted turn (graceful shutdown → `collectPendingResumes` → `resumePendingTurns` on boot, or manual `/continue`) is resumed via `InjectInboundResume`: an EMPTY message with `resume_turn` metadata (the original user message is already in DB; Assemble skips appending a user message). The resumed Run must continue the interrupted turn's identity — two invariants:

1. **Turn id reuse.** `admitToMsgCh` does NOT pre-allocate a turn id for `resume_turn` messages (same path as `ask_user_answered`). `chatProcessLoop` resolves it at dequeue time via `resolveResumeTurnID`: the turn of the last non-display-only user message (`SessionService.GetLastUserTurnID`), falling back to `nextTurnID()` when unresolvable (no user message / legacy rows without turn_id). Every restart previously allocated a FRESH turn id — splitting one logical turn into user(N)/resume(N+1)/resume(N+2) — the frontend rendered each as a separate assistant block ("重启后 turn 产生两个大 dom" bug). The TURN_ID monotonicity check and `lastTurnID` baseline skip resume turns (the reused id comes from DB, not the counter — it can legitimately be below the baseline when e.g. a notification turn ran during the interruption).
2. **Iteration number continuation.** `buildMainRunConfig` sets `RunConfig.IterationStart` = `GetMaxIterationForTurn(tenantID, turnID)` for resume turns; the engine's Run loop runs `i := n + s.iterStart` so the resumed Run's first iteration is K+1 (K = the interrupted Run's persisted max). Iteration numbers are turn-scoped: iteration_history is keyed by (turn_id, iteration), and the frontend's committed-turn shadow-lift upgrades a committed turn back to live only when `ev.iter > maxIter` — restarting at 1 would collide in DB and be dropped as a "replay".

Frontend contract (zero changes needed — already supported): after reload the interrupted turn is a `committed` turn (from `history_replaced`); `turn_started(sameTurnID, trigger='resume')` keeps it committed; the resumed Run's first iteration/stream event (turn-scoped id, iteration K+1) triggers the committed shadow-lift (`reduce.ts` iteration/stream cases: `ev.iter > maxIter` / `hasStreamEvidence` upgrades to live preserving iterations); `text_final` unions the live iterations with the committed history → ONE assistant block with iterations 1..K+n — identical to an uninterrupted turn.

`emitTurnStarted` already skips the per-session `iterationHistories` delete for resume ("Skip for resume — it continues the same turn" — now literally true with the reuse). `HasAssistantReplyAfterLastUser` (pending_resume.go) gates the resume itself: a turn whose last user message has a final (non-tool-calls) assistant reply is complete and is not resumed.

Tests: `agent/resume_turn_allocation_test.go` (admitToMsgCh skip + resolveResumeTurnID), `agent/engine_resume_iteration_test.go` (IterationStart numbering), `storage/sqlite/session_resume_turn_test.go` (GetLastUserTurnID + GetMaxIterationForTurn), `channel/iteration_history_test.go` `TestConvert_WithIterations_ResumedTurnReusesTurnID` (single assistant block), `web/src/chat/reduce.test.ts`「重启 resume 复用同 turn」(frontend contract).

## Context Management

- `Pipeline.Assemble()` safely deduplicates system messages (used to panic) (`middleware.go:170`)
- Cd tool: must update both `tc.CurrentDir` and `cfg.InitialCWD` (`engine_test.go:1429`, `TestBuildToolContext_SubAgentCdPersists`)
- Dynamic context injection detects CWD changes via `dynamic_context.go`

## Observation Masking

Long tool results auto-masked with `masked:mk_xxxx` placeholders.
Use `recall_masked` tool to retrieve. Configurable thresholds in `observation_masking.go`.
