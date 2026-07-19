# channel/ — Channel Adapters

## Progress snapshot + semantic log

Structured progress has one channel-agnostic production path. The agent
converts each engine event to one `protocol.ProgressEvent`, assigns its
monotonic semantic `Seq`, derives the completed-iteration delta once, stores the
current snapshot without cumulative history, then broadcasts an isolated clone of that semantic event
to every registered `channel.ProgressSender` (CLI, Web, and plugin channels).
Channels are transports only; they must not derive iteration history or use
channel-specific progress reducers.

Reconnect consumers install the active snapshot and use `ProgressEvent.Seq` as
the semantic watermark. Events with `seq <= snapshot.seq` are replayed or stale
and are ignored. `IterationHistory` is the authoritative completed-iteration
log, keyed by iteration; clients never synthesize an iteration when the current
iteration advances. SSE/WS envelope sequence numbers remain transport replay
IDs and are independent from the semantic progress watermark.

History recovery keeps DB rows as-is (including incrementally-persisted
assistant `ToolCalls` from the active turn). The frontend reconciles: when the
last history assistant is the active turn, `liveProgress` attaches to that row
instead of appending a separate `liveMessage`. The snapshot's
`iterationHistory` is authoritative for completed iterations, and
`LiveIteration` renders only the current iteration's tools — no overlap, no
duplicate. This mirrors CLI's `acceptProgress` merge where the history
assistant IS the streaming slot.

## Package Structure (Refactored)

The `channel` package has been split into a shared root package plus implementation sub-packages:

```
channel/              # Root package — shared core types, interfaces, infrastructure
├── channel.go        # Channel interface
├── types.go          # OutboundMsg, InboundMsg, AskQItem, SessionChatMessage
├── interfaces.go     # ProgressSender, UserMessageInjector, SessionStateSender
├── subscription.go   # Subscription/PerModelConfig aliases, ConvertMessagesToHistory
├── session_utils.go  # DeduplicateSessionName, NameEntry, GenerateSessionName
├── dispatcher.go     # Outbound message routing to channels
├── agent_channel.go  # AgentChannel for SubAgent communication
├── capability.go     # SettingsCapability, SettingDefinition, UIBuilder
├── provider.go       # ChannelProvider interface
├── callbacks.go      # RunnerCallbacks, RegistryCallbacks, LLMCallbacks
├── setting_keys.go   # Setting key constants, CLIRuntimeSettingKeys
├── setting_helpers.go
├── card_converter.go # ConvertFeishuCard (shared by CLI + Web)
├── mock.go           # MockChannel for testing
├── ws_base.go        # WSChannelBase (shared by QQ + NapCat)
├── i18n.go           # Internationalization: zh/en UI strings (~1390 lines)
├── channel_cli.go    # ChannelCliChannel: WS bridge for remote CLI
├── cli_msg_builder.go # CliMsg: message builder (shared by CLI + Web)

channel/cli/          # CLI BubbleTea TUI (~44k lines)
channel/feishu/       # Feishu webhook + settings UI
channel/web/          # REST + SSE Web server, WebSocket RemoteCLIChannel, auth
channel/qq/           # QQ bot (WebSocket)
channel/napcat/       # NapCat HTTP API
```

## Files

| File | Purpose |
|------|---------|
| `channel.go` | Channel interface: Name/Start/Stop/Send |
| `dispatcher.go` | Outbound message routing to channels |
| `cli.go` | CLI channel entry: BubbleTea init, channel lifecycle, asyncCh drain |
| `cli_message.go` | Message rendering, streaming, tool call display, iteration snapshot (~1996 lines) |
| `cli_panel.go` | Input panels, tool status, sidebar (~2991 lines) |
| `cli_view.go` | Message list layout, markdown rendering, title bar (~1030 lines) |
| `cli_model.go` | BubbleTea Model: Update/View loop (~960 lines) |
| `cli_debug.go` | Debug mode: UI capture, key injection socket, auto-input (`--debug-input`) |
| `cli_theme.go` | Lipgloss styles, color schemes, glamour config (~711 lines) |
| `cli_types.go` | Type definitions, glamour renderer constructor (~712 lines) |
| `cli_runner.go` | Runner integration, process management |
| `cli_approval.go` | Tool execution confirmation dialog |
| `cli_palette.go` | Command palette (Ctrl+K): fuzzy-search, category tabs, external contributors (~531 lines) |
| `feishu.go` | Feishu webhook, message send, card messages (~3154 lines) |
| `feishu_settings.go` | Feishu settings UI (~2189 lines) |
| `web.go` | WebChannel core, route registration, HTTP lifecycle, security middleware |
| `web_socket.go` | WebSocket handler and read/write pumps; retained for remote CLI transport |
| `web_sse.go` | Cookie-authenticated `/api/sse` transport, event replay, 15s heartbeat, SSE framing |
| `web_types.go` | Web channel configuration, callbacks, and API response types |
| `web_outbound.go` | WebChannel outbound message stamping and Hub delivery |
| `web_static.go` | Frontend static-file and SPA fallback handler |
| `web_hub.go` | Shared WS/SSE connection routing, Client struct, offline ring buffer, stateless message slotting |
| `web_eventstream.go` | EventStream: seq-stamped ring buffer for replay/dedup (~99 lines) |
| `web_remote_cli.go` | RemoteCLIChannel: virtual CLI channel for CLI→WS→server mode (~270 lines) |
| `web_api.go` | REST API endpoints (~1901 lines) |
| `web_auth.go` | OAuth/token auth (~670 lines) |
| `web_fs.go` | Filesystem REST API (`/api/fs/list`, `/read`, `/search`, `/stat`); single-level `os.ReadDir`, path-traversal guard, 2MB read cap, language-from-extension map (~511 lines) |
| `qq.go` | QQ bot API (~1736 lines) |
| `napcat.go` | NapCat HTTP API (~821 lines) |
| `i18n.go` | Internationalization: zh/en UI strings (~1390 lines) |
| `mermaid.go` | Mermaid → ASCII chart rendering |

## Capabilities

Optional channel capabilities via interfaces in `capability.go`:
- `SettingsCapability` — channel supports user settings UI
- `UIBuilder` — channel can render custom UI elements

## Web Context Usage

The Web context ring reads the authoritative session snapshot from the
`get_context_usage` RPC using `(channel, chat_id)`. The response combines the
current `(subscription, model)`, its effective `max_context_tokens`, and the
latest provider-confirmed `prompt_tokens`; the browser must not rebuild this
state by joining subscription APIs or by falling back to a hard-coded limit.

- `prompt_tokens` is the context fill value. Completion tokens are informational
  and are never added to the usage percentage.
- Usage is exact at LLM-response and compression checkpoints. Active streaming
  keeps the last confirmed snapshot until the provider returns another usage
  record; Web does not estimate token growth locally.
- A session with no confirmed usage, or one whose model was just changed,
  returns `available=false` and `usage_percent=null` until the new model responds.
- The server does not clamp `usage_percent`. The ring may cap its drawing at
  100%, but its tooltip must retain the real percentage when usage is over the
  configured context window.
- Web refreshes the snapshot on session/reconnection lifecycle changes, model
  switches, history compression or reset, completed turns, and exact prompt
  token changes from structured progress. The snapshot lives outside the
  progress store so terminal progress cleanup cannot reset an idle ring to 0%.
- Web chat creation persists the canonical user ID to `user_chats.user_id`, and
  tenant creation/binding copies it to `tenants.owner_user_id` while lazily
  repairing legacy zero-owner rows. Context RPC authorization still accepts a
  matching Web `user_chats.sender_id` for pre-canonical data, but never applies
  that fallback across channels.

## CLI Conventions

- Settings save is synchronous (`doSaveSettings` in `cli_helpers.go`) — all local I/O
- Remote CLI settings RPC must use business sender identity (for example `cli_user`) rather than WS auth user (`admin`)
- Server-side `get_settings`/`set_setting` accept payload `sender_id`; for first-time non-admin users with empty settings, they seed a small user-scoped whitelist from global CLI config (`context_mode`, `max_iterations`, `max_concurrency`, `max_context_tokens`, `enable_auto_compress`, `theme`)
- CLI TUI now centralizes user-scoped setting keys in `channel/cli_helpers.go` and uses shared merge/persist helpers instead of duplicating per-call switch lists; current user-scoped keys: `theme`, `language`, `context_mode`, `max_iterations`, `max_concurrency`, `max_context_tokens`, `enable_auto_compress`, `runner_server`, `runner_token`, `runner_workspace`
- `AskUser` tool works via CLI channel's interactive input panel
- ApprovalHook handler injected after program creation (`cli.go:139`)

### CLI Debug Infrastructure (`--debug`)

- `--debug` enables Unix socket for key injection + periodic UI capture (2000-line ring buffer)
- `--debug-input "seq"` auto-injects key sequences after 2s splash delay (e.g., `"esc,sleep:1,hello,enter,ctrl+c"`)
- `--debug-capture-ms N` controls capture interval (default 1000ms)
- **`parseKeyInput` must NOT set `Text` field when modifier is present.** `Key.String()` returns `Text` if non-empty, bypassing `Keystroke()` — so `{Code:'c', Text:"c", Mod:ModCtrl}.String()` returns `"c"` not `"ctrl+c"`, breaking cancel detection.

### CLI asyncCh Pattern (Remote Mode)

- `asyncCh` (buffered-64) is the **sole intermediary** for all non-startup `program.Send()` calls
- `handleAsyncDrain` goroutine is the only `program.Send()` caller (prevents keyboard readLoop starvation)
- All progress, outbound messages, SetProcessing, SendToast, InjectUserMessage route through `asyncCh`
- `progressCh` (buffered-1) drains into `asyncCh` via `handleProgressDrain`

### CLI Iteration Snapshots (Tool Summary)

- Iteration snapshots track reasoning, thinking, tools, and wall-clock time per iteration
- **Iteration-advance progress push must carry completed history**: before sending a structured event that advances current from C to D, record C into `iterationHistories` and attach `IterationHistory` to that same outgoing payload. The TUI must never observe `current=D` while completed history still lacks C; otherwise C's reasoning/content/tool block briefly disappears until the next tick pull.
- **Progress history must stay flat**: `lastProgressSnapshot` and every `iterationHistories` entry must have `IterationHistory=nil`. Only outgoing RPC/push payloads may carry a flat `IterationHistory` copy. Storing payloads with nested `IterationHistory` causes exponential history growth and can OOM during reconnect/progress restore.
- **Sparse same-iteration snapshots preserve generating tools**: `StreamingTools` is stream-only like `StreamContent`. When a same-iteration structured snapshot has no `StreamingTools`, `ActiveTools`, or `CompletedTools`, carry forward previous `StreamingTools` so an ultra-fast generating→done tool does not vanish for one frame. Once any structured tool state arrives, it replaces generating state.
- **Deduplication**: when `PhaseDone` and `handleAgentMessage` both snapshot the same iteration, prefer PhaseDone version (has complete reasoning from server)
- `ElapsedWall` must be set in ALL snapshot creation paths (iteration change, PhaseDone, handleAgentMessage) — missing it causes fallback to sum only last iteration's tool.Elapsed
- Title bar shows `[host:port]` in remote mode (parsed from `RemoteBackend.ServerURL()`)

### CLI SubAgent Session Viewing (Remote Mode)

When viewing an interactive SubAgent session, the CLI switches to an "agent session view":
- `m.activeAgentSession` tracks the current agent session key (`channel:chatID/roleName:instance`)
- Messages are loaded via `handleSuHistoryLoad` which calls `get_history` RPC
- Outbound messages from the SubAgent are routed to the parent's chatID — CLI detects and filters
- **`get_active_progress` RPC bypasses bizID check for agent channel** (`p.Channel != "agent"`)
- **Tick chain must not break** — `tickCmd()` injection should be unconditional in multiple code paths to prevent chain breakage during session switches
- **`handleSuHistoryLoad` default case (PhaseDone)**: triggers `DynamicHistoryLoader` reload to pick up the final assistant reply
- **Viewport dirty-check fallback**: tick handler checks `!m.renderCacheValid` when `busy=false` to ensure viewport refreshes after session switch
- **`removeAllToolSummaries()`** must be called in all progress restore paths to prevent duplicate tool summaries

### CLI Context Bar Rendering

The context bar (top border of input box) replaces the default lipgloss border with a token usage progress bar via `renderContextTopBorder()` in `cli_view.go`.

**Rendering rules:**
- Returns `""` (plain border) only when `cachedMaxContextTokens <= 0` — meaning the token budget is unknown
- Once `cachedMaxContextTokens > 0`, the bar ALWAYS renders: filled when `lastTokenUsage` has data, empty (0%) when nil
- `lastTokenUsage` is only cleared by explicit delete RPCs (`/clear`, `/cancel`, session reset); a zero prompt count during normal operation just means no LLM call has completed yet

**Token state restoration:**
- **Startup**: `TokenStateLoader` (in `cli.go:Start()`) restores `lastTokenUsage` from DB
- **Active turn restore**: `handleSuHistoryLoad` → `acceptProgress` branch → `cacheTokenUsage(activeProgress.TokenUsage)`
- **Idle session switch**: `handleSuHistoryLoad` → `default` branch now falls back to `suHistoryLoadMsg.tokenPrompt`/`tokenCompletion` (fetched via `GetTokenStateFn` in `suLoadHistoryCmd`)
- **Session save/restore**: `saveCurrentSession()` / `restoreSession()` persist `lastTokenUsage` in `sessionState` across switches

**`cliSettingsSavedMsg.syncOnly`:**
- `SyncLayoutSettings` (called every 5s in remote mode) sets `syncOnly: true`
- `handleSettingsSavedMsg` skips context cache reset when `syncOnly` is true
- Without this flag, the context bar flashes to solid line every 5s in remote mode

### CLI Progress Panel Rendering

- **`toolLine(icon, label, elapsedStyled, maxWidth)`** helper in `cli_message.go` — unified tool line formatting using `lipgloss.Width()` for precision. All tool rendering sites (historical, completed, active) use this helper. Previous code used `len()` (byte count) and magic number overhead constants (`7 + ...`) which broke on styled/unicode content.
- **Typewriter cursor overflow**: when reasoning/stream content cursor `▋` would exceed `innerWidth`, it renders on a separate line. When cursor is hidden (blink off), a guide-only placeholder line maintains stable height. Both reasoning guide and thinking guide sites use this pattern.
- **SubAgent tree**: description is skipped when `descW <= 0` (no room); old code forced `descW >= 10` minimum which caused overflow on narrow terminals.

### CLI Tool Body / Diff Rendering

- Tool progress carries both `Summary` (short label) and `Detail` (bounded full output) plus raw `Args`; CLI renderers use `Detail`/`Args` for per-tool bodies.
- `Read` output from the tool already contains `line\tcontent`; CLI parses those line numbers, highlights only pure code with Chroma, then renders its own line-number column.
- `FileCreate`/`FileReplace` include unified diff metadata; engine turns it into built-in `ToolHints` when no plugin hint is present. External `file-diff` plugin remains compatible but is no longer required.
- Diff/code background fills must not depend on ordinary trailing spaces: terminal/viewport layers can drop or not paint them. Use NBSP padding (`\u00a0`) with the desired background (see `padBgRight`/`renderBgLine`) for selectable, painted blank cells.
- Any highlighted/styled content must be measured/truncated with ANSI-aware helpers (`lipgloss.Width`, `ansi.Truncate`), never `len()`/`[]rune` on strings containing ANSI escapes.
- Tool hints render without the `│` guide prefix. Always pass the actual available container width into hint/body rendering; if a guide prefix is prepended for non-hint bodies, subtract `lipgloss.Width(guide)` first to prevent viewport hard-wrap.

### CLI Sidebar Layout

- **Sidebar is NOT a separate component** — it's part of `cliModel.View()` layout logic. To show/hide: `Ctrl+B` toggles `m.sidebarVisible`, `m.isWide()` checks `width >= 120`. Both feed into `m.sidebarShown()` helper.
- **Layout**: `sidebar + middleBlock` horizontal join. `middleBlock = viewport + status + [todo] + footer + input + infoBar`. Sidebar height equals middleBlock height.
- **`sidebarShown()` helper** (`cli_view.go:38`): `m.isWide() && m.sidebarEnabled && m.sidebarVisible`. Use this instead of 4 inline copies of the condition. The 4 sites: `chatWidth()`, `layoutMain()` showSidebar, `layoutViewportHeight()` todo lines exclusion, `trackMainLayoutZones()` todo bar skip.
- **Sidebar sections**: Sessions (always), Todo (when items exist), Tasks (when bgTaskCount > 0 or agentCount > 0). Sections stack vertically, separated by blank lines.
- **Sidebar bg task list**: `renderSidebarActive(w)` lists individual bg tasks (command name, clickable). Clicking a task opens the bgtasks panel directly in log-viewing mode with follow-tail enabled. Navigator stack is pushed with `mode: ""` so ESC returns to main view (skips task list). Zone tracking uses `sidebarActiveSectionOffset` + `sidebarBgTaskLines` globals.
- **Sidebar rendering pattern**: single lipgloss style per line + manual truncation (`truncateToWidth`) + padding to fill width (`lipgloss.Width`). Do NOT use separate styles for icon vs text on the same line — ANSI boundary causes wrapping artifacts in narrow (~26-char) sidebar content area. Follow `renderSidebarSessions` as the reference pattern.
- **Sidebar width**: `m.sidebarWidth` (default 30), persisted via `sidebar_width` layout key (not in `config.Config` struct — use `saveLayoutToConfig()` for persistence).
- **Session busy/idle indicators**: sidebar renders different icons for busy vs idle sessions in `renderSidebarSessions`. Current session uses `m.typing`, agent sessions use `entry.Running`, other main sessions use `entry.Busy`. Icons: active+busy → `◉` (Accent color), active+idle → `●` (Accent), inactive+busy → `◎` (Warning/SidebarBusy style), inactive+idle → `○` (TextPrimary). `SidebarBusy` style defined in `cli_theme.go` (Warning color, Bold). CJK width note: `◉`/`◎` same width as `●`/`○` — layout stays stable.
- **Sessions Panel busy indicators**: `viewSessionsList` in `cli_panel.go` likewise differentiates. Main sessions show `◉`+`⏳` when busy, agent sessions show `⏳` suffix when `Running`. Busy determination mirrors sidebar: current session → `m.typing`, agents → `entry.Running`, others → `entry.Busy`.
- **`Busy` field data flow**: populated in `SessionPanelEntry` via `SessionsList` callback (`cmd/xbot-cli/main.go`). For main sessions: `app.backend.IsProcessing("cli", chatID)` (works both local and remote). For agents: `entry.Busy = entry.Running`. Remote mode refreshes every 5s via `refreshAgentCache`.

### CLI TODO Rendering

- **Two rendering sites, one helper**: `renderSidebarTodo(w int)` for sidebar view, `renderTodoBar()` for main view. Which site renders depends on `m.sidebarShown()`.
- **Main view**: rendered in `layoutMain()` as part of `middleLines` (between status and footer) when `!showSidebar`. Uses `TodoFilled`/`TodoEmpty`/`TodoDone`/`TodoLabel`/`TodoPending` styles.
- **Sidebar view**: rendered by `renderSidebarTodo(contentW)` in `renderSidebarForBlock()` when `len(m.todos) > 0`. Compact format: header `Todo N/M ██░░░░░░░░`, items `  ○ text…` with single style per line and manual width padding.
- **Viewport height**: `layoutViewportHeight()` excludes todo lines from `reservedLines` when `m.sidebarShown()` — viewport expands to fill the space.
- **Mouse zones**: `trackMainLayoutZones()` skips todo bar zone when `showSidebar` — no dead zone in main view.
- **Data lifecycle**: `syncProgressTodos` populates `m.todos` from progress events AND persists to `cliModel.todoManager`. `endAgentTurn` restores unfinished todos from TodoManager on turn end. `restoreSession` restores from disk (`LoadFromFile`) on session switch. `saveCurrentSession` persists current todos to disk (`SaveToFile`).

### CLI Remote TODO Sync (`get_todos` RPC)

- **Problem**: On remote TUI startup, the first session switch loaded TODO from local disk cache (`TodoManager.LoadFromFile`). If the local disk was empty or stale (different terminal, server restart, etc.), todos would be missing until the next active turn.
- **Solution**: New `get_todos` RPC (`MethodGetTodos = "get_todos"`):
  - **Server side**: `local_transport.go` handler reads from `Agent.todoManager.GetTodos(sessionKey)` and returns `[]CLITodoItem`
  - **Client side**: `suLoadHistoryCmd` calls `GetTodosFn(channel, chatID)` concurrently with history + progress, populates `suHistoryLoadMsg.todos`
  - **Application**: `handleSuHistoryLoad` default (idle) branch overwrites `m.todos` + `persistTodosToManager()` with server data. Non-nil empty slice means "server has no todos" → clears local cache too
- **RPC registration** (8 files): `req_types.go` (constant + struct) → `backend.go` (interface) → `backend_impl.go` (method) → `local_transport.go` (handler) → `rpc_table.go` (route) → `cli_types.go` (callback) → `main.go` (wiring) → test stubs
- **Adding new RPC methods**: add a method to `*Client` in `agent/client.go`, and handle the method in `serverapp/rpc_table.go`. For tests, update `fakeTransport` in `cmd/xbot-cli/main_test.go` to handle the new method in its `Call` switch.
