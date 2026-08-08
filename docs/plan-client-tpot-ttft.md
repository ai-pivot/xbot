# 计划：客户端 TPOT/TTFT 采集

> 生成时间：2026-08-08
> 状态：待确认

## 背景

当前 xbot 没有客户端侧的 LLM 性能指标采集。后端 `CollectStreamWithCallback`（`llm/stream.go:90`）没有记录首个 chunk 到达时间（TTFT），也没有记录每 token 间隔（TPOT）。`TokenTracker` 只记录 API 返回的 `prompt_tokens` / `completion_tokens`，不含时间维度。

**目标**：在客户端（xbot 后端）采集 LLM 流式调用的 TTFT 和 TPOT 数据，通过 `/info` 命令和 Web UI 展示，帮助优化模型推理性能。

## 关键发现

### 现有链路

```
Run() → callLLM() → generateResponse() → RetryLLM.GenerateStreamAndCollect()
  → CollectStreamWithCallback(ctx, eventCh, streamContentFn, streamReasoningFn, ...)
    → for event := range eventCh:
        case EventContent:  streamContentFn(content)    // 每个 chunk
        case EventDone:     return response              // 流结束
```

### 时间戳缺失点

| 位置 | 当前状态 | 需要添加 |
|------|---------|---------|
| `CollectStreamWithCallback` 开始 | 无时间记录 | `requestStart = time.Now()` |
| 第一个 chunk 到达 | 无时间记录 | `firstChunkAt = time.Now()` → TTFT |
| 每个 chunk 到达 | 无时间记录 | `lastChunkAt = time.Now()` → TPOT |
| 流结束 | 无时间记录 | `endAt = time.Now()` → 总耗时 |
| `LLMResponse` | 无时间字段 | `TTFTMs`, `TPOTMs`, `TotalMs` |
| `ProgressEvent` | 只有 `ElapsedWall` | `TTFTMs`, `TPOTMs` |
| `TokenTracker` | 只有 token count | `LastTTFT`, `LastTPOT` |

## 详细计划

### 阶段一：在 `CollectStreamWithCallback` 中采集时间戳

**涉及文件：`llm/stream.go`**

在 `CollectStreamWithCallback` 函数中添加时间戳记录：

```go
func CollectStreamWithCallback(ctx context.Context, eventCh <-chan StreamEvent,
    streamContentFn func(string), streamReasoningFn func(string),
    streamToolCallFn func([]ToolCallDelta), streamUsageFn func(*TokenUsage),
) (*LLMResponse, error) {
    requestStart := time.Now()
    var firstChunkAt time.Time
    var lastChunkAt time.Time
    var chunkCount int64  // 非 reasoning chunk 计数（content + tool_call）

    // ... existing loop ...
    for {
        select {
        case <-ctx.Done():
            // ... existing cancel handling ...
            // Record timing even on cancel
            resp.StreamStats = &StreamStats{
                TTFTMs:   sinceMs(firstChunkAt, requestStart),
                TotalMs:  time.Since(requestStart).Milliseconds(),
                Chunks:   chunkCount,
            }
            return resp, nil

        case event, ok := <-eventCh:
            now := time.Now()
            if firstChunkAt.IsZero() {
                firstChunkAt = now
            }
            lastChunkAt = now

            switch event.Type {
            case EventContent:
                chunkCount++
                // ... existing ...
            case EventDone:
                // Record final timing
                resp.StreamStats = &StreamStats{
                    TTFTMs:   sinceMs(firstChunkAt, requestStart),
                    TPOTMs:   computeTPOT(firstChunkAt, lastChunkAt, chunkCount),
                    TotalMs:  time.Since(requestStart).Milliseconds(),
                    Chunks:   chunkCount,
                }
                // ... existing ...
            }
        }
    }
}
```

新增类型：

```go
// StreamStats holds timing statistics for a single LLM streaming response.
type StreamStats struct {
    TTFTMs  int64  // Time to first token (ms) — from request start to first chunk
    TPOTMs  int64  // Time per output token (ms) — (lastChunk - firstChunk) / (chunks-1)
    TotalMs int64  // Total stream duration (ms) — from request start to done
    Chunks  int64  // Number of content chunks received (excluding reasoning)
}
```

`LLMResponse` 新增字段：

```go
type LLMResponse struct {
    // ... existing fields ...
    StreamStats *StreamStats `json:"stream_stats,omitempty"`
}
```

### 阶段二：在 `callLLM` 中记录到 TokenTracker

**涉及文件：`agent/engine_run.go`**

```go
if response.Usage.PromptTokens > 0 {
    s.tokenTracker.RecordLLMCall(response.Usage.PromptTokens, response.Usage.CompletionTokens)
    // Record stream timing stats
    if response.StreamStats != nil {
        s.tokenTracker.RecordStreamStats(response.StreamStats)
    }
}
```

**涉及文件：`agent/token_tracker.go`**

```go
type TokenTracker struct {
    // ... existing fields ...
    lastTTFT  int64
    lastTPOT  int64
    lastTotal int64
    lastChunks int64
}

func (t *TokenTracker) RecordStreamStats(stats *llm.StreamStats) {
    if stats == nil { return }
    t.lastTTFT = stats.TTFTMs
    t.lastTPOT = stats.TPOTMs
    t.lastTotal = stats.TotalMs
    t.lastChunks = stats.Chunks
}

func (t *TokenTracker) GetStreamStats() (ttft, tpot, total int64, chunks int64) {
    return t.lastTTFT, t.lastTPOT, t.lastTotal, t.lastChunks
}
```

### 阶段三：在 ProgressEvent 中暴露

**涉及文件：`protocol/events.go`**

```go
type ProgressEvent struct {
    // ... existing fields ...
    StreamStats *StreamStats `json:"stream_stats,omitempty"`
}
```

**涉及文件：`agent/engine_wire.go`** — 在 `buildProgressPayload` 中附加 `StreamStats`：

```go
if s.TokenUsage != nil {
    payload.TokenUsage = &protocol.TokenUsage{...}
}
// Attach stream stats from the latest LLM call
if stats := s.tokenTracker.GetStreamStats(); stats.ttft > 0 {
    payload.StreamStats = &protocol.StreamStats{
        TTFTMs: stats.ttft, TPOTMs: stats.tpot,
        TotalMs: stats.total, Chunks: stats.chunks,
    }
}
```

### 阶段四：在 `/info` 命令中展示

**涉及文件：`agent/context_handler.go`** — `handleSessionInfo`

```go
// Token usage + stream stats
if pt, ct, _ := memSvc.GetTokenState(ctx, tenantSession.TenantID()); pt > 0 {
    fmt.Fprintf(&sb, "| Prompt Tokens | %s |\n", formatTokenCount(pt))
    fmt.Fprintf(&sb, "| Completion Tokens | %s |\n", formatTokenCount(ct))
}
// Stream timing stats
if ttft, tpot, total, chunks := uc.TokenTracker.GetStreamStats(); ttft > 0 {
    fmt.Fprintf(&sb, "| TTFT | %d ms |\n", ttft)
    fmt.Fprintf(&sb, "| TPOT | %d ms |\n", tpot)
    fmt.Fprintf(&sb, "| Stream Duration | %d ms |\n", total)
    fmt.Fprintf(&sb, "| Output Chunks | %d |\n", chunks)
}
```

### 阶段五：Web UI 展示

**涉及文件：`web/src/components/agent/ModelStatusBar.tsx`** 或新建 `StreamStatsBar.tsx`

在状态栏中展示当前会话的 TTFT/TPOT：

```
🧠 macaron-v1-venti | TTFT: 1.2s | TPOT: 28ms | 1.2k/8.5k tokens
```

数据来源：`ProgressEvent.StreamStats`（SSE 推送）或 `GetActiveProgress`（tick pull）。

### 阶段六：持久化到 DB（可选）

**涉及文件：`storage/sqlite/session_service.go`**

在 `session_messages` 表新增 `stream_stats` JSON 列（或新建 `stream_stats` 表），记录每次 LLM 调用的 TTFT/TPOT/Total/Chunks。支持历史查询和趋势分析。

```sql
CREATE TABLE IF NOT EXISTS stream_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    turn_id INTEGER NOT NULL,
    iteration INTEGER NOT NULL,
    ttft_ms INTEGER,
    tpot_ms INTEGER,
    total_ms INTEGER,
    chunks INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    model TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

## TPOT 计算公式

```
TTFT  = firstChunkAt - requestStart                              // 首 token 延迟
TPOT  = (lastChunkAt - firstChunkAt) / (totalChunks - 1)        // 每 token 平均延迟
Total = endAt - requestStart                                     // 总流式耗时
```

注意：
- `firstChunkAt` = 第一个**任何类型**的 chunk 到达时间（content / reasoning / tool_call 均算）
- `totalChunks` 包含 content chunk + reasoning chunk + tool_call chunk — 都是模型生成的输出
- 如果 `totalChunks <= 1`，TPOT 无法计算（只有一个 chunk），设为 0

## 验证方案

1. **单元测试**：`CollectStreamWithCallback` 的 mock event channel，验证 TTFT/TPOT 计算
2. **集成测试**：真实 LLM 调用后检查 `response.StreamStats` 非 nil
3. **`/info` 命令**：发送消息后执行 `/info`，确认 TTFT/TPOT 显示
4. **Web UI**：发送消息后检查状态栏显示 TTFT/TPOT

## 影响范围

| 文件 | 修改类型 |
|------|---------|
| `llm/stream.go` | 新增 `StreamStats` 类型 + `CollectStreamWithCallback` 时间记录 |
| `llm/types.go` | `LLMResponse` 新增 `StreamStats` 字段 |
| `agent/token_tracker.go` | 新增 `RecordStreamStats` / `GetStreamStats` |
| `agent/engine_run.go` | `callLLM` 中记录 `StreamStats` |
| `protocol/events.go` | `ProgressEvent` 新增 `StreamStats` 字段 |
| `agent/engine_wire.go` | `buildProgressPayload` 附加 `StreamStats` |
| `agent/context_handler.go` | `/info` 展示 TTFT/TPOT |
| `web/src/components/agent/` | 状态栏展示（可选） |

## 回滚策略

所有修改向后兼容（`StreamStats` 是 optional 字段，`omitempty` JSON tag）。不影响现有功能。回滚只需 revert 相关文件。
