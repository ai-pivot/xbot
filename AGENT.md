# xbot

> Go AI Agent framework with message bus + plugin architecture. Supports Feishu/QQ/CLI/Web channels, tool calling, pluggable memory, skills, subagents, MCP integration.

## Quick Reference

- Entry points: `cmd/xbot-cli/` (CLI), `cmd/runner/` (remote sandbox)
- Build: `go build ./...` | Test: `go test ./...` | Lint: `golangci-lint run ./...`
- Config: `~/.xbot/config.json`, env var overrides
- Pre-commit: gofmt → golangci-lint → go build → go test

## Knowledge Files

- `docs/agent/architecture.md` — package map, message flow, pipeline, key interfaces, concurrency
- `docs/agent/agent.md` — agent loop, middleware, SubAgent, context management, masking
- `docs/agent/llm.md` — LLM clients, streaming pitfalls, retry behavior
- `docs/agent/tools.md` — built-in tools, hooks system (agent/hooks/), sandbox types
- `docs/agent/hooks.md` — hooks lifecycle events, handler types, configuration, gotchas
- `docs/agent/channel.md` — CLI, Feishu, Web, QQ adapters
- `docs/agent/memory.md` — letta vs flat providers
- `docs/agent/conventions.md` — error handling, logging, testing, naming, build

## Gotchas — MUST READ Before Any Code Change

### Concurrency
- **Never `defer` semaphore release inside a loop.** Deadlock when iterations exceed capacity. Release immediately after Generate completes.
- Non-blocking channel sends: always use `select` with `ctx.Done()` to prevent blocking on full channels during shutdown.
- **User-scoped semaphores must not be hardcoded to capacity 1 when one sender can own multiple independent chats/sessions (for example remote CLI windows authenticated as `admin`).** Size them from configured concurrency or key them by session, otherwise different windows will block each other and look like a leaked semaphore.

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
- **`SanitizeMessages()` (formerly `FixupTrailingToolCalls`) is the final validation gate before sending to LLM.** It strips: (1) assistant messages with empty content AND no tool_calls — these are rejected by OpenAI/Anthropic with "Invalid assistant message: content or tool_calls must be set"; (2) trailing unpaired tool_calls from cancelled runs. Called in `buildPrompt` and `engine.go`. Any new message-persisting code path that might produce empty assistants must be fixed at source, but `SanitizeMessages` is the safety net — it logs warnings for each stripped message to aid debugging.

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

### Hooks System
- **Old `ToolHook`/`HookChain` is gone.** Replaced by `agent/hooks/Manager`. Any code referencing `HookChain`, `ToolHook`, `executeWithHooks` is stale.
- **Manager.Emit() is shared across Agent + SubAgents** (same instance). Must be concurrency-safe.
- **Decision priority**: `deny > defer > ask > allow`. Low-priority layer deny cannot be overridden by high-priority allow.
- **Command hooks disabled by default** — requires `enable_command_hooks: true` in config.
- **Max 10 handlers per event**, total timeout 60s. Excess silently truncated with warning log.

### Windows
- `syscall.PROCESS_QUERY_LIMITED_INFORMATION` and `STILL_ACTIVE` not in Go stdlib — define as uint32 constants.
- `exec.ExitError.ExitCode()` is cross-platform; avoid `syscall.WaitStatus` type assertion.
- `signal.Notify(sigCh, syscall.SIGTSTP)` doesn't compile on Windows — use build-tagged files.
- PowerShell env output is newline-delimited, not null-delimited.

## Development Principles

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
