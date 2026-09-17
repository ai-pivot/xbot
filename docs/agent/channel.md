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

### Text-based progress (PreReplyNotifier channels)

Channels without structured display (Feishu streams the turn into a CardKit
- **2026-09-16：飞书进度渲染改为飞书原生 CoT（思考过程），对齐 dsh-lark。** 契约与实现见 AGENTS.md 同名条目：`POST/PUT /open-apis/im/v1/message_cot`；AG-UI 事件族（RUN_STARTED / REASONING_MESSAGE_* / TOOL_CALL_* / TOOL_CALL_RESULT(code) / RUN_FINISHED）；工具图标词表 read/write/search/bash；事件 ≤50/次、content ≤4096 字符、timestamp 严格递增；**答案仍走普通消息**。开关 `channels.feishu.output`（默认 `cot`），CoT 失败自动降级到本文档描述的 CardKit 卡片；实现 `channel/feishu/feishu_cot.go` + `feishu_cot_renderer.go`，测试 `feishu_cot_test.go`。

card, QQ sends progress as separate messages) implement
`channel.PreReplyNotifier` and receive per-iteration progress as **text lines**
via `RunConfig.ProgressNotifier` → `a.sendMessage`. This must be keyed by
**channel capability** (`wantsPreReplyNotify`, i.e. `autoNotify` passed into
`buildMainRunConfig`), **never** by `cfg.ProgressEventHandler == nil` — every
channel now has a ProgressEventHandler (needed for `/su` viewing + PhaseDone),
so that old gate silently disabled text progress for ALL channels. CLI/Web
(ProgressSender, structured) have `autoNotify=false` → notifier is a no-op,
keeping their message stream free of progress text artifacts.

#### Feishu CardKit streaming card

The card is driven by the **structured** progress stream: the Feishu channel
implements `channel.ProgressSender` (`SendProgress` / `SendStreamContent`), the
same broadcast web and CLI consume. Flat progress text is NOT parsed any more —
it carries neither the thinking text nor iteration boundaries. Consequently
`PreReplyNotify()` returns **false** (no ack, no text progress; those would
double-render).

`channel/feishu/feishu_stream_card.go` renders ONE CardKit card entity per turn,
laid out like the Web UI's `IterationGroup` — **per iteration: thinking → answer
→ tools**, with **no header** (explicit user request 2026-09-13):

```
iteration N   💭 思考 N 字        collapsible_panel (collapsed; chevron icon)
              answer text        markdown (the CURRENT iteration owns the
                                 streamable element_id="content")
              ✅ Shell · ls -la · 12ms · 完成    one row per tool
iteration N+1 …
```

- Only the `content` element can be streamed (typewriter); finished iterations
  are re-laid-out by a throttled full-card `Card.Update`.
- Tool rows read `✅ **Shell** · ls -la · 12ms · <font color='green'>完成</font>`:
  the detail is the first line of `Summary` when finished, otherwise the most
  telling field of the raw `Args` JSON (`command`/`path`/`pattern`/`query`/…,
  see `streamCardArgKeys`) — rune-safe truncated.
- Lifecycle: create entity (`streaming_mode=true`) → send
  `{type:card,data:{card_id}}` → `cardElement.Content` → full-card `Card.Update`
  on structural change → finalize on `channel.MetaFinalReply`
  (`handleRunOutput` / empty-content warning / `handleCancelledRun`).

Gotchas:

- `finalize()` MUST always run (and runs even when the full-card update fails):
  an open stream leaves the card stuck on "生成中" until Feishu force-closes it
  after 10 minutes.
- `update_multi` MUST stay `true` — the content API rejects exclusive cards.
- A card entity can be sent exactly once and only by the app that created it →
  the app needs `cardkit:card:write`. On create failure the channel latches
  `streamCardBroken` (one warning, then the legacy static-card path) instead of
  retrying every tick.
- A fully-built card (`__FEISHU_CARD__:`) or a WaitingUser AskUser card
  supersedes the streaming card → finalize + delete before sending it.

**Removed legacy (do not resurrect):** `channel/capability.go`'s
`ProgressUI.BuildProgressUI` (and the Feishu implementation) — dead code, nobody
called it; the `channel.MetaProgressCard` metadata key (and its stamping in
`sendAck` / `ProgressNotifier` / the SubAgent notifier); the
`> ⏳/✅/❌/⚠️/📦/🎭` timeline text parsing (`splitStreamCardText` /
`parseTimelineStep`).

Gotchas:

- `finalize()` MUST always run (and runs even when the full-card update fails):
  an open stream leaves the card stuck on "生成中" until Feishu force-closes it
  after 10 minutes.
- The final reply text is the model's **answer**, not the accumulated progress
  log → it carries no trace lines. `finalize` therefore KEEPS the timeline
  collected during the turn instead of overwriting it with an empty one.
- `update_multi` MUST stay `true` — the content API rejects exclusive cards.
- Only the `content` element can be streamed; the collapsible panel is synced
  with full-card updates on its own throttle (`streamCardPanelMinInterval`).
- A card entity can be sent exactly once and only by the app that created it →
  the app needs `cardkit:card:write`. On create failure the channel latches
  `streamCardBroken` (one warning, then the legacy static-card path) instead of
  retrying every tick.
- The first send of a turn has no `update_message_id`; a card still open at that
  point belongs to a previous (e.g. cancelled) turn and is finalized first.
- A fully-built card (`__FEISHU_CARD__:`) or a WaitingUser AskUser card
  supersedes the streaming card → finalize + delete before sending it.

#### Provisioning the permission: `xbot-cli feishu-bind` / 设置 → 渠道

`cardkit:card:write` (创建与更新卡片) is part of the Feishu **agent app** preset.
An app created before that preset existed will NOT have it, and the streaming
card then silently degrades to the legacy static card (one WARN in the log).

Two entry points share `internal/feishuapp` (the device authorization flow
`registration.RegisterApp`, RFC 8628 — the same flow as the official "create a
Feishu agent app in one click" docs), so the scope/event/callback preset lives in
exactly one place:

**CLI** (`cmd/xbot-cli/feishu_bind.go`):

```
xbot-cli feishu-bind                    # create a NEW agent app
xbot-cli feishu-bind --app-id cli_xxx   # bind/upgrade an EXISTING app (增量叠加)
xbot-cli feishu-bind --create-only      # only allow creating
xbot-cli feishu-bind --no-save          # print credentials without writing config
```

**Web** — 设置 → 渠道 (`SettingsChannels.tsx`) → the Feishu card's
「一键绑定飞书智能体应用」 button:

- `feishu_bind_start` (serverapp/feishu_bind.go) runs the flow and returns the
  launcher link synchronously; `feishu_bind_status` is polled until
  `done`/`error`.
- The server owns ONE attempt at a time (`feishuBinder`): the link is single-use,
  so a new `Start` cancels the previous attempt.

Either way the returned credentials are written to `channels.feishu` in
config.json (`enabled=true`); **the server must be restarted** for the channel to
pick them up. `--app-id` / the panel's `app_id` field are pre-filled from
`channels.feishu.app_id` when empty.

#### Web 渠道面板（内置 + 插件渠道）

设置 → 渠道 renders EVERY channel from `get_channel_config`:

- built-ins (web/feishu) — schema from
  `channel.BuiltinChannelSchema` (`channel/channel_defs.go`, the single source of
  truth shared with the CLI settings panel),
- user-registered plugin channels — schema from `ChannelProvider.ConfigSchema()`.

Both arrive in the same shape (`_schema` JSON + `_builtin` flag), so one renderer
covers them; saving goes through `set_channel_config`, which writes config.json
and hot-starts/stops the channel through the dispatcher.

- **`AskUser` 事件必须送达**（web 端曾因 request-ID 校验静默吞掉事件 → 面板不渲染，用户手动回答污染历史）。规则：同一 (channel, chatID) **只有一个 pending AskUser**，所以 `Send`/SSE 写循环**只按 pending 存在性**判断（存在→发布/发送，清除→跳过/consumed），**绝不做 request-ID 相等校验**；`WithPendingAskUser` 仅用于补全 pending 快照，返回值不 veto 发送。已回答/取消的 prompt 由生产者跳过（Send 不重发）+ SSE consumed（reconnect 不重放）。回归测试：`TestSSEAskUser_PendingExistsSends` / `TestSSEAskUser_PendingMissingConsumed`。
- **`ask_user_resolved`（pending 解除失效广播）**：pending 停止待答（answered/cancelled/rewound/cleared）时后端发 `AskUserResolvedEvent`；Web 侧 `WebChannel.SendAskUserResolved`（实现 `channel.AskUserResolvedSender`，与 SessionStateSender 同模式）经 hub 按 `(channel, chatID)` 路由广播给该会话**全部 WS/SSE 客户端**（序列化 + 离线缓冲 + 可重放，`isSSEEventType` 已纳入）。信封为**扁平字段**（`channel`/`chat_id`/`request_id`/`reason`——前端直接读 `msg.reason`/`msg.chat_id`，与 `web/src/types/shared.ts` 的 WSMessage 契约一致）。与 `ask_user` 相反：**不做 pending 门、可重复安全投递**（客户端按 request_id 幂等），SSE 写循环也不把 resolved 当 consumed。**重连对账**：WS `enqueuePendingAskUser` / SSE `publishSSEFallbacks` 在无 pending 时补发 `resolved(reason="cleared")` —— 带着陈旧本地缓存（另一标签页/设备已答）的客户端立刻收起面板；有 pending 时只补发 `ask_user`、不发 resolved（不误伤在挂面板）。REST 取消（`/api/cancel`、`POST /api/ask_user/respond cancelled=true`）与 WS 路径一致携带 `ask_user_cancel` 标记（防陈旧取消污染下一条消息）。回归测试：`TestAskUserResolvedBroadcastReachesEveryClientOfSession` / `TestWSReconnectWithoutPendingPushesAskUserResolved` / `TestSSEConnectWithoutPendingPublishesAskUserResolved` / `TestRESTCancelCarriesAskUserCancelMarker` / `TestAskUserResolvedPassesSSEDeliveryWithoutPending`。
- **`AskUser` 历史记录**：`ask_question`/`ask_answer` 以 control record（role=control, display_only=1）追加（`AppendAskAnswer`），不参与 LLM 上下文与正常消息渲染；回答（`ask_user_answered`）**两条路径**：(a) **替换 AskUser tool 消息内容为回答**（让本轮 LLM 上下文包含回答——否则模型只看到 "Asked N question(s)" 以为用户没答）；(b) **持久化为正常 user 消息**（绑定本 turn 的 turn_id，非 display_only——Replay 排除 display_only 行，前端拿不到会导致顺序破坏）。**回答 user 消息是回答后迭代的 turn 锚点**——没有它，appendAssistant 的 insertBeforeLastUser 回退到原始 user 消息，把回答后的新迭代渲染到旧迭代上方（顺序破坏）。

- **Web 无乐观渲染（确定性原则）**：前端**禁止任何乐观渲染**——用户消息（含 AskUser 回答）只由**后端 `user_echo` 推送**渲染（`web_inbound.go dispatchUserMessage`：每条被接受的 user 消息回显，**含权威 turn_id**）；`sendMessage` 不插乐观行/不绑 turn_id/无 queued 标记；`bindLastUserToTurn` 已删除；`reconcileHistoryWithLiveRows` 保留 history 竞态未覆盖的 persisted user-echo 行（确定性数据不丢）；AskUser 回答后 AgentPanel 触发 `chat.reload()`（回答 user 消息从后端历史加载）。**迭代号（iter id）也由后端下发**：`ProgressEvent.Iteration` + 历史 `HistoryIteration.Iteration`——前端不做任何迭代号推测。**所有数据确定性、决定性**：turn_id/iter_id 由后端生成并在历史/事件/echo 中返回。
- **`reconcileHistoryWithLiveRows` must dedup by `turnID:role`, not `eventSeq`/`content`**：cancel 后 `appendAssistant` 创建 live 消息（`seq-N`，`turnID=3`，`persisted=false`）；`markDestructiveMutation` → 下次 reload 走 `reconcileHistoryWithLiveRows`。DB 返回 `[interrupted]`（`hist-N`，`turnID=3`，`content=""`）。修复：从 history 构建 `Set<turnID:role>`，live 消息的 `turnID:role` 已在集合中则丢弃；`turnID=0` 消息（user_echo）用 `content:role` 兜底。live（persisted=false）行在 `eventSeq < watermark` 时被 history 覆盖，但**高于 watermark 的 live 行**（reload 后 SSE 新到数据）保留。**Reload 主路径 ALWAYS reconcile**（`markDestructiveMutation` 在 reload 前递增 gen 导致 `mutated=false`，直接 parsed 替换会丢 persisted user_echo 行）。**persisted USER 行保护**：带 eventSeq 的 echo 走 watermark 判断；无 eventSeq 的 echo（web_inbound.go 推送）仅当 `turnID > 快照最新 user turn` 时保留（竞态 reload 期间当前 turn 消息不消失，同/更早 turn 被 DB 覆盖）。persisted assistant 行由 DB 版本权威。

- **`ConvertMessagesToHistoryWithIterations` 必须在 turn 边界 flush pending 迭代**：重启打断的 turn 没有最终 assistant 消息（全是 tool_calls 中间消息），重启恢复的 turn 用**新 turn_id** 继续 → 两个 turn 的中间消息**背靠背、无 user 分隔**。旧代码把所有中间消息积累进一个 `pendingIters`（`pendingTurnID`=最后 turn），`flushPending` 用**最后 turn** 的 `turnIterMap` 记录替换整个缓冲 → 前一个 turn 的迭代从 `get_history` 静默消失（数据一直在 `iteration_history` 表，仅渲染层丢失；复现：fancy memory tenant 134262 turn 219 13 条 ih 全在，但重启后只渲染 turn 220 的 2 条）。修复：`len(m.ToolCalls) > 0` 分支先 `if pendingTurnID > 0 && m.TurnID != pendingTurnID { flushPending() }` 再积累；`flushPending` 重置 `curIterIdx=0`（fallback 迭代号每 turn 从 1 开始）。回归测试：`TestConvert_WithIterations_RestartTurnBoundary`。

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
├── i18n.go           # Internationalization: zh/en UI strings (~1390 lines)
├── channel_cli.go    # ChannelCliChannel: WS bridge for remote CLI
├── cli_msg_builder.go # CliMsg: message builder (shared by CLI + Web)

channel/cli/          # CLI BubbleTea TUI (~44k lines)
channel/feishu/       # Feishu webhook + settings UI
channel/web/          # REST + SSE Web server, WebSocket RemoteCLIChannel, auth
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
| `i18n.go` | Internationalization: zh/en UI strings (~1390 lines) |
| `mermaid.go` | Mermaid → ASCII chart rendering |

## Command metadata

Command execution remains split by scope: TUI-local commands are dispatched in
`channel/cli/cli_slash.go`, while Agent commands are dispatched by
`agent.CommandRegistry`. Their presentation metadata is unified:

- `protocol.CommandInfo` is the shared transport DTO.
- `channel.TUICommandList()` is the TUI-local metadata source.
- `CommandRegistry.CommandList()` is the Agent and plugin metadata source.
- RPC `list_commands` returns complete Agent metadata; legacy
  `list_command_names` remains available for older clients, and new clients
  fall back to it when connected to an older server.
- The CLI merges both lists and uses the result for `/help`, Tab completion,
  and catalog-only Ctrl+K entries. This merge never changes handler selection or
  registration-order match priority.

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
- Agent-channel history/progress access recursively validates the child session's
  real parent channel and chat ID; there is no `chat_id == bizID` authorization
  shortcut.
- **Tick chain must not break** — `tickCmd()` injection should be unconditional in multiple code paths to prevent chain breakage during session switches
- **`handleSuHistoryLoad` default case (PhaseDone)**: triggers `DynamicHistoryLoader` reload to pick up the final assistant reply
- **Viewport dirty-check fallback**: tick handler checks `!m.renderCacheValid` when `busy=false` to ensure viewport refreshes after session switch
- **`removeAllToolSummaries()`** must be called in all progress restore paths to prevent duplicate tool summaries

### Append-only History Display and Rewind

- `get_history` WS RPC and `/api/history` REST expose the same chronological
  projection: every persisted message row (including tool/tool-call rows) plus
  every compression marker, ordered by `history_id`. Internal controls
  (`context_edit`, `mask`, AskUser controls, `prune`) stay private.
- `compacted_by` and compression source IDs are relationship metadata only.
  Neither TUI nor Web hides source messages after compression.
- TUI renders compression markers with its existing summary style. Web renders
  each marker as an independent collapsible tool-like block whose body contains
  only that compression summary.
- Every persisted, non-display-only user message is a Rewind candidate, including
  messages before a compression boundary. Rewind requests contain exactly
  `channel`, `chat_id`, and `history_id`; timestamp and cutoff fallbacks do not
  exist.
- A matching `history_rewound` session event clears live progress and forces a
  history reload. Server WS/SSE state is keyed by explicit channel + chat ID;
  reset clears only the target route before broadcasting the barrier, so
  reconnect cannot restore deleted future events and same-ID sessions on other
  channels remain intact.
- Remote CLI subscriptions carry an explicit route and a route-scoped replay
  cursor. The server installs the subscription and replay suffix under one
  publication lock. If the retained ring cannot cover the cursor, it sends
  `resync_required`; both TUI and Web then reload the complete authoritative
  session snapshot, including active progress, TODOs, and pending AskUser.
- Web edit-and-rewind waits for REST success and a completed history reload before
  resending the edited text. File-checkpoint rollback errors are warnings after
  history commits; REST or reload failures retain the draft and do not send.
- TUI and Web keep rewind locked through the matching reset and history reload.
  Session generation guards prevent a late rewind/reload from clearing or
  resending into a newly selected session, and AskUser input is disabled while
  that destructive operation is pending.
- Agent child sessions load the same canonical history API. TUI/Web sending and
  post-rewind resend use `continue_interactive_session(full_key, content)` so an
  active interactive object is continued with its own Run config; stale/one-shot
  history is read-only. TUI invokes the blocking RPC through an asynchronous
  BubbleTea command so progress rendering remains responsive.

### CLI Context Bar Rendering

The context bar (top border of input box) replaces the default lipgloss border with a token usage progress bar via `renderContextTopBorder()` in `cli_view.go`.

**Rendering rules:**
- Returns `""` (plain border) only when `cachedMaxContextTokens <= 0` — meaning the token budget is unknown
- Once `cachedMaxContextTokens > 0`, the bar ALWAYS renders: filled when `lastTokenUsage` has data, empty (0%) when nil
- `lastTokenUsage` is cleared on conversation/session reset and session switching; `/clear` only clears rendered messages. A zero prompt count during normal operation just means no LLM call has completed yet

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

### Web Frontend SSE Recorder & Replay Tests

- **REC 按钮（开发者专用，默认隐藏）**：`DebugToolbar.tsx`（AgentPanel 顶部）+ `useSSERecorder.ts`。入口在 **Settings → 开发者** tab：开启"开发者工具"开关（`useDeveloperMode`，localStorage 持久化 + window CustomEvent 跨组件同步）后，AgentPanel 才渲染 REC 工具栏。点击 REC 用**独立** `ws.onMessage` handler 录制所有 WS/SSE 消息（`onMessage` 是注册式 API，返回 unsubscribe，不干扰 `useProgressStream`/`useChatMessages`）；STOP 下载 `sse-dump-{ts}.ev` **并打印完整 store 状态 JSON 到 console**（`[SSE_DUMP_STATE] {...}`，来自 `AgentPanel.progressSnapshot`）。**100% 复现工作流**：重放 .ev 事件到 ProgressStore → 对比重放快照 vs `[SSE_DUMP_STATE]` JSON → 第一个差异点就是破坏 store 的事件（turn 消失等）。开发者专属调试/导出功能（REC、Turn+Iter 导出、benchmark JSONL）都集中在 Settings → 开发者 tab，不暴露给普通用户。
- **录制格式与后端线格式一致**：`id:{顶层 WSMessage.seq}\nevent:{type}\ndata:{完整 JSON}\n\n`——顶层 `seq` 是**传输层** seq（后端 `writeSSEEvent` 用 `msg.Seq` 写 SSE `id:`），与 `progress.seq`（语义水印）是两套体系。**诊断 turn 消失/事件丢失时，传输层与语义层 seq 不可混用**（曾导致误判 gap）。
- **重放基础设施**：`src/test-utils/sseReplay.ts` 的 `parseSSEDump()` 解析录制文件为 `WSMessage[]`，`seq` 解析与 `handleEvent` 同构（`msg.seq ?? lastEventId`）——**录制文件可 1:1 重放进 `useProgressStream` 测试**。复现 bug → 下载 .ev → 用 `parseSSEDump` 重放写回归测试固定。
- **turn 消失回归测试**：`useProgressStream.test.ts` 的 "SSE dump replay" describe——重放"迭代边界清 streamContent → PhaseDone 无 text 事件"的流，断言 liveMessage 不消失 + `onIterationGap` 触发（reload 从 DB 恢复权威完整回复）。

### Web Frontend UI Mode（外壳模式：自动 / 桌面 / 移动端）

- **单一权威源 `web/src/hooks/useUIMode.ts`**：`UIMode = 'auto' | 'desktop' | 'mobile'`（命名对齐现有 `desktop.*` / `mobile.*` layout slot）。localStorage `xbot-ui-mode` 是读路径（首帧即生效，无异步闪烁），服务端 `user_settings` 的 `web:ui:ui-mode`（SETTING_MAP）负责跨设备同步；`useSyncExternalStore`（+ `storage` / `SETTINGS_SYNCED_EVENT` 监听）让同窗口多实例在设置面板切换后立即重渲染。`auto` 模式下视口跨断点由 `matchMedia(MOBILE_QUERY = '(max-width: 767px)')` 的 change 事件重解析。
- **`useIsMobile()` 派生自 `useUIMode().effective === 'mobile'`**（原实现自带 matchMedia，已删除）——外壳切换（`AppShell` 的 `if (isMobile) return <MobileAppShell />`）、`TerminalPanel`、LLM 控制台等所有布局分支共享同一判定，**不允许任何地方再各自 matchMedia**（否则强制模式只有外壳生效、布局分支仍按视口走）。
- **`useIsTouch()` 不受影响**：它是【设备能力】（`(hover: none) and (pointer: coarse)`）而非布局模式——强制手机外壳的桌面上 hover 依然可用，触屏上的 tooltip/popover 分流照常。
- **设置入口**：设置 → 外观 → UI 模式（`SettingsAppearance.tsx`，三个 `aria-pressed` 按钮 + 当前生效提示）。手机外壳里同样能打开该设置（`MobileAppShell` 的 `SettingsDialog`），因此强制 mobile 后不会被困住。
- **范围**：只切换外壳，不改 CSS 断点——窄屏强制桌面外壳会得到压缩的桌面布局（有意为之）。

### Web Frontend Session Tabs（session-per-tab：切会话 = 切 tab）

- **主编辑区 AgentPanel 的会话身份在【它自己 tab 的 `params.sessionId`】上，不在全局 `activeSession`。** `useTabManager.openTab({type:'agent', data:{filePath, channel}})` 把 `filePath` 写进 `PanelParams.sessionId`（tab 逻辑键 `agent:<channel>:<chatID>`，`tabLogicalKey` 保证同会话重复打开只聚焦不重复 tab，且 `openTabInternal` 会 `panel.api.setActive()`）。`AgentPanel` 的 `chatID = params.sessionId ?? activeSession?.chatID`（只有 seed tab / 手机端无 sessionId 时才回落 activeSession）。`DockviewContainer` 的 `onDidActivePanelChange` 在 agent tab 激活时反向 `activateSession(params.sessionId, params.channel)`（侧栏高亮跟随 tab）。
- **推论（踩坑点）：只调 `store.switchSession`/`activateSession` 不会切换主区** —— 侧栏高亮变了、主区当前 tab 仍绑旧 sessionId（用户报告："点侧栏『新建会话』，确认后侧栏新会话高亮了，但窗口没切过去，必须再点一下新会话"）。**任何"创建/派生出新会话并想切过去"的入口都必须同时打开/聚焦该会话的 tab**：`openAgentSessionTab(tabManager, chatID, channel, title)`（`web/src/lib/sessionTabs.ts`，唯一入口）。已接：desktop `core.sessions` 面板的新建会话（`builtinPanels.tsx`）、命令 `session.new`（`AppShell.tsx`）、fork（`builtinPanels` 的 `onFork` → openTab）、会话列表点击（`handleSelect` → openTab + `activateSession`）。
- **手机端（`MobileAppShell` / mobile AgentPanel 的 `mobilePanelProps` 无 sessionId）不能调它**：手机没有 dockview，`tabManager.openApi.openTab` 只会进 `pending` 队列静默丢失（`bindApi` 前）；手机端 AgentPanel 跟随 `activeSession`，因此「完成切换」= **关闭抽屉**（抽屉是覆盖层，不关就等于"窗口没切"）。侧栏容器 `SessionSidebar`（现仅手机抽屉消费）用 `onSubAgentSelect` 有无判别两态（与 `handleSelect` 同一判据）：有 → 手机（`onSessionSelected` 关抽屉，不开 tab）；无 → desktop（`openAgentSessionTab`）。
- 守护测试：`web/src/components/panel/builtinPanels.createSession.test.tsx`（创建成功 → `openTab` 带新 chatID；修复前红灯）+ `builtinPanels.fork.test.tsx` + `web/src/components/session/SessionSidebar.test.tsx`（mobile：关抽屉且不开 tab / desktop：开 tab）。

### Web Composer — `!cmd` Bang Commands（终端命令直通）

- **契约**：以 `!` 开头的消息由**后端** `agent/bang_command.go` 处理（`isBangCommand` + `bangCmd`，注册于 `command_builtin.go`，`Concurrent() == true`）——**跳过 LLM**，在 sandbox 里执行并把输出以对话消息返回（超过 16k 字符落盘成文件）。`![...]`（Markdown 图片 / 粘贴截图）与裸 `!` **不是**命令。
- **⚠️ REST 的 turn_id 豁免必须与分发共用同一判定**：命令消息由 chatWorker 并发处理（**不分配 turn_id**，也不发 `turn_started`），而 `POST /api/message` 对非排队用户消息要求 `turnID != 0`（fail-fast，防止前端把乐观 user 行绑到不存在的 turn）。豁免曾用 `/` 前缀启发式（`isSlashCommand`）→ **`!cmd` 被误判为普通用户消息** → 500 `internal error: message accepted without a turn_id`（日志：`handleMessage: turn_id is 0 for a user message — refusing to return an unbound user message`），前端乐观行卡在"发送中"、输出不渲染，用户表现为**"! 开头的命令没生效"**。修复：`WebCallbacks.MatchesCommand`（`serverapp/callbacks.go` 注入 `ag.Commands().Match(content) != nil`），`WebChannel.isCommandMessage` 优先用它、未接线时回落 slash 前缀。回归：`channel/web/web_rest_bang_command_test.go`（bang 200 且无 turn_id / 普通消息 turnID=0 仍失败 / slash 不回归）。
- **前端可发现性**：composer 草稿以 `!` 开头时显示 `agent.bangCommandHint`（`data-testid="bang-command-hint"`）。判定 `isBangDraft`（从 `MessageInput.tsx` 导出）**镜像后端 `isBangCommand` 规则**（`![` 不是命令、裸 `!` 不是命令），有单测；纯提示，消息原样发送。
- **⚠️ 本机（`none`）沙箱的工作区必须先创建**：`ensureWorkspace`（agent/agent.go）曾把 `none` 与 remote/docker 一起跳过 → 首次运行/新用户时 bang 的 exec `Dir`（`~/.xbot/users/<uid>/workspace`）不存在 → `execve` 失败并报**误导性**错误 `fork/exec /bin/bash: no such file or directory`（bash 明明存在）。守护：`agent/bang_command_test.go:TestEnsureWorkspace_LocalSandboxCreatesWorkspace`（测试必须给 `Agent.sandbox` 赋值，否则 `sandboxNameForUser` 返回 `""` 会绕过该分支、守卫失效）。

- **⚠️ 命令回复的前端渲染契约（`standalone` 段，2026-09-17 "发了没反应 / 消息消失" 三连根因）**：① 后端命令分发**不分配 turn_id**，因此 `text_final` 事件 `turnID === null`；M4 状态机原逻辑 `if (target === null) return s` 会**静默吞掉**输出 —— 修复为渲染进 `ChatState.standalone`（`derive.ts` 输出 `[...legacy, ...turns, ...standalone, ...pending]`，位置在 turns **之后 = 底部**）。`legacy` 是 DB 历史前缀（顶部）、`standalone` 是无 turn 的**实时**回复（底部），**语义不同不可混用**。② 命令消息**不得标 `persisted`**（命令不落库：`message_id=0` 且无 turn_id）——`useChatMessages` 判据必须是 `(message_id>0 || turn_id>0)`，否则它从底部乐观行变成顶部 legacy 行（"消息直接消失"）。③ 命令回复**绝不继承正在跑 turn 的 id**：`sendMessage` 见到 `metadata.command_reply`（`sendCommandReply`/`markCommandReply` 打标）不回落 `getActiveTurnID`；否则回复带上当前 turn 的 id → 被该 turn 的真回复覆盖（SSE 探针实证：修复前 `"turn_id":1`，修复后 0 次）。守护：`chat/reduce.test.ts`（2 REPRO）、`chat/commandReplyPipeline.test.ts`（端到端管线：SSE text 无 turn_id → normalize → reduce → deriveRows → `messages`）、`agent/command_reply_turn_test.go`。
- **⚠️ 追加行必须权威重测虚拟列表（同一轮用户报告的最后一环，纯前端布局）**：命令输出**在 DOM 里**（`data-message-id="cmd-1"`）、滚动容器**在底部**，但**看不见** —— CI 真实 Chromium 诊断：live 行 DOM 盒高 **8660px**，而虚拟器仍认为它是 **91px**（该行刚出现时的高度）→ 追加行按 91px 定位 → 两个绝对定位行重叠 **8569px**。机制：`resizeItem` 只从该 index 往后重算，而 RO 的 `entry.borderBoxSize` 是**观察时刻快照**，乱序/滞后 entry 会把真值覆盖回旧值且此后不再上报（永久固化）；`getMeasurements` 的 memo 又不依赖 `estimateSize`。**修复（`MessageList.tsx` 三层触发点，缺一不可）**：① **追加行时**（rows 增长、末行变化）`virtualizer.measure()` 清缓存 → `remeasureMountedRows()` 逐个已挂载 `.virt-row[data-index]` 读**当前真实几何** → **校正之后**再贴底（+ 一帧 settle 重测）；② **测量源改读当前几何**（`noDegenerateMeasureElement` 优先 `offsetHeight/offsetWidth`，不信过期 RO 快照）；③ **live 行内容版本变化即重测该行**（`useLayoutEffect([liveContentRev])`）—— CI 实证：`measurePass=4` 说明①②都跑了、读到的却仍是 91 ⇒ 内容是重测**之后**才长出来的，而行列表没变（①不跑）+ RO 静默（②无从触发）。诊断标记：`data-measure-pass` / `data-measure-heights` / `data-virt-total`。守护 `src/components/agent/measureOnAppend.test.tsx` + `virtualizer_stale_row.test.ts` + `e2e/standalone-command-layout.spec.ts`（真实 Chromium：`measure-pass>0` + 相邻行不得重叠 + 输出 bbox 必须在视口内）。

### Web Frontend Message Composer (tiptap)

- **Stack**: `MessageInput.tsx` — tiptap v3（StarterKit + 定制 Link + Placeholder + tiptap-markdown）。编辑器输出 markdown（`getMarkdown()`），下游 onSend 接口零变化。富文本状态（mark 结构）与 markdown 文本互转由 tiptap-markdown 承担。
- **Link 扩展必须定制（StarterKit 的默认值是文档编辑器语义，不是输入框语义）**：StarterKit v3 **内置 Link 扩展**（v2 没有）——`MessageInput` 用 `StarterKit.configure({ link: false })` 关掉，换 `EditorLink`（`Link.extend({ inclusive: () => false }).configure({...})`）。四个默认值是 bug 之源：`inclusive()` 默认 `= autolink`（true）→ 链接 mark 是 inclusive 的 → 在链接尾部打字新文字并入链接（"意料之外的文字变成超链接"根因）；`openOnClick: true` → 编辑时点链接新开标签页；`linkOnPaste: true` → 选中文字后粘贴 URL 把选中文字变成链接；`shouldAutoLink` 默认接受裸域名 → `file.tar.gz`（`.gz` 是合法 TLD）被链接化。定制值：`inclusive: false`（autolink 的 appendTransaction 重扫描机制不依赖 inclusive，只影响打字延伸）、`openOnClick/linkOnPaste: false`、`shouldAutoLink: /^(https?:\/\/|www\.)/i`（只认协议/www）、`markdownLinks: true`（打 `[text](url)` 实时成链）。
- **链接 CSS 必须显式写**：Tailwind preflight 把 `a` 重置为 `color: inherit; text-decoration: inherit` → `.xbot-editor` 内链接与普通文本像素级一致（"看不见超链接"根因）。`.ProseMirror.xbot-editor a` 样式在 `index.css`（accent 色 + 下划线 + hover 态 + `overflow-wrap: anywhere` 防长 URL 撑破）。
- **SelectionToolbar**（`web/src/components/agent/SelectionToolbar.tsx`）：选中文本浮现工具栏（B/I/S/code/链接）+ 链接编辑/解除。基于 `@tiptap/react/menus` 的 `BubbleMenu`（官方子路径，零新依赖；floating-ui flip/shift 视口钳制——手机安全）。自定义 `shouldShow` 全接管可见性（默认镜像 + `hiddenRef`（completion 弹窗优先）+ `linkModeRef`（URL 编辑态保持））。URL 输入 Enter 必须 `!e.nativeEvent.isComposing`（IME 保护）。`Ctrl/Cmd+K`（MessageInput handleKeyDown）触发链接编辑。
- **PM selectionchange 异步竞态（domObserver.flush）**：浏览器移动 caret（Home/End/方向键）后 PM 只在异步 `selectionchange` task 里同步 state.selection——同帧键击（Playwright 零延迟 `keyboard.press('Home')` 后立即 `Control+k`，或极快的真人键击）读到 stale selection。修复：Ctrl+K handler 里读 selection 前调 `ed.view.domObserver.flush()`（PM 在 mousedown 前 flush 的同款模式）。
- **粘贴/拖拽文件自动上传**：`editorProps.handlePaste`/`handleDrop` 拦截 `clipboardData.files`/`dataTransfer.files` → `onPickFilesRef`（ref 防闭包 stale）→ 复用 📎 附件上传链路。拖拽高亮用 `handleDOMEvents.dragenter/leave`（深度计数，子元素两者都触发）。
- **上传类型零限制（2026-09-05 用户指令，不许加回）**：`channel/web/web_file.go` 的 `isAllowedExtension` 白名单 + `isBlockedMIME` 黑名单已删除——**任何类型文件可上传**（.html/.exe/.php 等全部接受），仅保留 10MB 大小限制（`maxFileSize`，大小不是类型）。Web 上传仍走 OSS（本地存储禁止不变）。测试 `web_file_test.go`。
- **测试 gotchas**：vitest 里 PM 内部 handler 先于 editorProps 读取 clipboard——合成事件必须带 `getData: () => ''`（paste 路径 PM 先 `clipboardData.getData('text/plain')`）+ drop 测试需 polyfill `document.elementFromPoint`（PM 内部 handleDrop 先 posAtCoords）；jsdom 里 `focus()` 会把 PM selection 塌缩成光标（inclusive=false mark 后的光标 isActive('link') 为 false）——测工具栏用显式 `setTextSelection`。Playwright E2E：**position-click 到块级 `<p>` 中心会落在短文本右侧空白 → caret 在文本末尾**——确定性光标用 `Home` 键（配合 domObserver.flush 后 PM state 同步）；E2E 用 notification-flicker.spec.ts 的全 mock 后端模式（page.route 拦 `/api/*`，不需要真实 server）。
- **Streaming pipeline**: `MarkdownRenderer` gates re-parsing behind the typewriter — markdown is only re-parsed when `visibleChars` catches up to the parsed content length, NOT on every SSE chunk. `ParsedMarkdown` uses `key={debouncedContent}` to force fresh DOM on content change (typewriter clips `text.data` behind React's back). The `streaming` prop flows through a `StreamingContext` to all code-block-level components.
- **Mermaid rendering**: `MermaidDiagram` (`web/src/components/agent/MermaidDiagram.tsx`) lazy-loads the ~1MB mermaid package via `import('mermaid')` (module-scope singleton + `useSyncExternalStore`). **Must NOT render during streaming** — the source is incomplete (typewriter-clipped) and `mermaid.render()` is async + CPU-intensive. `CodeBlock` checks `StreamingContext`: streaming → `MermaidSourceBlock` (plain source, synchronous); settled → `MermaidDiagram` (renders SVG once). Theme-aware: re-initialises mermaid on dark/light + accent color changes. Error fallback shows raw source. `MarkdownPreview` always renders `MermaidDiagram` directly (non-streaming).
