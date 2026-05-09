# channel/ — Channel Adapters

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
| `web.go` | HTTP server, WebSocket (~1957 lines) |
| `web_api.go` | REST API endpoints (~1569 lines) |
| `web_auth.go` | OAuth/token auth (~670 lines) |
| `qq.go` | QQ bot API (~1736 lines) |
| `napcat.go` | NapCat HTTP API (~821 lines) |
| `i18n.go` | Internationalization: zh/en UI strings (~1390 lines) |
| `mermaid.go` | Mermaid → ASCII chart rendering |

## Capabilities

Optional channel capabilities via interfaces in `capability.go`:
- `SettingsCapability` — channel supports user settings UI
- `UIBuilder` — channel can render custom UI elements

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
- **Sidebar sections**: Sessions (always), Todo (when items exist), Active tasks (when bgTaskCount > 0 or agentCount > 0). Sections stack vertically, separated by blank lines.
- **Sidebar rendering pattern**: single lipgloss style per line + manual truncation (`truncateToWidth`) + padding to fill width (`lipgloss.Width`). Do NOT use separate styles for icon vs text on the same line — ANSI boundary causes wrapping artifacts in narrow (~26-char) sidebar content area. Follow `renderSidebarSessions` as the reference pattern.
- **Sidebar width**: `m.sidebarWidth` (default 30), persisted via `sidebar_width` layout key (not in `config.Config` struct — use `saveLayoutToConfig()` for persistence).

### CLI TODO Rendering

- **Two rendering sites, one helper**: `renderSidebarTodo(w int)` for sidebar view, `renderTodoBar()` for main view. Which site renders depends on `m.sidebarShown()`.
- **Main view**: rendered in `layoutMain()` as part of `middleLines` (between status and footer) when `!showSidebar`. Uses `TodoFilled`/`TodoEmpty`/`TodoDone`/`TodoLabel`/`TodoPending` styles.
- **Sidebar view**: rendered by `renderSidebarTodo(contentW)` in `renderSidebarForBlock()` when `len(m.todos) > 0`. Compact format: header `Todo N/M ██░░░░░░░░`, items `  ○ text…` with single style per line and manual width padding.
- **Viewport height**: `layoutViewportHeight()` excludes todo lines from `reservedLines` when `m.sidebarShown()` — viewport expands to fill the space.
- **Mouse zones**: `trackMainLayoutZones()` skips todo bar zone when `showSidebar` — no dead zone in main view.
- **Data lifecycle**: `syncProgressTodos` populates `m.todos` from progress events AND persists to `cliModel.todoManager`. `endAgentTurn` restores unfinished todos from TodoManager on turn end. `restoreSession` restores from disk (`LoadFromFile`) on session switch. `saveCurrentSession` persists current todos to disk (`SaveToFile`).
