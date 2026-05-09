# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, Backend/Transport architecture, key interfaces, concurrency
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/tools.md` — built-in tools, hooks system (agent/hooks/), sandbox types
- `docs/agent/hooks.md` — hooks lifecycle events, handler types, configuration, gotchas
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build
- `docs/agent/plugin.md` — plugin system architecture, runtimes, integration
- `docs/agent/worktree.md` — git worktree-based multi-agent workspace isolation, WorktreeRegistry, AutoDetectAndInit, peer discovery, path security

## Gotchas — MUST READ Before Any Code Change

### Concurrency
- **Never `defer` semaphore release inside a loop.** Deadlock when iterations exceed capacity. Release immediately after Generate completes.
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown.
- **User-scoped semaphores must not be hardcoded to capacity 1 when one sender can own multiple independent chats/sessions (for example remote CLI windows authenticated as `admin`).** Size them from configured concurrency or key them by session, otherwise different windows will block each other and look like a leaked semaphore.
- **`SetMaxConcurrency` must clear `userSemaphores` cache.** The global semaphore is rebuilt with the new capacity, but `getUserSemaphore` caches per-user channels in a `sync.Map` via `LoadOrStore`. Without `Clear()`, users with custom LLM keep using the cached semaphore with the OLD capacity forever. Symptom: setting max_concurrency to 100 has no visible effect.

### Subscription & Model Resolution
- **`user_llm_subscriptions` DB is the single source of truth for ALL LLM config** (provider, model, base_url, api_key, max_output_tokens, thinking_mode). These keys are subscription-scoped — they must NOT appear in `settingHandlerRegistry`, `CLIRuntimeSettingKeys`, or `user_settings` table. Adding them back would cause startup `applyRuntimeSettings` to overwrite DB with stale values (e.g. name→provider, max_output_tokens→8192).
- **CLI subscriptions are in config.json, server subscriptions are in DB (`user_llm_subscriptions`).** `GetLLMForModel` must check both — `configSubsFn` (CLI) and `subscriptionSvc` (DB).
- **`UpdateCachedModels(subID)` nil-derefs if subID not in DB.** Always nil-check `sub` after `Get()`. Config subs have IDs not in DB.
- **`OnModelsLoaded` callback runs in `NewOpenAILLM`'s async goroutine** — must be concurrency-safe.
- **Tier fallback**: unconfigured tier → vanguard→balance→swift chain. Empty tier must NOT return default client with wrong model.
- **`createClientFromSub` uses sub's credentials with a *different* model** — verify target model is served by that endpoint.

### Context Management & Token Estimation
- **`maybeCompress` uses ONLY API-returned `prompt_tokens` via `TokenTracker.GetPromptTokens()`.** No local estimation (tiktoken/CountMessagesTokens) in the compression decision path. The `no_data` source (no API data yet) skips all compress/masking checks. Tests must set `cfg.LastPromptTokens` to simulate a previous Run.
- **`ResetAfterCompress()` takes no arguments — it zeros ALL tracker fields** (promptTokens, completionTokens, hadLLMCall). Any non-zero value causes `maybeCompress` to re-trigger immediately, creating infinite compression loops. The tracker returns "no_data" until the next real LLM API call.
- **`RecordLLMCall(prompt, completion)` takes 2 args only.** No msgCount — the old delta estimation via `CountMessagesTokens` has been removed. Offload handles large tool results; a single iteration cannot add enough tokens to justify local estimation.
- **`buildToolContextExtras` uses `TenantSession.MemoryService()` for `MemorySvc`/`TenantID`, NOT `LettaMemory` type assertion.** These are tenant-level fields that work for all memory providers. Only LettaMemory-specific fields (CoreMemory, ArchivalMemory, ToolIndexer) stay inside the type assertion.
- **`ObservationMaskStore` and `OffloadStore` both persist to disk.** Mask uses `~/.xbot/mask/{tenantID}/{id}.json`, Offload uses `~/.xbot/offload_store/{session}/{id}.json`. `Recall` falls back to disk on memory miss. Both cleaned on compress and `/clear`.
- **`PersistenceBridge` manages the persistence watermark (`lastPersistedCount`), not inline fields.** All compression paths use `ApplyCompress` pipeline which calls `PersistenceBridge.RewriteAfterCompress()` to atomically clear+rewrite+update watermark. The invariant `LastPersistedCount <= len(messages)` is verified by `ValidateInvariants()` at debug level after every LLM call, compression, and persistence operation.
- **Shell binary output silently bypasses offload: `summarizeShell` splits by `\n` and keeps last 5 lines.** Binary data (e.g. `cat libmujoco.so`) has very few newlines, so "last 5 lines" ≈ entire content. The offload summary then contains nearly all original binary data, causing context explosion (39k→2.2M tokens observed). `summarizeShell` now applies `maxLineRunes=500` truncation (same as `summarizeRead`) on output lines. But any new summary generator must handle the few-lines-but-megabytes-per-line case.
- **`SanitizeMessages()` is the final validation gate before sending to LLM.** Five passes: (1) strip assistant messages with empty content AND no tool_calls — rejected by OpenAI/Anthropic; (2) strip tool_calls with invalid/malformed JSON arguments — caused by cancelled stream with partially-generated tool arguments, DeepSeek rejects with 400; (3) strip trailing unpaired tool_calls from cancelled runs; (4) ensure every tool_call_id has a matching tool response message — strip orphaned tool_calls, re-run Pass 1 to clean up emptied assistant messages; (5) strip orphaned tool messages whose tool_call_id doesn't appear in any assistant's tool_calls — Pass 2 and Pass 4 can strip tool_calls leaving orphaned tool messages, DeepSeek rejects with "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'". Called in `buildPrompt` and `engine.go`.
- **`CollectStreamWithCallback` must return partial content on `ctx.Done()`, not nil.** When user cancels streaming mid-reasoning, returning nil loses accumulated `reasoning_content` and partial `tool_calls`. The partial tool_calls may have broken JSON arguments, which then pass through `SanitizeMessages` Pass 2 for cleanup. Returning nil also prevents the engine from properly recording the cancellation state.

### Startup
- `NewOpenAILLM` loads model list asynchronously. `ListModels()` returns fallback immediately.
- Settings save is synchronous — all local I/O, no network calls.
- **`SaveToFile` uses deep JSON merge to preserve unknown fields.** `json.Unmarshal` silently drops fields not in the Go struct. `SaveToFile` reads the existing disk file first and recursively merges struct JSON into it, so user-added custom fields (or future struct fields added in newer versions) survive load→save cycles. Never bypass `SaveToFile` with raw `json.Marshal` writes to config.json.

### CLI / BubbleTea
- **`parseKeyInput` with modifiers must NOT set `Text` field.** `Key.String()` returns `Text` if non-empty (ultraviolet `key.go:392`), bypassing `Keystroke()`. `{Code:'c', Text:"c", Mod:ModCtrl}.String()` → `"c"` not `"ctrl+c"`, breaking cancel.
- **Iteration snapshot deduplication**: PhaseDone + handleAgentMessage can both snapshot the same iteration. Always dedup by iteration number, preferring PhaseDone (has reasoning from server).
- **`ElapsedWall` must be set in ALL snapshot paths** — missing it causes total time to fall back to summing only last iteration's tool.Elapsed.
- **SubAgent remote mode: `convertWsProgressToCLI` must copy StreamContent/ReasoningStreamContent** from WsProgressPayload to CLIProgressPayload — otherwise stream content never reaches CLI.
- **`convertWsProgressToCLI` must copy `Iteration` field for ActiveTools/CompletedTools.** Missing it causes all tools to have Iteration=0, filtering them out of the progress panel and making the CLI misinterpret the turn as ended.
- **Cancel key must be `channel:chatID` only (no senderID).** Background task notifications inject messages with senderID="user", while CLI users have different senderIDs. Including senderID in the cancel key makes cancel impossible for bg-task-triggered turns.
- **SubAgent remote mode: tick chain breakage** — `tickCmd()` injection must be unconditional (not gated on `!m.fastTickActive`) in splashDoneMsg, PhaseDone return, and history reload paths. Conditional injection causes chain to break during session switches.
- **SubAgent session view: viewport freeze on return** — when main session's turn ended while viewing agent session, PhaseDone is detected on return but assistant reply is missing. Tick handler with `busy=false` must check `!m.renderCacheValid` as fallback.
- **SubAgent CWD inheritance**: `parent_cwd` metadata must fallback to `parentCtx.WorkingDir` when `parentCtx.CurrentDir` is empty (parent never Cd'd). `buildParentToolContext` must also fallback to `workspaceRoot`. Otherwise SubAgent starts in `a.workDir` (config value) instead of the parent's actual working directory.
- **`wireSubAgentCLIProgress` must be called for ALL sessions (foreground AND background).** Background sessions gated by `!background` have no live progress when viewed via Ctrl+T panel. ChatID-based filtering in `handleProgressMsg` ensures events route to the correct session.
- **CreateChat tool must set `background=true` in metadata** before `SpawnInteractive`. Without it, CreateChat blocks the parent agent's turn until the SubAgent finishes.
- **Progress panel cursor overflow**: when typewriter cursor `▋` would overflow the line width, render it on a separate line with placeholder (guide-only when hidden) to prevent height jumping during blink.
- **Progress panel tool lines**: use `toolLine()` helper (lipgloss.Width-based) instead of `len()` for width calculation — byte length ≠ visual width for styled/unicode content.
- **SubAgent tree description**: skip description when `descW <= 0` instead of forcing `descW >= 10` minimum — the old minimum caused overflow on narrow terminals.
- **Group chat members must be pre-spawned**: `CreateChat(type="group")` must auto-spawn each member agent and register AgentChannel in Dispatcher. Otherwise `@mentions` in SendMessage fail with "unknown channel: agent:role/instance".
- **Panel navigation stack (push/pop)**: `panelStack []panelStackEntry` stores parent panel state for nested navigation. `pushPanel()` saves state, `popPanel()` restores it. `pushPanelFromPalette()` marks `fromPalette=true` so ESC reopens the palette. Only the **caller** (e.g. Settings entering Runner) should push — `openXxxPanel()` never pushes itself. `closePanel()` clears the entire stack.
- **Settings inline overlay (Crush-style)**: edit/combo overlay renders inline right below the cursor item, NOT at the end of the list. `trackSettingsZones` must account for inline overlay lines. `ensureSettingsCursorVisible(extraLines)` adjusts `panelScrollY` so the overlay stays visible.
- **Settings click handlers must `commitPanelEdit()` first**: clicking a different item while in edit/combo mode must save the current edit value and close the overlay before activating the new item. Otherwise stale overlay state causes rendering bugs.
- **Panel mouse wheel uses `isYInPanelBox(y)`, not zone matching**: zone-based scroll detection left gaps (category headers, blank lines). Y-range check covers the entire panel box area with no dead zones.
- **Palette tab zone is a single line**: `trackPaletteZones` tracks tabs as ONE zone, not `tabCount` zones. The old code tracked one zone per tab, causing Y offset = `tabCount - 1` for all items below.

### CLI Deterministic Rendering
- **Every assistant/tool_summary message is keyed by `agentTurnID` + `role`.** `upsertMessageByTurn(turnID, role, msg)` finds existing entries and updates in-place instead of appending duplicates. This prevents duplicate messages when PhaseDone and cliOutboundMsg arrive out of order.
- **`turnDoneFlags` tracks per-turn completion state**: `doneProcessed` (PhaseDone created tool_summary) and `replyReceived` (handleAgentMessage appended assistant reply). `handleAgentMessage` checks `doneProcessed` to skip redundant tool_summary creation.
- **Queue flush requires `replyReceived` or `doneProcessed+turnCancelled`.** The old heuristic (`!typing` on next tick) could flush before the assistant reply arrived. Now the tick handler waits for `replyReceived=true` on the completed turn. A 2s timeout fallback prevents permanent queue stalls when replies are lost.
- **`pendingToolSummary` is no longer used for PhaseDone→handleAgentMessage handoff.** The upsert-by-turn mechanism replaces it — `handleProgressDone` creates the tool_summary via upsert, and `handleAgentMessage` either finds it already there (doneProcessed) or creates it from local iteration history.

### SubAgent Progress Identity
- **All SubAgent progress structs MUST carry an `Instance` field** (`CLISubAgent`, `WsSubAgent`, `SubAgentNode`, `childAgentStatus`). `mergeSubAgentTrees` uses `Role + ":" + Instance` as the unique key — without Instance, same-role different-instance SubAgents collapse into a single tree node.
- **`RunSubAgent` interface must include `instance` parameter.** One-shot SubAgent goes through `spawnAgentAdapter.RunSubAgent` → `buildMsg` → metadata. Without this, instance never reaches `SubAgentProgressDetail` and the progress tree can't distinguish parallel SubAgents.
- **`isPlausibleAgentRole` must strip `[instance]` suffix before the space check.** The formatted line `"> 🔄 explore [mem-1]: desc"` has `"explore [mem-1]"` as the role candidate, which contains a space and would be rejected. Strip `[instance]` before `strings.Contains(name, " ")` — otherwise ALL SubAgent progress lines with instance are filtered out by `isStatusEmojiLine`.
- **`parseSubAgentLine` no-colon completion path must also extract instance.** For `"✅ explore [mem-1]"` (legacy format), instance extraction must happen before the early return, or the Instance field stays empty.

### Reasoning Contamination
- **`snapshotIterationChange` and `handleProgressDone` must NOT use `prev.ReasoningStreamContent` as reasoning fallback.** Stream-only messages (`StreamReasoningFunc`) update `m.progress.ReasoningStreamContent` directly between structured progress updates. Since `prev` is captured at the START of `handleProgressMsg` (pointer to `m.progress`), `prev.ReasoningStreamContent` can contain the NEXT iteration's reasoning when the iteration changes. Use `prev.Reasoning` (server's `ReasoningContent`, set at LLM completion) or `m.lastReasoning` instead.
- **Reasoning stream without iteration advance contaminates previous snapshot.** `isStreamOnly` reasoning updates bypass `snapshotIterationChange`. When the next structured progress arrives with a new iteration, the old progress (with accumulated reasoning) gets snapshotted under the OLD iteration. Fix: `advanceIterationForReasoning()` detects when reasoning arrives for a completed iteration and advances `m.progress.Iteration`. Must also call after `restoreIterationHistory` + `carryForwardProgressState` for the TUI restart case.
- **`handleAgentMessage` must NOT use `m.progress.ReasoningStreamContent` as `m.lastReasoning`/`m.reasoningByIter` fallback.** When the agent's text reply arrives after a structured progress has advanced `m.progress.Iteration` (e.g. #3→#4), `ReasoningStreamContent` may still contain the previous iteration's reasoning content. This causes misattribution: the previous iteration's reasoning gets stored under `m.reasoningByIter[newIter]`, and the next iteration's snapshot will show the wrong reasoning text (symptom: "#4 shows #3's opening content"). Only use `m.progress.Reasoning` (the authoritative per-iteration field, set at LLM completion).

### CLI Rendering Panics
- **All render bodies (`renderGlobBody`, `renderShellBody`, `renderReadBody`, etc.) must use `ansi.Truncate`, NEVER manual `runes[:maxW-N]`.** On narrow viewports, `maxW-N` goes negative causing `slice bounds out of range [:-1]` panic. Glob and Shell both had this pattern — fix one, grep for the other. `ansi.Truncate` handles negative/zero widths safely.
- **`regexp.MustCompile` must be package-level `var`, never inside function bodies.** Chroma-powered render functions (`renderReadBody`, `renderDiffStyled`) are called per-frame and re-compiling regexps on every call wastes CPU. All other channel files (feishu.go, mermaid.go, qq.go) already use package-level vars — follow the convention.
- **`currentTheme` writes must be guarded when called from non-BubbleTea goroutines.** `ApplyTheme()` is exported and can be called from plugin/settings handlers. `setTheme()` now holds `currentThemeMu` — any new write path to `currentTheme` must also acquire this mutex.

### TUI Control & Config Tools (AI-Native)
- **`SyncLayoutSettings` must ONLY use `asyncCh`, never call model methods directly.** `refreshRemoteValuesCache` runs in a background goroutine. Direct calls to `c.model.applyLayoutConfig()` or `c.model.relayoutViewport()` race with the BubbleTea event loop, causing glamour table render panics (`index out of range` in lipgloss table resize). ALWAYS route through `asyncCh` → `cliSettingsSavedMsg` → `handleSettingsSavedMsg`.
- **`relayoutViewport` → `newGlamourRenderer(cw - 4)` must only be called from the event loop goroutine.** The glamour renderer is NOT goroutine-safe. Background goroutine calls cause concurrent access to `m.renderer` and crash.
- **`sidebar_width` and other layout keys are NOT in `config.Config` struct.** `SaveToFile` deep merge only PRESERVES unknown keys from disk, never writes new ones. Use `saveLayoutToConfig()` (raw JSON read-modify-write) to persist these keys to `config.json`.
- **Remote `tui_control` flow**: Server → `tui_control_req` WS message → client `readPump` → `tuiControlReqCb` → `SendTUIControl` → `asyncCh` → `handleAsyncDrain` → `program.Send` → event loop → `handleSessionControlMsg`. The WS callback in `transport_remote.go` MUST be wrapped in `go func()` to keep `readPump` responsive — otherwise RPC calls within handlers (e.g. `SessionsDeleteFn`) deadlock waiting for responses.
- **`config` tool uses `SettingsSvc` auto-injection, not Agent callbacks.** `buildToolContext` injects `ConfigGet`/`ConfigSet` from `cfg.SettingsSvc`. This works in ALL modes (local + remote via RPC). Do NOT rely on Agent `SetTUICallbacks` for config.
- **`config` tool masks sensitive keys on read** (`api_key` → `sk-a***`, `runner_token` → first 4 chars + `***`). Writes are NOT blocked — users can type API keys anyway, our responsibility ends at masking.
- **`DeleteTenant`/other `TenantService` methods must nil-guard `s.db`.** Interactive session cleanup races with DB initialization — `destroyInteractiveSession` can fire before the DB is connected, causing nil pointer dereference.

### Hooks System
- **Old `ToolHook`/`HookChain` is gone.** Replaced by `agent/hooks/Manager`. Any code referencing `HookChain`, `ToolHook`, `executeWithHooks` is stale.
- **Manager.Emit() is shared across Agent + SubAgents** (same instance). Must be concurrency-safe.
- **Decision priority**: `deny > defer > ask > allow`. Low-priority layer deny cannot be overridden by high-priority allow.

### Backend/Transport Architecture
- **Backend is a pure typed RPC client** (agent/backend_impl.go). Every method is 1-3 lines calling `b.call(method, req, &result)` or `b.callVoid(method, req)`. There is NO business logic branching — zero `if agent != nil` in RPC methods.
- **Transport is the execution layer** (agent/transport.go). `localTransport` (agent/local_transport.go) has a handler table that executes business logic directly on `*Agent`. `RemoteTransport` (agent/transport_remote.go) sends JSON-RPC over WebSocket. Backend never knows which one it's using.
- **Handler table uses generic helpers**: `rpc0`, `rpc1`, `rpcVoid`, `rpcVoid0` eliminate JSON marshal/unmarshal boilerplate. Adding a new RPC method = 1 constant in req_types.go + 1 handler in local_transport.go + 1 method in backend_impl.go.
- **Request types** (agent/req_types.go) define typed structs + RPC method name constants (`MethodXxx`) for compile-time safety. Use them instead of bare strings.
- **Adding new transports** (gRPC, MCP) only requires implementing ~12 Transport interface methods.

### Channel Configuration
- **TUI channel config changes require live channel restart.** Writing config.json is not enough — Feishu/Web/QQ/NapCat channels are created once at startup via `registerChannels()`. `SetChannelConfig()` now calls `reconfigureFn` (set by server.go) to start/stop the affected channel. Any new channel type must be added to both `channelShouldRun()` and `createChannelInstance()`.
- **Key naming must be consistent across all channels.** Web used `enable` while Feishu/QQ/NapCat used `enabled` — the RPC handler only checked `enabled`, so web changes were silently ignored. All channels now use `enabled` (with backward compat for `enable`).
- **New backend methods must be added to `AgentBackend` interface, not `LocalBackend` only.** `SetChannelReconfigureFn` was originally a `*LocalBackend`-only method, requiring a type assertion in server.go. This breaks the abstraction — `AgentBackend` is the contract. Both `LocalBackend` and `RemoteBackend` must implement any functional method. RemoteBackend gets a no-op when the server handles the behavior via RPC.
- **Command hooks disabled by default** — requires `enable_command_hooks: true` in config.
- **Max 10 handlers per event**, total timeout 60s. Excess silently truncated with warning log.

### Plugin System
- **Plugin system is opt-in** — only activates when `plugins.enabled: true` in config.json. No plugin loading happens without explicit user consent.
- **`pm.workDir` is `atomic.Value` (not `string`).** `activate()` may be called while `pm.mu` write lock is held — reading workDir must be lock-free. Never change it back to `string` or `activate`/`InstallPlugin` will deadlock.
- **`runAndUpdate()` does NOT write global slot cache.** It calls `NotifyUpdated()` instead of `UpdateWidget()`. Writing global cache causes cross-session overwrites (session B's git branch overwrites session A's).
- **CLI WS clients must NOT auto-subscribe to senderID ("admin").** `client_type=cli` connections skip p2p subscribe. Subscribing CLI to "admin" causes `PushPluginWidgetsPerSession` to send stale content to wrong windows.
- **`PushPluginWidgetsPerSession` skips non-path chatIDs.** Only chatIDs starting with `/` are session chatIDs. Virtual chatIDs like "admin" or "web-123" are not rendered.
- **`OnPluginWidgets` callback filters by chatID.** Client-side rejects pushes for other sessions. Double protection against cross-session widget corruption.
- **Script plugin outputs map is per-workDir.** `RenderForWorkDir(width, workDir)` reads `outputs[workDir]`. `Render()` falls back to shared `pctx.WorkingDir()` — never use for remote multi-session.
- **`HookPayload.ToolOutput` is truncated to 8KB.** Don't rely on it for full file content. Plugins needing full output should use dedicated tool result channels.
- **PluginManager.ActivateAll() collects capabilities; WireAll() connects them.** Never call registerCapabilities manually — WireAll is the single integration point.
- **PluginEntry.stateMu protects state transitions.** Use CAS pattern (check state → set activating → set active/error) to prevent concurrent activation races.
- **gRPC plugin processes are killed on timeout/cancellation.** The `call()` method kills the process and marks it as not-running to prevent goroutine leaks from blocked stdout reads.
- **PluginToolBridge auto-detects PluginToolV2.** If a plugin tool implements V2, the bridge passes ToolCallContext. Otherwise falls back to V1 Execute(ctx, input).
- **Plugin IDs validated with regex `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`.** This prevents path traversal, null bytes, and injection attacks in storage paths.
- **Storage files use 0600 permissions and atomic write (tmp+rename).** Never use 0644 for plugin storage.
- **WASM runtime is skeleton-only.** It compiles and loads but Activate() is a no-op. Phase 2 requires wazero dependency.
- **PluginContext provides 4 type-safe Storage helpers:** `StorageInt`, `StorageBool`, `StorageJSON`, `StorageGetJSON`. These wrap the base `StorageAccessor` with parse/unmarshal and return typed results. Failed parses return zero-value + false (not errors) for Int/Bool, and errors for GetJSON.
- **Auto-retry runs in a background goroutine.** `SetAutoRetry(true, maxRetries)` starts `retryLoop` with exponential backoff (1s→30s cap). **`DeactivateAll()` cancels the retry context** — if you call `activate()` manually after `DeactivateAll()`, you must re-enable auto-retry or failed plugins won't recover automatically.
- **Manifest `timeout` field accepts Go duration strings** (`"30s"`, `"1m"`, `"500ms"`), parsed via `time.ParseDuration`. Empty or missing defaults to `DefaultPluginTimeout` (30s). Max allowed: 5 minutes.
- **EventBus requires `bus.plugin` permission** in addition to `bus.read`/`bus.write`. Subscribe needs `bus.plugin` + `bus.read`; Publish needs `bus.plugin` + `bus.write`. This separates plugin-to-plugin events from the core message bus.
- **`InstallPlugin` uses `filepath.EvalSymlinks`** to resolve the real directory path before deletion check, preventing symlink-based path traversal attacks. Only directories under `xbotHome` are deleted.
- **`WatchConfig` polls config.json every 30 seconds** (configurable, min 5s). It compares `plugins.disabled_plugins` lists via diff and reactively deactivates newly disabled / activates newly enabled plugins. Returns a stop channel for shutdown.
- **DependencyResolver uses Kahn's algorithm (BFS topological sort).** Circular dependencies return an error (not panic). `AddManifest` with duplicate ID replaces the existing entry. Resolve() returns activation order — plugins with no dependencies first, then in dependency order.
- **Profiler is safe for concurrent use** (sync.Mutex). `Profile(pluginID)` returns a **copy** of PluginProfile — safe to mutate without affecting internal state. Unprofiled plugins return zero-value PluginProfile.
- **ExportConfig acquires RLock on PluginManager.** Must not be called while holding a write lock (e.g., inside custom Activate/Deactivate that calls pm.mu.Lock). ImportConfig acquires write lock internally — do not nest inside another write-locked operation.
- **MockPlugin/MockTool chain API returns the same pointer** — each `With*` call mutates and returns `*MockPlugin`/`*MockTool`. Do not share a single mock across parallel tests without cloning.
- **PluginRegistry MVP only supports local sources** for installation. Search operates on locally installed plugins only. GitHub/URL sources are defined but InstallFromSource is not yet implemented — Phase 3 scope.
- **Plugin migration `Migrator` creates backup before applying migrations.** Backup is stored in `~/.xbot/plugins/<id>/backups/<version>/`. Rollback restores from the most recent backup. Migrations run sequentially by version order.
- **`toolHint` zone plugins run synchronously on PostToolUse hook.** When `isHintPlugin=true`, the hook trigger runs `runScript` inline (not via triggerCh). The engine calls `PluginManager.GetToolHints()` immediately after the hook returns to populate `ToolProgress.ToolHints`. **`GetToolHints()` consumes (clears) the hint after reading** to prevent stale content from attaching to the next tool.
- **`snapshotIterationChange` must include ActiveTools(done).** When an iteration ends, completed tools may still be in `ActiveTools` (status=done) rather than `CompletedTools` (which is populated later by `progressFinalizer`). Only checking `CompletedTools` loses ToolHints data.
- **Do NOT use glamour to render diff inside progress panel.** Glamour's output (background fills, margins, line wrapping) corrupts the progress panel border layout. Use direct ANSI coloring (`renderDiffANSI`) with width truncation instead.
- **`runScript` must `os.Stat(workDir)` before setting `cmd.Dir`.** On Windows parallel tests, temp dirs may be cleaned up before the script runs, causing `chdir` failure. If dir doesn't exist, skip setting `cmd.Dir` and run in plugin's own directory.

### Windows
- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` not in Go stdlib — define as uint32 constants.
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion.
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files.
- PowerShell env output is newline-delimited, not null-delimited.

### Worktree
- **Primary registration must NOT check dirty tree.** `WorktreeTool.init` and `AutoDetectAndInit` both skip dirty check when registering as primary (first agent in repo). Only worktree creation requires a clean tree — `git worktree add` fails on dirty repos.
- **AutoDetectAndInit runs in `processMessage` before `buildPrompt`.** Session CWD is updated before system prompt construction, so the agent sees the correct worktree path from the start.
- **`IsWorktreeIsolated` overrides `isUnrestricted()`.** CLI mode (`sandbox="none"`) normally bypasses all path checks, but when `IsWorktreeIsolated=true`, `isUnrestricted()` returns `false` — path boundaries are enforced even for CLI sessions.
- **Worktree paths must be outside main repo.** Git rejects `git worktree add` inside the main working tree. Paths are placed at `{repo}/../.xbot-worktrees/{role}-{instance}/`.
- **Worktree creation is serialized via `GlobalWorktreeRegistry.mu`.** Two agents creating worktrees simultaneously would race on `.git/worktrees/` lockfiles.
- **`go:embed embed_skills/*` auto-discovers new skills.** Adding a directory under `tools/embed_skills/` requires zero code changes — the skill is automatically available.

## Development Principles

### Never Blame the User's Binary

**永远不假设用户用了旧二进制。** 如果怀疑版本问题，说明自己的排查逻辑有漏洞，不是用户的问题。

### Always Prefer Explicit

**核心原则：永远优先使用显式 API，避免隐式假设。**

本项目遵循 "always prefer explicit" 开发原则。大量 bug 源于隐式设计——调用者无法从 API 签名推断出所有必要参数或行为。

#### 具体实践

1. **避免直接使用结构体作为公共 API 参数**
   - ❌ `func NewFoo(cfg FooConfig) *Foo` — 调用者可能遗漏 `FooConfig` 中的关键字段
   - ✅ `func NewFoo(opts ...FooOption) *Foo` — 使用私有结构体 + 构造函数 + 显式 Option 模式
   - ✅ `func NewFoo(required string, optional ...string) *Foo` — 必填参数显式列出

2. **假设调用者只看到你的 API 签名**
   - 调用者没有义务阅读实现细节
   - API 签名应自解释：参数名、类型、顺序应清晰表达意图
   - 使用 `// WithXxx` 风格的 Option 函数提供可选配置

3. **宁可冗长，不要隐晦**
   - 5 个显式参数优于 1 个包含 20 个字段的结构体
   - 如果必须用结构体，确保必填字段在构造函数中强制提供
   - 使用 `Must` 前缀函数（如 `MustParse`）在编译期捕获错误

4. **文档即合同**
   - 每个公共函数/类型必须有 godoc 注释
   - 注释应说明 "什么" 和 "为什么"，而不仅仅是 "如何"
   - 参数约束（如 "must not be empty"）应在注释中明确说明

#### 为什么重要

- 减少运行时 panic 和零值 bug
- 提高代码可读性和可维护性
- 让新贡献者能快速理解 API 用法
- 编译器帮你捕获更多错误

## Project Context

`ProjectContextMiddleware` auto-loads this file into system prompt. After code changes, update relevant Knowledge Files to keep documentation in sync.
