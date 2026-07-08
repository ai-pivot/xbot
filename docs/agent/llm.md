# llm/ — LLM Client Abstraction

## Files

| File | Purpose |
|------|---------|
| `interface.go` | LLM, StreamingLLM interfaces |
| `openai.go` | OpenAI-compatible client (OpenAI, DeepSeek, Qwen, etc.) (~901 lines) |
| `anthropic.go` | Anthropic/Claude client (~741 lines) |
| `retry.go` | Exponential backoff retry wrapper (~295 lines) |
| `stream.go` | CollectStream: assembles StreamEvents into LLMResponse |
| `types.go` | ChatMessage, ToolDefinition, LLMResponse, think block extraction |
| `proxy.go` | ProxyLLM: forwards via sandbox protocol to remote runner |
| `semaphore.go` | Per-tenant concurrency limiter |
| `think_extract.go` | Extracts <think/>, <reasoning> blocks |
| `tokenizer.go` | Token counting via tiktoken (~380 lines) |

## Streaming Pitfalls

- **Anthropic `hasContent` must be set on ALL content-emitting branches, not just `text_delta`.** `thinking_delta`, `input_json_delta`, and `content_block_start`→`tool_use` all emit events to eventChan but were missing `hasContent = true`. Without this, a stream truncated during thinking-only or tool-use-only responses has `hasContent=false` → EOF sends `EventDone` instead of `EventError` → `stream.go` safety net (`gotDone`) doesn't fire → three layers of defense all penetrated → truncated content silently dropped (`anthropic.go` `streamAnthropic`).
- DeepSeek duplicates `reasoning_content` in Content — deduplicate with TrimSpace (`openai.go:584`)
- Empty stream deltas (all nil) cause panic if not skipped (`openai.go:763`)
- `finish_reason` in intermediate chunks causes premature termination — check only after loop ends (`openai.go:788`)
- Must send Usage before Done event (`openai.go:836`)
- Provider without `finish_reason` but with tool_calls: infer reason as tool_calls (`openai.go:844`)
- OpenAI-compatible `Generate` is a non-stream API, but some providers return `text/event-stream` even for non-stream requests. If `openai-go` fails with the explicit SSE-not-JSON content-type error, `Generate` falls back to `GenerateStream` + `CollectStream` and still returns a complete `LLMResponse`. This keeps compaction/non-stream callers provider-agnostic without masking ordinary JSON/API errors.

## Reasoning History Replay

- In OpenAI-compatible auto mode, if any assistant message in the conversation already has `reasoning_content`, replay all assistant history messages with a `reasoning_content` field as well (use `""` when the original reasoning was lost, e.g. after compression). Some reasoning providers reject mixed assistant history shapes.

## Retry Behavior

- `Generate` (non-stream): uses `perAttemptCtx` — fresh `context.Background()` with timeout per attempt, parent cancel bridged via goroutine (`retry.go:251-278`)
- `GenerateStreamAndCollect`: does NOT use `perAttemptCtx`. A per-attempt deadline would bind to the underlying HTTP connection, killing active streams mid-generation when total elapsed time exceeds the deadline. Instead, passes parent `ctx` directly to `GenerateStream` and `CollectStreamWithCallback`. Stream timeout is handled by idle timeout only.
- `CollectStreamWithCallback` idle timeout: 120s without any chunk → `context.DeadlineExceeded`. Timer resets on every received chunk. Active streams of any duration are safe. This replaces the old approach of using ctx deadline as total stream timeout, which incorrectly killed long-running responses.
- `CollectStreamWithCallback` early tool detection: the 5th param `onToolCall func([]ToolCallDelta)` fires when a tool NAME arrives in the stream (first chunk of each tool call), before arguments finish generating. OpenAI/Anthropic send tool names early — this lets the UI show "✦ Read generating…" immediately. Callback fires once per tool name arrival, NOT per argument delta. All existing callers pass `nil` for backward compat.

## Client Fingerprinting

The OpenAI Go SDK (`openai-go/v3`) injects `X-Stainless-*` headers that TypeScript clients never send. These are stripped via `option.WithHeaderDel()` to match opencode's fingerprint:
- `X-Stainless-Lang`, `X-Stainless-Package-Version`, `X-Stainless-OS`, `X-Stainless-Arch`, `X-Stainless-Runtime`, `X-Stainless-Runtime-Version`, `X-Stainless-Timeout`
- Default `User-Agent` set to `opencode/1.14.17` (matches opencode's format)
- `stream_options: {include_usage: true}` added to all requests (matches Vercel AI SDK behavior)

## Async Model Loading

`NewOpenAILLM` loads model list in a goroutine (non-blocking). `ListModels()` returns fallback model immediately, full list updates when API responds.

## Key Interfaces

```go
type LLM interface {
    Generate(ctx, model, messages, tools, thinkingMode) (*LLMResponse, error)
    ListModels() []string
}
type StreamingLLM interface {
    Stream(ctx, model, messages, tools, thinkingMode) (<-chan StreamEvent, error)
}
type ModelLoader interface {
    LoadModelsFromAPI(ctx context.Context) error
}
```

`ModelLoader` is implemented by `*OpenAILLM` only — used by `GetLLMForModel` via type assertion for sync model loading on cache miss.

## OnModelsLoaded Callback

`UserLLMConfig.OnModelsLoaded` is called by `NewOpenAILLM`'s async goroutine after fetching model list from API. Used to persist models to DB via `UpdateCachedModels`. Must handle case where sub ID doesn't exist in DB (config-only subs).
