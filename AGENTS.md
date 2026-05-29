# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox), `cmd/xbot/` (server)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Subscriptions: `~/.xbot/config.json` (CLI) or DB `user_llm_subscriptions` (Server) — the single source of truth for LLM config
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, Transport (Call+Close)/Backend/DirectBackend/Lifecycle separation, key interfaces, concurrency, TokenTracker, CompressPipeline, PersistenceBridge
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking, dynamic context, reminder
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior, subscription system, model tiers (vanguard/balance/swift)
- `docs/agent/tools.md` — built-in tools: Shell, Read, Edit, Glob, Grep, Cd, Fetch, WebSearch, Cron, SubAgent, CreateChat, SendMessage, Worktree, config, tui_control, TodoWrite, context_edit, AskUser, DownloadFile, ChatHistory, ManageTools, Skill, EventTrigger, TaskManager, hooks system (agent/hooks/), sandbox types
- `docs/agent/settings.md` — settings system: single registry (agent/setting_runtime.go), cli_settings.go, UpdatePerModelConfig, subscription-scoped vs user-scoped, runtime apply chain
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build, **local/remote unification**
- `docs/agent/hooks.md` — hooks lifecycle events, handler types, configuration, gotchas
- `docs/agent/channel.md` — CLI (BubbleTea TUI), Feishu, Web, QQ adapters, asyncCh pattern, deterministic rendering, mouse support, settings panels
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build
- `docs/agent/plugin.md` — plugin system architecture, runtimes, integration, RPC bridge
- `docs/agent/worktree.md` — git worktree-based multi-agent workspace isolation, WorktreeRegistry, AutoDetectAndInit, peer discovery, path security

## Gotchas — MUST READ Before Any Code Change

### Concurrency
- **Never `defer` semaphore release inside a loop.** Deadlock when iterations exceed capacity. Release immediately after Generate completes.
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown.
- **User-scoped semaphores must not be hardcoded to capacity 1 when one sender can own multiple independent chats/sessions (for example remote CLI windows authenticated as `admin`).** Size them from configured concurrency or key them by session, otherwise different windows will block each other and look like a leaked semaphore.
- **`SetMaxConcurrency` must clear `userSemaphores` cache.** The global semaphore is rebuilt with the new capacity, but `getUserSemaphore` caches per-user channels in a `sync.Map` via `LoadOrStore`. Without `Clear()`, users with custom LLM keep using the cached semaphore with the OLD capacity forever. Symptom: setting max_concurrency to 100 has no visible effect.
- **`cancelChildSessions` must only cancel sessions with matching `parentKey`.** The old code called `cancelCurrent()` on ALL interactive sessions inside the `Range` loop before checking `parentKey`. This killed sibling/peer background agents when any single agent was unloaded or panicked — all N peer agents die simultaneously at whatever time the first one finishes. The `parentKey` check must happen BEFORE `cancelCurrent()`.

### Subscription & Settings
- **`agent/setting_runtime.go` is the SINGLE source of truth for runtime setting handlers.** Both CLI and server use it. Never create a second handler registry — it will silently diverge.
- **`backendSubscriptionManager` is the SINGLE SubscriptionManager implementation.** No more local/remote/config variants. All subscription operations go through Backend interface → Transport (local or remote).
- **Never use `UpdateSubscription` for PerModelConfig changes.** Use `UpdatePerModelConfig(subID, model, pmc)` — it only touches PerModelConfigs, never touches credentials. The old List→modify→Update pattern reads masked keys from the API, then writes them back, destroying real credentials.
- **`serverapp/rpc_table.go:updateSubscription` starts from EXISTING subscription, only overlays non-masked fields.** Client sends masked keys (****) — the handler preserves real credentials from DB.
- **`max_context_tokens` is ScopeSubscription.** Stored in `PerModelConfigs[model].MaxContext`, NOT in user_settings DB or config.Agent.MaxContextTokens. Changing it in `/settings` writes to subscription via `UpdatePerModelConfig`.
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
- **`GenerateStreamAndCollect` must NOT use `perAttemptCtx` for streaming.** A per-attempt deadline binds to the underlying HTTP connection via ctx. The deadline kills active streams mid-generation even when chunks are still flowing (e.g., DeepSeek long reasoning > 120s). Stream timeout is managed by `CollectStreamWithCallback`'s idle timeout (120s without any chunk) — timer resets on every received chunk, so actively-streaming responses never time out.
- **MCP stdio `exec.Command` uses process PATH, not `cmd.Env`.** Go's `exec.Command("npx", ...)` resolves the executable using the *process* PATH (`os.Getenv("PATH")`), ignoring `cmd.Env`. When xbot runs as a service with minimal PATH, tools like npx/nvm are invisible. `ConnectStdioServer` now calls `resolveCommand()` to pre-resolve the absolute path using the login shell PATH from `getLoginShellEnv()`. Also, `getLoginShellEnv` uses `bash -i -l` (interactive login shell) because `bash -l` alone skips `.bashrc` where nvm initializes.

- **SQLite `datetime()` comparison with RFC3339 strings is a silent trap.** `created_at` stores `2026-05-24T14:00:00+08:00` (RFC3339 with timezone). `datetime(?, '-2 seconds')` returns `2026-05-24 05:59:58` (UTC, no timezone). Raw string comparison: `'T'` (0x54) > `' '` (0x20) at position 10, so `created_at > datetime(?)` is **always TRUE**, breaking any time-window dedup/filter. Always wrap both sides in `datetime()`: `datetime(created_at) > datetime(?, '-2 seconds')`.

### Startup
- `NewOpenAILLM` loads model list asynchronously. `ListModels()` returns fallback immediately.
- Settings save is synchronous — all local I/O, no network calls.
- **`SaveToFile` uses deep JSON merge to preserve unknown fields.** `json.Unmarshal` silently drops fields not in the Go struct. `SaveToFile` reads the existing disk file first and recursively merges struct JSON into it, so user-added custom fields (or future struct fields added in newer versions) survive load→save cycles. Never bypass `SaveToFile` with raw `json.Marshal` writes to config.json.

### CLI / BubbleTea
- **`CLIChannelConfig` is ALWAYS passed by pointer, NEVER by value.** `NewCLIChannel(cfg *CLIChannelConfig, ...)` stores the pointer; all later modifications (callbacks like `BindChatFn`, `GetActiveProgressFn`) are immediately visible to the channel. Passing by value (the old code) silently discards any wiring done after construction, causing nil callbacks at runtime. Tests must always initialize `config: &CLIChannelConfig{}`.
- **`BindChatFn` must be set before `NewCLIChannel`.** The wiring order matters because the channel stores the pointer. All other callbacks (GetActiveProgressFn, GetTodosFn, etc.) follow the same rule — set on `cliCfg` before passing to `NewCLIChannel`.
- **`parseKeyInput` with modifiers must NOT set `Text` field.** `Key.String()` returns `Text` if non-empty (ultraviolet `key.go:392`), bypassing `Keystroke()`. `{Code:'c', Text:"c", Mod:ModCtrl}.String()` → `"c"` not `"ctrl+c"`, breaking cancel.
- **Iteration snapshot deduplication**: PhaseDone + handleAgentMessage can both snapshot the same iteration. Always dedup by iteration number, preferring PhaseDone (has reasoning from server).
- **`ElapsedWall` must be set in ALL snapshot paths** — missing it causes total time to fall back to summing only last iteration's tool.Elapsed.
- **Remote TUI progress: WS now sends `protocol.ProgressEvent` directly** (no conversion). Adding new fields to ProgressEvent is sufficient — no separate WsProgressPayload or conversion functions exist anymore.
- **Cancel key must be `channel:chatID` only (no senderID).** Background task notifications inject messages with senderID="user", while CLI users have different senderIDs. Including senderID in the cancel key makes cancel impossible for bg-task-triggered turns.
- **SubAgent remote mode: tick chain breakage** — `tickCmd()` injection must be unconditional (not gated on `!m.fastTickActive`) in splashDoneMsg, PhaseDone return, and history reload paths. Conditional injection causes chain to break during session switches.
- **SubAgent session view: viewport freeze on return** — when main session's turn ended while viewing agent session, PhaseDone is detected on return but assistant reply is missing. Tick handler with `busy=false` must check `!m.renderCacheValid` as fallback.
- **Background interactive SubAgent completion MUST append final assistant reply to messages.** Both the initial spawn path (`placeholder.messages`) and the "send" action path (`ia.messages`) must append `llm.NewAssistantMessage(out.Content)` + carry `ReasoningContent` + save `iterationHistory` + persist to DB via `cfg.Session.AddMessage`. The foreground path does all of this; both background paths (spawn + send) must match. `GetAgentSessionDump` reads `ia.messages` — without the assistant message, the session view shows only user message + tool calls but no final reply. The background "send" path must also append the user message from `action=send` and use `out.Messages` (not `cfg.Messages`) for Run-produced messages.
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
- **textinput `placeholderView()` emits `\x00` NUL bytes that break lipgloss word-wrap.** When placeholder (e.g. "在此输入...") has fewer runes than `Width+1`, `placeholderView()` fills the gap with NUL bytes. `lipgloss.Width()` counts these as 0-width, but lipgloss `Style.Render()` treats them as 1-column during word-wrap, causing the scrollbar `▐` to wrap to the next line in PanelBox. Fix: `strings.Map` to strip `\x00` from textinput `View()` output before rendering. Also: `placeholderView()` outputs `Width+1` visible chars total; `applyScrollbar` must use `>= contentWidth` (not `>`) when truncating.
- **`ensureAskUserCursorVisible` must use actual question line count, not hardcoded estimate.** `hardWrapRunes` can split a long question into many lines. The old code used `headerLines=2` (or 4), causing `cursorLine` to be grossly underestimated — every arrow key press reset scroll to the top. Fix: compute exact header height via `hardWrapRunes + strings.Count`. Also use `askUserPanelVisibleHeight()` (not `panelVisibleHeight()`) for the askuser split layout.
- **`m.defaultChatID` may contain session suffix, not just workDir.** When `GetLastActiveSession` returns a full chatID like `/path:Agent-xxx`, `defaultChatID` becomes that full chatID. `SetLastActiveSession` uses `ParseChatID` internally to extract the bare workDir for correct sessions file hashing — callers can pass either format safely.
- **Remote CLI `ChatRenameFn` must use upsert (INSERT ON CONFLICT), not UPDATE.** CLI sessions may not have a `user_chats` row (created locally, never INSERT'd). Plain `UPDATE` silently affects 0 rows. The upsert creates the row if missing. Both CLI-local and server-side rename paths now call `DeduplicateSessionName` to auto-suffix colliding names (adj-noun from session pool, then numeric fallback).
- **Session JSON `created_at` uses `flexTime` — must handle space-separated legacy format.** Older sessions stored timestamps as `"2026-05-08 20:40:28+08:00"` (space separator). Go's `time.Time.UnmarshalJSON` only accepts RFC3339 (`T` separator). A single malformed timestamp causes the entire `LoadDirSessions` to fail, making ALL sessions invisible and preventing new session creation. `flexTime` tries RFC3339 first, then falls back to space-separated formats.
- **`RenameSession` never changes chatID — only the display name.** chatID is the primary key in DB `tenants` table. Changing it disconnects the session from its message history. The function now returns `(actualName, error)` where `actualName` may differ from requested name after dedup.
- **RPC `get_history` must call `GetOrCreateTenantID` to update `last_active_at`.** Without this, all CLI sessions have NULL `last_active_at` in the DB, making `ListTenants` unable to find the most recent session.
- **Palette tab zone is a single line**: `trackPaletteZones` tracks tabs as ONE zone, not `tabCount` zones. The old code tracked one zone per tab, causing Y offset = `tabCount - 1` for all items below.
- **Session deletion must delete DB FIRST, then local JSON.** `deleteLocalSession` used to remove from local JSON before calling backend `SessionsDeleteFn`. If the backend delete fails (network error, DB lock), local JSON is already cleaned but the DB tenant persists. Creating a new session later with the same random name reuses the old tenant via `INSERT OR IGNORE` in `GetOrCreateTenantID`, restoring all deleted messages — a data leak. The fix swaps the order: backend first (fail fast, keep local JSON intact for retry), local JSON second. `showSessionCreateDialog` also explicitly calls `SessionsDeleteFn` as a safety net to clean up any residual DB tenant from a previously-failed deletion.

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

### CLI Rendering Performance
- **`syncProgressTodos` must do change detection before calling `relayoutViewport`.** High-frequency progress events carry the same Todos every time. Without `todosEqual()` check, each event triggers `relayoutViewport` → `fullRebuild` → `glamour.Render` + `chroma.Highlight` on ALL messages — ~34% CPU waste during agent work. Only call `relayoutViewport` when todo count changes; for content-only changes (e.g. item marked done), just set `renderCacheValid = false`.
- **`relayoutViewport` only invalidates render caches when width changes.** Height-only changes (todo bar, panel open/close, `endAgentTurn`) just resize the viewport without rebuilding all messages. Without this guard, every `endAgentTurn` / `handleProgressDone` triggers O(N) `fullRebuild` with `glamour.Render` on every message — accounts for ~36% CPU during spike periods.
- **`viewport.SetContent` calls `maxLineWidth` internally which runs `ansi.StringWidth` on every line — ~49% CPU with large content.** `cli_viewport_bypass.go` bypasses this by directly setting viewport's private `lines` and `longestLineWidth` fields via `unsafe` (offsets computed via `reflect.FieldByName` at init). The caller pre-wraps lines to `chatWidth` so `maxW = cw` is always correct.

### CLI Todo & Session Persistence
- **`cliModel.todoManager` MUST be initialized.** It was always nil (no assignment), making ALL `if m.todoManager != nil` dead code. `syncProgressTodos`, `endAgentTurn`, and `restoreSession` all relied on TodoManager for cross-turn/cross-session todo persistence, but silently did nothing. Now created in `cli.go:Start()`. Any new model construction must also set it.
- **`syncProgressTodos` must call `persistTodosToManager()` after updating `m.todos`.** Without this, todos live only in `m.todos` (volatile per-session slice) and are lost on turn end/session switch. The helper converts `[]CLITodoItem` → `[]tools.TodoItem` and calls `SetTodos` on the CLI-side TodoManager. `saveCurrentSession()` later calls `SaveToFile` for disk persistence.
- **`/su` and `/chat` commands MUST call `saveCurrentSession()` before changing `m.chatID`.** They were directly mutating identifiers without saving the old session's state (typing, progress, messageQueue, inputDraft) — permanent data loss on session switch. Now also reset critical fields (`typing`, `progress`, `inputReady`, `tickGen++`, etc.) after switching, mirroring `postRestoreSessionSetup`.
- **`syncProgressTodos` MUST NOT clear `m.todos` when `payload.Todos` is empty.** An empty `Todos` field only means "this progress event carries no todo data" (e.g. early thinking phase before `todo_write` executes), NOT "todos were deleted". Clearing on empty caused TODOs to vanish as soon as iteration started. Clear only on: user sending a message (`todosDoneCleared` guard), turn ending with all done (`endAgentTurn`), or explicit `todo_write([])`.

### CLI Race Conditions (Remote Mode Session Switching)
- **`handleSuHistoryLoad` must NOT clear `suLoading` before the stale check.** The old code set `m.suLoading = false` unconditionally on line 876, BEFORE checking `msg.chatID != m.chatID`. A stale RPC callback from the old session clears the new session's loading guard, letting progress events through prematurely and corrupting typing/idle state. The `suLoading=false` now only executes after the stale guard passes.
- **`handleHistoryReload` needs `channelName`/`chatID` stale check.** `cliHistoryReloadMsg` had no session identifiers — an old session's async history load could overwrite `m.messages` after the user already switched away. Added `channelName`/`chatID` fields to the message and a guard in the handler.
- **`splashTickMsg` must carry `gen` (tickGen) for stale rejection.** Rapid session switching creates multiple splash tick chains. Without gen checking, old ticks from cancelled sessions compete with new ticks, causing animation jitter and potential state corruption. Handler now returns early when `msg.gen != m.tickGen`.
- **`handleSuHistoryLoad` default branch does NOT restore `lastTokenUsage` — context bar shows 0 on first switch to idle session.** The `acceptProgress` branch restores token usage from `activeProgress.TokenUsage`, but when the target session has no active turn, the `default` branch runs and leaves `lastTokenUsage` nil. Fix: `suLoadHistoryCmd` now fetches token state via `GetTokenStateFn` and `suHistoryLoadMsg` carries `tokenPrompt`/`tokenCompletion`; the handler falls back to this when `lastTokenUsage` is still nil after processing.

### TUI Control & Config Tools (AI-Native)
- **`SyncLayoutSettings` and 5s polling goroutine REMOVED.** Layout sync is now event-driven: `ApplyInitialLayout` at startup, `doSaveSettings` → `cliSettingsSavedMsg` for user changes, `tui_control` for AI changes. `refreshRemoteValuesCache` runs ONCE at startup, not every 5s. `valuesCache` is updated on-demand by settings panel save, subscription switch, and config tool set callbacks. Adding polling back will cause ~30% CPU waste from unnecessary `invalidateAllCache` → `fullRebuild` cycles (7+ GB/min temporary string allocations observed).
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
- **Script plugin triggers are global hooks** (`hookRegistration.Global=true`). `bridge.Dispatch` skips session isolation for them — they manage per-workDir state and must fire for all sessions. Without this, multi-session remote CLI silently drops triggers for all but the last session that called `RefreshWorkDir`.

### Backend/Transport Architecture
- **CLI always uses Client → Transport → Server, even in local mode.** Client (agent/client.go) is a pure RPC client — every method is `Transport.Call("method", params)`.
- **Transport is the pure transmission layer** (agent/transport.go). Only 2 methods: `Call(method, payload)` and `Close()`. `ChannelTransport` (agent/transport_channel.go) dispatches directly to `ServerCore.HandleRPC`. `RemoteTransport` (agent/transport_remote.go) sends JSON-RPC over WebSocket.
- **ServerCore** (serverapp/server_core.go) is the shared server core for both local and remote modes. Owns Agent + RPCTable + Dispatcher + Bus. Created by CLI (local mode) and server.go (remote mode).
- **RPCTable is the single handler truth source** (serverapp/rpc_table.go). Handlers access `h.Ag` (the *Agent) directly — no intermediate Backend/DirectBackend layer.
- **Lifecycle interfaces** (agent/lifecycle.go): AgentRunner, EventRouter, CallbackRegistry — only used by the legacy Backend struct for remote mode.
- **Adding a new RPC method**: 1 handler in rpc_table.go (using h.Ag) + 1 method in client.go (Transport.Call) + 1 MethodXxx constant in req_types.go.
- **Adding new transports** (gRPC, MCP) only requires implementing 2 Transport methods (Call + Close).

### Channel Configuration
- **TUI channel config changes require live channel restart.** Writing config.json is not enough — Feishu/Web/QQ/NapCat channels are created once at startup via `registerChannels()`. `SetChannelConfig()` now calls `reconfigureFn` (set by server.go) to start/stop the affected channel. Any new channel type must be added to both `channelShouldRun()` and `createChannelInstance()`.
- **Key naming must be consistent across all channels.** Web used `enable` while Feishu/QQ/NapCat used `enabled` — the RPC handler only checked `enabled`, so web changes were silently ignored. All channels now use `enabled` (with backward compat for `enable`).
- **New client methods must be added to `*Client` in `agent/client.go`.** The `Client` struct is the unified RPC client used by both CLI and server modes. Both local (ChannelTransport) and remote (RemoteTransport) modes go through the same RPC path.
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
- **`RegisterPeer` always uses role="peer" — no primary concept.** Every session is an equal peer for awareness purposes. `WorktreeTool.init` and `AutoDetectAndInit` both create physical worktrees for each session. Only worktree creation requires `git worktree add` which uses `--detach HEAD` (works on dirty trees too).
- **`createWorktree` uses `--detach` not `-b`.** `git worktree add --detach HEAD` followed by `checkout -b` in the worktree. This avoids dirty-tree failures that `-b` would trigger.
- **AutoDetectAndInit runs in `buildPrompt` (not `processMessage`).** `buildPrompt` is the common code path for ALL message types. Session CWD is updated before system prompt construction.
- **`IsWorktreeIsolated` overrides `isUnrestricted()`.** CLI mode (`sandbox="none"`) normally bypasses all path checks, but when `IsWorktreeIsolated=true`, `isUnrestricted()` returns `false`.
- **Worktree paths must be outside main repo.** Git rejects `git worktree add` inside the main working tree. Paths: `{repo}/../.xbot-worktrees/{role}-{instance}/`.
- **Worktree creation is serialized via `GlobalWorktreeRegistry.mu`.** Two agents creating worktrees simultaneously would race on `.git/worktrees/` lockfiles.
- **Registry persisted per-repo to `{repo}/../.xbot-worktrees/registry.json`.** `saveRepoLocked` uses atomic tmp+rename. `ensureLoaded` lazily restores on first access. Orphaned worktree directories are skipped on load.
- **`BuildSystemReminder` takes `sessionKey` parameter.** Used to query `GlobalWorktreeRegistry` for worktree/peer info injected into sys_reminder per iteration.
- **`MethodSetCWD` only sets CWD when empty.** Prevents CLI session switch from overwriting worktree CWD with main workspace path. Session-specific CWDs (worktree, Cd tool) are preserved.
- **`go:embed embed_skills/*` auto-discovers new skills.** Adding a directory under `tools/embed_skills/` requires zero code changes.
- **`buildPrompt` 绝不恢复 CWD 到 worktree 路径。** 当 session 已注册后 `AutoDetectAndInit` 跳过，这是正确行为。agent 通过 Cd 工具主动切换的目录必须被尊重，如果在后续 buildPrompt 中强制恢复会破坏 agent 的 Cd 操作，产生人机对抗。
- **Cd 工具绝不强制 worktree 边界。** CLI 模式下 Cd 允许 agent 自由导航到任何目录，包括主仓库。如果对 Cd 加边界限制会导致 agent 无法读取 worktree 外的文件（如 AGENTS.md、知识库、配置文件），这是严重 bug。worktree 隔离是软性的工作指引，不是硬性监狱。

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
