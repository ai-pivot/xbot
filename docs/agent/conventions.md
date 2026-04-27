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
- `cjkWordBounds()` / `cjkWordAt()` — gse-based CJK word boundary cache

**CJK word segmentation:**
Uses `github.com/go-ego/gse` (pure Go, embedded dictionary) for Ctrl+Arrow
word navigation. The segmenter is lazy-initialized via `sync.Once` and shared
across all textarea instances. Word boundaries are cached per-line and
automatically invalidated when the line text changes.

**Gotcha — `CutSearch` vs `ModeSegment`:**
`ModeSegment` (HMM-based) segments isolated CJK words like `"测试"` as two
single characters. Use `CutSearch` (DAG-based) instead — it correctly
segments isolated CJK words. Both return correct results with surrounding
context; the difference only matters for short isolated text.

**Gotcha — Do NOT add trailing spaces to visual lines:**
Previous versions appended `' '` to each visual line in wrap() for cursor
navigation. This created phantom character positions that view() trimmed
inconsistently. LineInfo.StartColumn accumulated these phantom spaces,
shifting all cursor calculations. Removing them was a 3-part fix:
wrap() stops injecting, view() stops trimming, setCursorLineRelative
uses StartColumn+Width (down) and StartColumn-1 (up).
