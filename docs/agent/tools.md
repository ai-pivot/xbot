# tools/ — Built-in Tools

## Key Files

| File | Purpose |
|------|---------|
| `interface.go` | Tool interface, SubAgentManager, SessionMCPManagerProvider |
| `hook.go` | (removed — replaced by agent/hooks/) |
| `approval.go` | ApprovalHook (permission control) |
| `sandbox.go` | Sandbox interface (Run, Sync, Resolve) |
| `sandbox_router.go` | Selects sandbox type (none/docker/remote) |
| `docker_sandbox.go` | Docker sandbox implementation |
| `remote_sandbox.go` | Remote runner sandbox (~1300 lines) |
| `cd.go` | Cd tool (directory switching, persists across turns) |
| `edit.go` | FileReplace + FileCreate tools |
| `read.go` | Read tool (line-numbered output) |
| `grep.go` | Grep tool (Go RE2 regex) |
| `glob.go` | Glob tool (pattern matching) |
| `fetch.go` | Fetch tool (HTTP → markdown) |
| `shell.go` | Shell tool (command execution) |
| `shell_unix.go` | Unix process helpers: setProcessAttrs (Setpgid), killProcessTree (-pgid SIGKILL), isProcessAlive (Signal(0)), defaultShell, loginShellArgs |
| `shell_windows.go` | Windows process helpers: setProcessAttrs (CREATE_NEW_PROCESS_GROUP), killProcessTree (taskkill /T /F), isProcessAlive (OpenProcess), defaultShell (powershell.exe), loginShellArgs |
| `none_sandbox.go` | None sandbox (local execution). Uses platform helpers from shell_unix/shell_windows.go |
| `mcp_common.go` | MCP protocol definitions |
| `mcp_remote_transport.go` | MCP HTTP transport |
| `tui_control.go` | **TuiControlTool** — AI operates TUI sidebar/layout/theme via asyncCh |
| `config_tool.go` | **ConfigTool** — AI reads/modifies config via SettingsSvc auto-injection |
| `memory_tools.go` | Core memory tools (append/replace/rethink/search/recall) — letta only |
| `knowledge_tools.go` | Shared file-write helper (writeFileSandboxAware) — knowledge tools removed, project knowledge via AGENTS.md + docs/agent/ |
| `flat_memory_tools.go` | Flat memory tools (read/write/list) — flat provider only |
| `context_edit.go` | ContextEdit tool (conversation history surgery) |
| `cron.go` | Cron tool (scheduled tasks) |
| `task_manager.go` | Background task management |
| `checkpoint.go` | CheckpointHook + CheckpointStore (Ctrl+K rewind file rollback) |
| `create_chat.go` | CreateChat tool — create agent private chat or moderated group chat |
| `send_message.go` | SendMessage tool — unified routing to agent/group/IM targets |
| `group_state.go` | GroupState struct + sync.Map store for meeting-mode group chats |

## Tool Schema Rule

**Array types MUST include `Items` field.** OpenAI API rejects schemas without it.
```go
Items: &llm.ToolParamItems{Type: "string"}
```

## Hooks System

The old `ToolHook`/`HookChain` (`tools/hook.go`, `tools/hook_builtin.go`) has been replaced by the new `agent/hooks/` package. See `docs/agent/hooks.md` for full details.

### Key Changes
- `tools/hook.go`, `tools/hook_builtin.go` — **deleted** (replaced by `agent/hooks/`)
- `tools/approval.go` — `ApprovalHook` removed, `ApprovalRequest`/`ApprovalResult`/`ApprovalHandler` types preserved
- `tools/checkpoint.go` — `CheckpointHook` removed, `CheckpointStore` preserved (used by `CheckpointCallback`)
- Checkpoint initialization: `agent.go` NewAgent creates `CheckpointState`, registers `CheckpointCallback` as builtin. Per-session `CheckpointStore` created lazily in `processMessage` → `ensureCheckpointStore`, wired to CLI via `SetCheckpointState`.
- Old `HookChain` field in `engine.go`/`agent.go` → replaced by `hooks.Manager`

### agent/hooks/ Package

| File | Purpose |
|------|---------|
| `manager.go` | Manager — Emit, Decision aggregation, config reload, concurrency-safe |
| `event.go` | Event interface + 17 concrete event types + BasePayload |
| `types.go` | Action/Decision/Result/HookDef/CallbackHook/Executor interfaces |
| `matcher.go` | Exact/multi-select/regex/if-condition matching |
| `config.go` | hooks.json three-layer config loading (user/project/local) |
| `executor_command.go` | Shell command executor (stdin JSON, exit code semantics) |
| `executor_http.go` | HTTP POST executor (with SSRF protection) |
| `executor_mcp.go` | MCP tool executor (variable interpolation) |
| `builtin.go` | Logging/Timing/Approval/Checkpoint as callback hooks |

### 17 Lifecycle Events
SessionStart, SessionEnd, UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure,
PostToolBatch, PermissionRequest, PermissionDenied, SubAgentStart, SubAgentStop,
AgentStop, AgentError, PreCompact, PostCompact, CronFired, WebhookReceived

### Decision Priority (multi-handler conflict)
`deny > defer > ask > allow`

## Sandbox Types

- `none`: direct execution (default). Uses `/bin/bash -l -c` on Unix, `powershell.exe -Command` on Windows
- `docker`: Docker container per OS user (always Linux)
- `remote`: remote runner process via runner protocol (always Linux)

## Agent Communication (CreateChat + SendMessage)

Two tools for inter-agent messaging via the Dispatcher's AgentChannel mechanism.

### CreateChat
- **type=agent**: Spawns an interactive SubAgent (`InteractiveSubAgentManager.SpawnInteractive`), registers an `AgentChannel` in the Dispatcher. Returns `agent:<role>/<instance>` address.
- **type=group**: Creates a `GroupState` in the global `sync.Map`. Members are address strings (not pre-spawned). Returns `group:<id>` address.

### SendMessage
Routes by address prefix:
- `agent:*` → `Dispatcher.SendMessageCtx()` → `AgentChannel.Send()` (RPC, blocks for reply)
- `group:*` → `GroupState` meeting mode: parses `@agent:xxx` mentions, builds history prompt, sends to each mentioned agent sequentially via `sendMessageWithCtx()`
- `peer:*` → `PeerMessageFn` (async broadcast, busy→inject, idle→user message)
- `session:*` → `PeerMessageFn` (async to specific session)
- `feishu:/web:/qq:/cli:` → `Dispatcher.SendMessage()` → IM channel (fire-and-forget)

**Deadlock prevention**: AgentChannel dispatches each request to its own goroutine, so concurrent RPCs don't block each other. Two agents sending to each other simultaneously won't deadlock.

**Ctrl+C propagation**: `sendMessageWithCtx()` uses `bus.MessageSenderCtx` (type assertion) to pass caller context through `OutboundMsg.Ctx` → `AgentChannel.Send()` listens on both `replyCh` and `msg.Ctx.Done()`. Ctrl+C cancels the caller's context → `Send()` returns immediately.

### Meeting Mode (Group)
- Moderator (caller) controls who speaks via `@agent:role/instance` mentions
- Messages without @mentions are recorded in history but don't trigger agents
- @mentioned agents receive full discussion history + current question
- Round counter increments per moderator message WITH mentions; group auto-closes at `max_rounds` (default 10)
- `group_state.go`: `GroupState` struct with `sync.Mutex`, global `groupStore sync.Map`
- `channel/agent_channel.go`: `AgentChannel` wraps SubAgent as Dispatcher Channel with **concurrent** per-request RPC reply channels. Uses `ac.wg.Go()` dispatch + `msg.Ctx` for caller cancellation.

## Windows Support

- **None sandbox only** — docker/remote sandboxes are always Linux
- Shell: `powershell.exe -Command` replaces `/bin/bash -l -c`
- Process management: `taskkill /T /F` replaces `kill(-pgid, SIGKILL)`; `CREATE_NEW_PROCESS_GROUP` replaces `Setpgid`
- `run_as` (sudo) not supported on Windows — returns error
- Platform helpers in `shell_unix.go` / `shell_windows.go`: `setProcessAttrs`, `killProcessTree`, `isProcessAlive`, `defaultShell`, `loginShellArgs`
- `cmdbuilder` uses `defaultShell`/`defaultShellFlag` constants from `shell_default.go` / `shell_windows.go`

## TUI Control & Config Tools (AI-Native)

### tui_control (`tools/tui_control.go`)

Core tool (always loaded). AI operates TUI sidebar, layout, and themes.

**Actions**: `switch_session`, `close_session`, `set_layout`, `set_theme`, `send_slash`, `reload_plugins`, `reload_hooks`

**send_slash**: Executes TUI-only slash commands (`/palette`, `/settings`, `/rewind`, `/tasks`, `/clear`, etc.). Do NOT use `send_slash` for agent-level commands like `/set-llm`, `/unset-llm`, `/set-model`, `/models`, `/new`, `/compress`, `/usage`, `/context` — those are handled natively by the agent command registry. `send_slash` goes through BubbleTea's event loop (synchronous RPC); commands that call back into the agent (like `/usage` did via `usageQueryFn` → agent RPC) will deadlock.

**Flow**: `Execute()` → `ctx.TUIControl(action, params)` → `CLIChannel.SendTUIControl()` → `asyncCh` → `handleAsyncDrain` → `program.Send` → event loop → `handleSessionControlMsg`

**Remote mode**: Server `RemoteTUICtrlFn` → `RemoteCLIChannel.SendTUIControlRequest()` → WS `tui_control_req` → client `readPump` → goroutine → `SendTUIControl` → asyncCh. ReadPump stays responsive (goroutine wrapper), allowing RPC calls within handlers.

**Persistence**: `handleSessionControlMsg` calls `persistCLISettingsValues` after applying changes. Layout keys (sidebar_width etc.) are written directly to `config.json` via `saveLayoutToConfig()` (they're not in `Config` struct).

### config (`tools/config_tool.go`)

Core tool (always loaded). AI reads/modifies xbot configuration.

**Actions**: `list`, `get`, `set`, `subscriptions`, `reload_plugins`, `reload_hooks`, `runner`

**Runner action**: `config action=runner` with sub-actions:
- `sub=create name=NAME mode=native|docker workspace=PATH [llm_provider=... llm_model=...]` — create a new runner (auto-starts remote sandbox server if needed)
- `sub=list` — list all runners for current user (with online status)
- `sub=delete name=NAME` — delete a runner
- `sub=switch name=NAME` — switch active runner for current session (session-level, not user-level; all tools immediately route to new runner)
- `sub=rename name=OLD new_name=NEW` — rename a runner
- `sub=` (empty) — show current active runner

**Runner routing**: Session-level binding via `SandboxRouter.sessionRunners` sync.Map. `config switch` writes `"channel:chatID" → runnerName`. Per-tool-call sandbox re-resolution in `buildToolExecutor` (engine_wire.go) and `defaultToolExecutor` (engine.go) ensures all tools (Shell, Read, Grep, Glob, FileReplace...) immediately use the new runner after switch. CWD auto-resets to runner's live workspace from `GetConnectionInfo`.

**LLM model operations**: To switch model → tell user to run `/set-model <model>`. To configure custom LLM → tell user to run `/set-llm`. To view usage → tell user to run `/usage`. All these are agent-level commands handled natively. Do NOT use `send_slash` or `config set` for these — they have dedicated paths.

**Injection**: `buildToolContext` auto-injects `ConfigGet`/`ConfigSet` from `cfg.SettingsSvc`, and `RunnerCreate`/`RunnerList`/`RunnerDelete`/`RunnerGetActive`/`RunnerSetActive` from `tools.RunnerTokenStore` via `tools.GetRunnerTokenDB()`. Works in ALL modes (local + remote via RPC). Does NOT rely on Agent `SetTUICallbacks`.

**Masking**: Sensitive keys (`api_key`, `runner_token`) show `sk-a***` on read. Writes are NOT blocked — users can type API keys anyway.

## Worktree Tool (`tools/worktree.go`, `tools/worktree_registry.go`)

Git worktree-based multi-agent workspace isolation. When multiple agents work on the same git repository, this tool creates isolated worktrees so agents don't conflict on the same files.

**Actions**: `init`, `cleanup`, `status`

- **init**: Creates a new git worktree for the calling agent, registers it in the global `WorktreeRegistry`. First agent in a repo uses the main project directly (role="primary"). Subsequent agents get their own worktree (role="peer" or "child"). Returns the worktree path.
- **cleanup**: Removes the worktree and deregisters from the registry.
- **status**: Lists all active worktrees in the current repo, including peers.

**Registry**: `WorktreeRegistry` (`tools/worktree_registry.go`) is a global singleton tracking all active worktrees by repo path. Supports peer discovery so agents can find each other's workspaces.

## TodoWrite / TodoList Tools (`tools/todo.go`)

Structured TODO management with cross-session persistence.

- **TodoWrite**: Takes an array of `{id, text, done}` items and overwrites the current TODO list.
- **TodoList**: Returns the current TODO list with completion status.

**Persistence**: CLI's `TodoManager` persists todos to `~/.xbot/todos/{chatID}.json` via `persistTodosToManager()`. Restored on session switch and startup. `syncProgressTodos` synchronizes progress panel todos with the persisted store.

## Cd Tool (`tools/cd.go`)

Changes the agent's working directory. Subsequent tool calls (Shell, Read, Grep, Glob, etc.) execute in the new directory.

**Persistence**: Working directory persists across conversation turns. SubAgents inherit the parent's working directory via `parent_cwd` metadata.

## AskUser Tool (`tools/ask_user.go`)

Allows the agent to ask the user questions and wait for responses. Only available in CLI mode. Supports:
- Multiple questions in a single call
- Optional multiple-choice options for each question
- Multi-line question text

The CLI channel renders questions in an interactive input panel.

## DownloadFile Tool (`tools/download.go`)

Downloads files from URLs or Feishu messages to the local filesystem. Supports:
- Web/OSS files via signed URLs
- Feishu files via message_id + file_key
- Sandbox-aware path resolution

## EventTrigger Tool (`tools/event_trigger.go`)

Manages webhook event subscriptions for external service integration. Actions: `add`, `list`, `remove`, `enable`, `disable`. Returns webhook URLs that external services can POST to. Supports Go template message rendering with event data.

## Other Tools

| Tool | File | Purpose |
|------|------|---------|
| `ChatHistory` | `tools/chat_history.go` | Query recent chat message history |
| `Skill` | `tools/skill.go` | Load skill documentation on demand |
| `ManageTools` | `tools/manage_tools.go` | Manage MCP servers (add/remove/list/reload) |
| `task_status` / `task_kill` | `tools/task_tools.go` | Check/terminate background tasks |
| `recall_masked` | `tools/recall_masked.go` | Retrieve full content of masked observations |
| `offload_recall` | `tools/offload_recall.go` | Retrieve full content of offloaded tool results |
| `knowledge_tools` | `tools/knowledge_tools.go` | ~~Removed~~ — project knowledge now via AGENTS.md + docs/agent/ using standard Read/FileReplace |
| `logs` | `tools/logs.go` | Query agent logs |
| `WebSearch` | `tools/web_search.go` | Tavily web search |
| `Runner` | `tools/sandbox_runner.go` | Manage remote sandbox connections |

## GrpcPluginTransport (`agent/transport_grpc.go`)

Bidirectional JSON-RPC over stdin/stdout for gRPC plugin channel providers. Replaces the old `serverapp/channel_bridge_grpc.go` approach where the plugin's activation process was reused for channel communication.

### Architecture

```
xbot (serverapp)                    Plugin (separate process)
┌─────────────────┐                 ┌─────────────────┐
│ RPCTable        │◄───stdout───────│ Plugin main loop │
│ (dispatch)      │─────stdin──────►│ (JSON-RPC)       │
│ GrpcPlugin      │                 │ HTTP server /    │
│ Transport       │◄──eventCh───────│ bot framework    │
└─────────────────┘                 └─────────────────┘
```

### Protocol (identical to WS)

- Plugin → xbot (RPC request): `{"id":"1","method":"send_inbound","params":{...}}`
- Plugin → xbot (RPC response): `{"id":"1","result":{...}}`
- xbot → Plugin (event push): `{"type":"progress","progress":{...}}`
- xbot → Plugin (RPC request): `{"id":"2","method":"channel_send","params":{...}}`

### Key Interfaces

- `channel.Channel`: registered in Dispatcher for message routing
- `channel.ProgressSender`: push progress/stream events
- `channel.SessionStateSender`: push session state changes
- `channel.UserMessageInjector`: inject background messages

### Lifecycle

1. Plugin activates → declares `channel_provider` in activation response
2. `serverapp/channel_plugin.go` (`grpcPluginChannelProvider`) spawns a **dedicated** process
3. `GrpcPluginTransport` wraps the process stdin/stdout as JSON-RPC channel
4. `readLoop()` routes incoming messages: RPC requests → RPCTable dispatch, RPC responses → pending calls
5. `eventPushLoop()` pushes WSMessage events from xbot to plugin
6. Channel is registered in Dispatcher, receives outbound messages via `Send()`

### Related Files

| File | Purpose |
|------|---------|
| `agent/transport_channel_plugin.go` | ChannelPluginTransport: bidirectional JSON-RPC over stdin/stdout |
| `agent/channel_plugin_prompt.go` | channelPluginPromptProvider: thread-safe prompt storage for channel plugins |
| `serverapp/channel_plugin.go` | stdioChannelPluginProvider: spawns process, creates transport |
| `plugin/channel_provider.go` | ChannelProviderFactory: creates provider from plugin decl |
| `plugin/channel_tool_bridge.go` | ChannelToolBridge: adapts channel-declared tools to tools.Tool |
| `plugin/examples/echo-channel/` | Example plugin: HTTP echo server over JSON-RPC |

### Channel-Scoped Tools

Channel plugins can declare tools via the `"channel_tools"` protocol message.
These tools are registered with `Registry.RegisterForChannel(channel, bridge)`
and only visible in sessions of that channel.

Flow:
1. Channel process sends `{"type":"channel_tools","tools":[...]}` on stdout
2. `ChannelPluginTransport.handleChannelTools()` parses the declaration
3. Each tool is wrapped in `ChannelToolBridge` (implements `tools.Tool`)
4. Registered via `Registry.RegisterForChannel(channelName, bridge)`
5. When agent calls the tool → `ChannelToolBridge.Execute` → `Call("execute_tool")` → channel process

Hot-update: sending a new `channel_tools` message replaces the entire tool set
(`UnregisterChannelTools` + re-register).

### Channel-Specific Prompt

Channel plugins can declare channel-specific system prompt fragments via the
`"channel_prompt"` protocol message. These are injected into the agent's system
prompt for sessions of that channel — identical to built-in channels (feishu, cli).

Flow:
1. Channel process sends `{"type":"channel_prompt","system_parts":{"05_channel_xxx":"..."}}` on stdout
2. `ChannelPluginTransport.handleChannelPrompt()` stores parts in `channelPluginPromptProvider`
3. `OnChannelPrompt` callback fires → `Agent.AddChannelPromptProvider()` registers with pipeline
4. `ChannelPromptMiddleware` (priority 5) matches `MessageContext.Channel` and injects parts

Key files: `agent/channel_plugin_prompt.go` (provider), `agent/channel_prompt.go` (middleware).
Hot-update: sending a new `channel_prompt` replaces the entire parts map.
The `ChannelPromptMiddleware` uses `sync.RWMutex` for concurrent `AddProvider` access.

## Tool Visibility Model

All registered tools are **always visible** to the LLM with full parameter schemas. There is no
on-demand activation, no `load_tools`, and no expiry mechanism. This applies uniformly to:

- Built-in tools (`Register` / `RegisterCore` — equivalent)
- MCP tools (global + per-session) — full schemas via `mcpSchemaProvider` interface
- Channel-scoped tools (`RegisterForChannel`)
- Tenant-specific tools (`RegisterForTenant`)

The previous two-phase system (stub schemas + `load_tools` activation + `maxIdleRounds` expiry)
was removed because it caused LLM confusion: tools visible in conversation history would silently
disappear from the tool list, and the execution gate would reject calls with "not loaded" errors,
creating feedback loops.
