# Coding Conventions

## Error Handling

- Wrap with context: `fmt.Errorf("context: %w", err)`
- Never use `pkg/errors` — stdlib `fmt.Errorf` with `%w` only
- User-facing validation errors can be Chinese
- Error message prefix: lowercase English with colon separator
- For sentinel errors: define in package, use `errors.Is()`/`errors.As()`

## Logging

- Import: `log "xbot/logger"` (custom wrapper, not stdlib)
- Single field: `log.WithField("key", val).Info("msg")`
- Multiple fields: `log.WithFields(log.Fields{...}).Info("msg")`
- Errors: `log.WithError(err).Error("msg")` — never `WithField("error", err)`
- Save to variable for reuse: `l := log.WithField("request_id", id)`
- Levels: Fatal (unrecoverable startup) → Error (runtime) → Warn (degraded) → Info (state change) → Debug (verbose)

## Testing

- Files: `*_test.go` alongside source, one test file per source file
- 102 test files across the project
- Pre-commit hook runs: gofmt → golangci-lint → go build → go test
- Run specific: `go test ./agent/ -run TestName -count=1`
- Run all: `go test ./...`

### 等异步：只用宽上限，绝不赌机器速度（2026-11-27 Windows CI 偶发红根治）

问题现场（Windows runner，`TestAskUserInterruptReapsExpiredThroughProcessLoop`）：

```
14:53:52  Auto-cancelling stale pending AskUser (TTL expired)
14:53:53  WAL state … sessions\web\68\chat-loop-stale-….db (open)
14:54:07  Failed to append ask_answer pairing …      ← 14 秒才写完
```

测试用的是 **10 秒固定上限轮询**，于是「代码对不对」变成了「机器够不够快」——
同一份代码在 Linux 上毫秒级通过、在 Windows 上必红（红不红只看当天负载）。

契约：

1. **上限统一用 `testAsyncWait`（2 分钟）**：各包 `zz_test_async_wait_test.go`
   （agent 包是 `agent/test_async_wait_test.go`）。⛔ 不要改回个位数秒 —— 这个值
   只回答「等多久判失败」，条件一成立就立即返回，放宽**不降低**判别力。
   只覆盖「正向等待」（timeout 分支是 `t.Fatal`）；**负向观察窗口**（等「不该发生
   的事」）保持短值，那才是真正的判据。
2. **优先用事件而不是轮询**：等 channel/广播（如
   `fakeResolvedSender.signal`、`ss.notifyCh`），别用 `sleep(20ms)` 反复读内存 ——
   轮询把「goroutine 调度延迟」写进了断言。
3. **预热昂贵的前置**：被等待的链路若含「首次打开会话库」，测试先
   `mt.GetOrCreateSession(...)`；环境开销不该落在判据窗口里。
4. **⛔ deadline 不得兼任「起点」**：若测试还要断言下界（如 TTL 必须 ≥900ms 才
   回收），显式 `start := time.Now()`，绝不能 `deadline.Add(-3*time.Second)`
   反推 —— 改上限会连带改错语义（2026-11-27 我改这一处时踩过，`TestInteractiveSessionTTL_Configurable`
   当场红）。

## Interfaces

- Define at point of use, not in separate `interfaces.go`
- Small, focused interfaces (1-3 methods typical)
- Key interfaces: see `docs/agent/architecture.md#key-interfaces`

## Concurrency

- Goroutines for: agent loops, channel handlers, background tasks, streaming
- ~70 goroutine launch points across the codebase
- Always use `context.WithCancel` for cancellable work
- Non-blocking channel sends with `select { case ch <- msg:; case <-ctx.Done(): }`
- Use `sync.WaitGroup` for background task drain on shutdown
- Never defer semaphore release inside loops (causes slot accumulation)

## Naming

- Packages: short, lowercase, no underscores (`agent`, `llm`, `tools`)
- Files: snake_case (`engine_run.go`, `middleware_builtin.go`)
- Test helpers: `setupXxx()`, `newMockXxx()`
- Constants: CamelCase in Go, UPPER_SNAKE for pre-commit env vars

## Build

```bash
go build ./...                  # compile all
go test ./...                   # run all tests
golangci-lint run ./...         # lint
```
Makefile targets: `make build`, `make run`, `make test`
Binary: `xbot-cli` from `cmd/xbot-cli/`

## Textarea (BubbleTea Component)

`internal/textarea/` is a fork of `charm.land/bubbles/v2/textarea` with CJK-aware
wrapping and word navigation. Key files:

- `textarea.go` — wrap(), LineInfo(), view(), setCursorLineRelative()
- `textarea_cjk_test.go` — base CJK tests
- `textarea_cursor_test.go` — cursor navigation regression tests

**Architecture:**
- `wrap()` splits logical text into visual line grid (no phantom spaces)
- `LineInfo()` maps cursor logical position (col) → visual row/column
- `view()` renders each visual line and positions cursor via LineInfo
- `setCursorLineRelative()` handles CursorUp/CursorDown across soft-wraps
- `cjkWordBounds()` / `Model.Word()` — gse-based CJK word boundary cache and word-at-cursor

**CJK word segmentation:**
Uses `github.com/go-ego/gse` (pure Go, embedded dictionary) for Ctrl+Arrow
word navigation. The segmenter is lazy-initialized via `sync.Once` and shared
across all textarea instances. Word boundaries are cached per-line and
automatically invalidated when the line text changes.

**Gotcha — `Segment` vs `CutSearch`:**
`CutSearch` (DAG-based) returns overlapping candidates for ambiguous
input (e.g. `"第一个"` → `["第一","一个","第一个"]`), which causes
cumulative position errors when treated as contiguous token boundaries.
Use `Segment` (HMM-based) instead — it produces non-overlapping tokens
that are safe for cursor navigation. The tradeoff is that `Segment` may
split isolated CJK words like `"测试"` into two single characters when
no surrounding context is available; this is acceptable because cursor
navigation correctness takes priority over ideal linguistic segmentation.

**Gotcha — Do NOT add trailing spaces to visual lines:**
Previous versions appended `' '` to each visual line in wrap() for cursor
navigation. This created phantom character positions that view() trimmed
inconsistently. LineInfo.StartColumn accumulated these phantom spaces,
shifting all cursor calculations. Removing them was a 3-part fix:
wrap() stops injecting, view() stops trimming, setCursorLineRelative
uses StartColumn+Width (down) and StartColumn-1 (up).

## Local / Remote Unification

CLI operates in two deployment modes:
- **Local**: Backend runs in-process, Transport is `ChannelTransport` (direct function calls to RPCTable, zero overhead)
- **Remote**: Backend runs on xbot-server, Transport is `RemoteTransport` (WebSocket RPC)

**Architecture (post Transport refactor)**:

```
Transport (2 methods: Call + Close) — pure transmission layer
  ├── ChannelTransport: in-process → RPCTable.Dispatch
  └── RemoteTransport: WebSocket RPC to xbot-server

AgentRunner + EventRouter + CallbackRegistry — lifecycle separation
  ├── LocalLifecycle: in-process (agent/bus/eventCh)
  └── RemoteTransport: also implements all three interfaces

DirectBackend: replaces old localTransport handler table
  → calls Agent methods directly, used by RPCTable in server-side handlers
```

**Principle**: All CLI code goes through `AgentBackend` interface. The Transport
layer handles routing. CLI should NOT have `IsRemote()` branches except for
irreducible architectural differences:

| Irreducible Branch | Reason |
|---|---|
| `RemoteMode` field | TUI needs to know whether to show connection status |
| LLM client rebuild | Only local mode has `createLLM()` closure |
| Remote event subscription | Only remote mode receives WS push events |
| WS connection lifecycle | Only remote mode connects to server |
| Local subscription seeding | Only local mode seeds from config.json on first run |

Everything else (settings, history, sessions, web users, chat management, UI callbacks)
is unified through Backend methods that route via Transport automatically.

**When adding new CLI functionality**:
1. Add method to `AgentBackend` interface (agent/backend.go)
2. Implement in Backend (agent/backend_impl.go) using `CallRPC` (auto-routes via Transport)
3. Add `DirectBackend` method (agent/direct_backend.go) for local mode
4. Add server RPC handler (serverapp/rpc_table.go) — single truth source for both modes
5. No IsRemote() needed in CLI code

## Session Export (`protocol/session_export.go`)

- `ExportSession` produces a portable JSON (`--export-after` for bench). The **last
  assistant message's `Detail` is stripped** on export: it holds the aggregated
  iteration-history JSON which duplicates content already present in the message
  stream. Tool-message `Detail` (UI-only diff etc.) is preserved — it round-trips
  through `ImportSession` for UI rendering and is not sent to the LLM.
- `DisplayOnly` messages are filtered out; the first system message becomes
  `SystemInstructions`.
