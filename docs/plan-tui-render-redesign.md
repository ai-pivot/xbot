# 技术方案：TUI 渲染管线架构重设计

> 生成时间：2026-07-02
> 状态：待确认

## 1. 背景与目标

### 1.1 现状问题

当前 CLI TUI 渲染管线经过长期迭代，积累了大量防御性编程。排查发现三个用户可感知的 bug（reasoning 闪烁、turn 结束闪烁、scroll 强制跳底），根因都指向架构层面的数据流设计缺陷。

核心矛盾：**系统试图从离散、有损、乱序的事件流中渲染连续画面**。

### 1.2 防御性代码统计

经完整审计，当前渲染管线共有 **47 处防御性代码**，分布如下：

| 类别 | 数量 | 根因 |
|------|------|------|
| Fallback 链（多级数据补救） | 15 处 | 流式事件与结构化事件的字段割裂 |
| 去重逻辑（写入+读取双重保险） | 7 处 | PhaseDone/handleAgentMessage 双路径产生重复 |
| 状态保留（防闪烁） | 6 处 | turn 结束的两个事件之间有中间态 |
| 竞态防护（session/cancel） | 12 处 | 异步 RPC 的固有特性（**不可消除**） |
| 数据修复/补救 | 7 处 | streamingMsgIdx 失效 + coalescing 丢数据 |

### 1.3 目标

通过架构重设计，从根源消除前 35 处防御性代码（Fallback链 + 去重 + 状态保留 + 数据修复），保留 12 处必要的竞态防护。具体目标：

1. **消除 carryForwardProgressState**（9 个字段的 carry-forward 逻辑）
2. **消除 snapshotIterationChange 的多级 fallback 链**
3. **消除 turnDoneFlags + pendingToolSummary**（双路径到达问题）
4. **消除 guide 颜色跳变**（turn 结束闪烁根因）
5. **消除 reasoning 闪烁**（carryForward 条件不足导致）
6. **简化 4 层缓存为 2 层**
7. **消除 dedupMessagesGuard**（写入路径保证唯一则不需要读取去重）

---

## 2. 根因分析

### 2.1 根因一：流式事件与结构化事件的字段割裂

```
引擎产生两种 progress 事件：
├─ 结构化事件 (Phase, Iteration, ActiveTools, CompletedTools, Thinking, Reasoning)
│   来源: notifyProgress / progressFinalizer
│
└─ 流式事件 (StreamContent, ReasoningStreamContent, StreamingTools, StreamTokens)
    来源: StreamContentFunc / StreamReasoningFunc / StreamToolCallFunc
```

**问题**：CLI 的 `handleProgressMsg` 用 `m.progressState.current = msg.payload` **整体替换** current（`cli_update_progress.go:335`）。这导致流式字段被结构化事件清空。`carryForwardProgressState` 试图恢复它们，但条件过于保守（thinking 阶段不恢复 ReasoningStreamContent）→ **reasoning 闪烁**。

**防御链**：carryForwardProgressState（9字段）→ reasoningByIter map → lastReasoning → liveIterationBlocks 的 RSC→Reasoning fallback → snapshotIterationChange 的 3 级 fallback → handleProgressDone 的 6 级 fallback

### 2.2 根因二：progressCh coalescing（buffer=1）丢失事件

```
progressCh (buffer=1) 蓄意丢弃中间事件
├─ 工具状态转换 (running→done) 丢失 → liveIterationBlocks 需要工具去重
├─ CompletedTools 列表不完整 → 需要 carryForward CompletedTools
├─ 结构化 Reasoning 丢失 → 需要 reasoningByIter + fallback
└─ iteration 切换事件丢失 → snapshotIterationChange 需要数据不匹配路径
```

**问题**：coalescing 是性能优化（防止高频 event 淹没 BubbleTea event loop），但代价是数据丢失。所有防御逻辑都在补偿这个丢失。

### 2.3 根因三：PhaseDone 与 handleAgentMessage 作为独立事件

```
turn 结束 = 两个独立事件
├─ PhaseDone: 携带工具快照 (CompletedTools) + 最后迭代
└─ handleAgentMessage: 携带最终内容 (content) + reasoning

到达顺序不确定:
├─ PhaseDone 先到 → endAgentTurn → typing=false → 但 streamingMsgIdx 保留
│   → updateStreamingOnly 用 bright guide 渲染"半成品"
│   → handleAgentMessage 后到 → isPartial=false → dim guide
│   → ★ guide 颜色跳变 = 闪烁
│
└─ handleAgentMessage 先到 → finalize message → PhaseDone 后到检查 doneProcessed
```

**防御链**：turnDoneFlags（doneProcessed/replyReceived/2s timeout）→ pendingToolSummary → bakeIterations 3级 fallback → endAgentTurn 保留 streamingMsgIdx → endAgentTurn 保留 progressState → dedupMessagesGuard → upsertMessageByTurn + purgeZombieMessages → rerenderCachedMessage

### 2.4 根因四：tick 渲染中间态

```
100ms tick → updateViewportContent()
├─ turn 活跃时: 看到 progressState.current 的最新状态 (正确)
├─ PhaseDone 后、handleAgentMessage 前: 看到 typing=false + 半成品 (中间态)
└─ turn 结束后: 看到缓存 (正确)
```

**问题**：tick 期间数据可能处于"turn 已结束但消息未完成"的中间态，这不需要防御——需要消除中间态本身。

### 2.5 根因五：4 层缓存 + 25 个失效点

```
第0层: msg.rendered/wrappedLines (per-message)
第1层: rc.history/histLines/histMaxW (aggregate)
第2层: rc.allLines/dynamicLines/gen counter (assembly)
第3层: viewport private fields (unsafe bypass)

25 个 rc.valid=false 触发点 → 部分触发 fullRebuild (O(N) glamour)
```

**问题**：缓存层级过多，失效路径复杂。generation counter 是正确设计但增加了理解成本。

---

## 3. 新架构设计

### 3.1 核心原则

**原则一：原地合并，不整体替换**
> 流式字段和结构化字段在同一个 `ProgressState` 中原地更新，永不整体替换。消除 carryForward。

**原则二：引擎负责快照，CLI 只消费**
> iteration 完成时由引擎发送预构建的快照，CLI 不需要检测 iteration 变化并自己快照。消除 snapshotIterationChange。

**原则三：单一 TurnComplete 事件**
> turn 结束是一个原子事件，携带全部最终状态（内容+reasoning+迭代历史+token）。消除 PhaseDone/handleAgentMessage 双路径。

**原则四：数据层与渲染层分离**
> 数据层只由事件更新，永远一致。渲染层纯从数据层派生，无独立状态。tick 只驱动动画帧，不驱动内容渲染。

### 3.2 新数据模型

```go
// ProgressState — 单一可变状态，原地更新，永不替换
type ProgressState struct {
    mu     sync.Mutex
    dirty  bool       // 有更新未消费

    // 结构化字段（由 notifyProgress 更新）
    Phase          string
    Iteration      int
    ActiveTools    []protocol.ToolProgress
    CompletedTools []protocol.ToolProgress
    Thinking       string
    Reasoning      string
    TokenUsage     *protocol.TokenUsage
    Todos          []protocol.TodoItem
    SubAgents      []protocol.SubAgentNode

    // 流式字段（由 streaming callbacks 更新，原地覆盖）
    StreamContent          string
    ReasoningStreamContent string
    StreamingTools         []protocol.ToolProgress
    StreamTokens           int

    // 元数据
    StartedAt time.Time
}
```

```go
// IterationSnapshot — 引擎预构建的迭代快照
type IterationSnapshot struct {
    Iteration   int
    Tools       []protocol.ToolProgress
    Thinking    string
    Reasoning   string
    ElapsedWall int64
}
```

```go
// TurnState — CLI 端的 turn 状态机
type TurnState struct {
    TurnID    uint64
    Active    bool              // turn 是否活跃
    Current   *ProgressState    // 当前迭代的 live 状态
    History   []IterationSnapshot // 已完成迭代（由引擎发送）
    Cancelled bool
}
```

### 3.3 新事件类型

```go
// ProgressUpdate — 原地合并到 ProgressState
// 替代当前的 cliProgressMsg，不再区分 isStreamOnly
type ProgressUpdate struct {
    ChatID    string
    TurnID    uint64

    // 结构化字段（nil = 不更新，非 nil = 设为新值）
    Phase          *string
    Iteration      *int
    ActiveTools    *[]protocol.ToolProgress
    CompletedTools *[]protocol.ToolProgress
    Thinking       *string
    Reasoning      *string
    TokenUsage     *protocol.TokenUsage
    Todos          *[]protocol.TodoItem

    // 流式字段（直接设为最新值）
    StreamContent          string
    ReasoningStreamContent string
    StreamingTools         []protocol.ToolProgress
    StreamTokens           int
}

// IterationSnapshotEvent — 引擎发送的迭代完成快照
type IterationSnapshotEvent struct {
    ChatID   string
    TurnID   uint64
    Snapshot IterationSnapshot
}

// TurnCompleteEvent — 单一的 turn 完成事件
type TurnCompleteEvent struct {
    ChatID      string
    TurnID      uint64
    Content     string                // 最终回复文本
    Reasoning   string                // 最终 reasoning
    Thinking    string                // 最终 thinking
    Iterations  []IterationSnapshot   // 完整迭代历史
    TokenUsage  *protocol.TokenUsage
    Cancelled   bool
}
```

### 3.4 新数据流

```
┌─────────────────────────────────────────────────────────┐
│ 引擎层                                                   │
│                                                          │
│  streaming callbacks ──→ SendStreamFields() ──────────┐ │
│  notifyProgress ──────→ SendStructuredProgress() ─────┤ │
│  iteration change ────→ SendIterationSnapshot() ──────┤ │
│  turn complete ───────→ SendTurnComplete() ───────────┤ │
│                                                       │ │
│  所有 Send 方法 ──→ 原地更新 ProgressState ──→ notify  │ │
│                  (mutex 保护，字段级合并)              │ │
└───────────────────────────────────────────────────────┘ │
                                                          │
┌─────────────────────────────────────────────────────────┐
│ CLIChannel 层                                            │
│                                                          │
│  notifyCh (buf=1) ← "有更新"信号（可丢弃，无数据丢失）  │
│       │                                                  │
│       ▼                                                  │
│  drain goroutine:                                        │
│    lock ProgressState.mu                                 │
│    snapshot = deep-copy ProgressState                     │
│    dirty = false                                         │
│    unlock                                                │
│    send snapshot to asyncCh                              │
│                                                          │
│  IterationSnapshotEvent → 直接 send to asyncCh           │
│  TurnCompleteEvent → 直接 send to asyncCh                │
└──────────────────────────────────────────────────────────┘
                                                          │
┌─────────────────────────────────────────────────────────┐
│ CLI Update 层 (BubbleTea 串行)                           │
│                                                          │
│  case ProgressSnapshot:                                  │
│    merge snapshot into TurnState.Current                 │
│    → 触发一次 updateViewportContent()                    │
│                                                          │
│  case IterationSnapshotEvent:                            │
│    append snapshot to TurnState.History                  │
│    → 触发一次 updateViewportContent()                    │
│                                                          │
│  case TurnCompleteEvent:                                 │
│    finalize streaming message (原子操作):                │
│      msg.isPartial = false                               │
│      msg.content = event.Content                         │
│      msg.iterations = event.Iterations                   │
│      msg.thinking = event.Thinking                       │
│      streamingMsgIdx = -1                                │
│      typing = false                                      │
│    → 触发一次 updateViewportContent()                    │
│    ★ 无中间态，无 guide 跳变                             │
│                                                          │
│  case cliTickMsg:                                        │
│    只推进动画帧 (spinner/typewriter/timer)               │
│    如果 turn 活跃 → updateViewportContent() (刷新动画)   │
│    如果 turn idle → 不调 updateViewportContent (省 CPU)  │
└──────────────────────────────────────────────────────────┘
```

### 3.5 关键设计：为什么消除了防御

#### carryForwardProgressState → 消除

**原因**：`ProgressState` 是原地更新，流式字段和结构化字段永远共存。结构化事件不会清空流式字段（因为是字段级更新，不是整体替换）。

**消除的代码**：
- `cli_update_progress.go:42-185` 整个 `carryForwardProgressState` 函数
- `reasoningByIter` map（不再需要——ProgressState.Reasoning 始终可用）
- `lastReasoning`（同上）
- `lastCompletedTools`（同上）
- `liveIterationBlocks` 的 RSC→Reasoning fallback（直接读 ProgressState，两个字段都可用）

#### snapshotIterationChange → 消除

**原因**：引擎在 iteration 切换时主动发送 `IterationSnapshotEvent`，CLI 只需 append 到 `TurnState.History`。

**消除的代码**：
- `cli_update_progress.go:558-652` 整个 `snapshotIterationChange` 函数
- 数据不匹配路径 + alreadySnapped guard（引擎保证不重复发送）
- 3 级 reasoning fallback（快照由引擎预构建，reasoning 已包含）

#### turnDoneFlags + pendingToolSummary → 消除

**原因**：`TurnCompleteEvent` 是单一原子事件，携带全部最终状态。不存在"谁先到"的问题。

**消除的代码**：
- `turnDoneFlags` 结构体和相关逻辑
- `pendingToolSummary`
- `bakeIterations` 及其 3 级 fallback
- `handleProgressDone` 的正常路径和 cancel 路径的 6 级 reasoning fallback
- `endAgentTurn` 的 turnID guard（TurnCompleteEvent 自带 turnID，且是唯一完成路径）

#### guide 颜色跳变 → 消除

**原因**：`TurnCompleteEvent` 是原子操作——`isPartial=true→false` 和 `streamingMsgIdx→-1` 同时发生。不存在 `typing=false` 但 `streamingMsgIdx>=0` 的中间态。

`updateStreamingOnly` 只在 `typing=true` 时被调用（turn 活跃期间）。turn 完成后直接走缓存路径，guide 已经是 dim。

**消除的代码**：
- `endAgentTurn` 保留 `streamingMsgIdx` 的设计（不需要保留——没有中间态）
- `endAgentTurn` 保留 `progressState` 的设计（不需要保留——没有中间态）
- `rerenderCachedMessage`（TurnCompleteEvent 直接 finalize 消息，下一步走正常缓存路径）

#### dedupMessagesGuard + upsertMessageByTurn → 消除

**原因**：单一 `TurnCompleteEvent` 路径意味着每个 turn 只有一条 assistant 消息通过一条路径创建。不存在 PhaseDone 和 handleAgentMessage 各创建一条的可能。

**消除的代码**：
- `dedupMessagesGuard`（慢速路径前的全量扫描）
- `upsertMessageByTurn` + `purgeZombieMessages`（写入去重）
- `findMessageByTurn` in isPartial fallback（streamingMsgIdx 不会失效）

#### liveIterationBlocks 工具去重 → 消除

**原因**：`ProgressState` 原地更新，`ActiveTools` 和 `CompletedTools` 不会因整体替换而产生瞬时重复。引擎的 `progressFinalizer` 保证 iteration 切换时 `ActiveTools` 全部移入 `CompletedTools`。

**消除的代码**：
- `cli_render_turn.go:398-416` 的 `Name+"\x00"+Label` 去重逻辑 + generating 例外

#### renderTurnBody content/Thinking 去重 → 消除

**原因**：`TurnCompleteEvent` 携带的 `Iterations` 由引擎预构建，`Thinking` 字段只存 thinking/reasoning 文本（不是完整的 LLM 回复）。`Content` 是唯一的回复文本来源。

**消除的代码**：
- `cli_render_turn.go:130-136` 的 `strings.TrimSpace` 精确匹配去重

### 3.6 缓存简化：4 层 → 2 层

#### 保留的 2 层

```
第 0 层: Per-Message Render Cache
  msg.rendered      ← glamour 输出 (dirty/renderWidth 控制)
  msg.wrappedLines  ← 预折行行数组
  msg.dirty         ← 是否需重新渲染

第 1 层: Viewport Line Cache
  rc.viewportLines  ← 所有非流式消息的 wrappedLines 拼接
  rc.viewportMaxW   ← 最大行宽
  rc.msgCount       ← 覆盖的消息数
  rc.valid          ← 是否有效
```

#### 消除的缓存层和字段

| 字段 | 消除原因 |
|------|----------|
| `rc.history` (string) | 被 `viewportLines` 取代，不再需要字符串拼接 |
| `rc.wrapHistory/Raw/Width` | `setViewportContent` 字符串路径被 direct lines 取代 |
| `rc.lastTick{HistLen,ProgFP,RewFP}` | tick 不再驱动内容渲染 |
| `rc.dynamicRaw/Lines/Width` | rewind block 用独立函数即时渲染 |
| `rc.histGen/allLinesGen` | 不需要 generation counter——invalidation 由事件驱动 |
| `rc.streamAllBuf/streamPrefix*` | streaming 从 TurnState 直接渲染 |
| `rc.streamCompleted{Lines,Count,Width,MaxW}` | 从 TurnState.History 直接渲染（数据量小） |
| `rc.streamHeaderLine/Width` | header 即时构建（开销极小） |
| `rc.progressBlock` | 已是 no-op，直接删除 |
| `rc.vpContent/vpWidth` | tick 不再做去重检测 |

#### 缓存失效简化

**旧**：25 个 `rc.valid=false` 触发点

**新**：3 个触发点
1. `TurnCompleteEvent` finalize 消息后 → msgCount 变化
2. 终端宽度变化 → 所有消息 dirty
3. Session 切换 / compression → 全量重建

### 3.7 渲染路径简化

#### 旧路径（4 条）

```
updateViewportContent()
├─ FastPath 1: streamingMsgIdx>=0 && rc.valid → updateStreamingOnly()
├─ FastPath 2: rc.valid && msgCount不变 → tick 直拼 (histLines + rewind)
├─ FastPath 3: rc.valid && msgCount变多 → appendNewMessagesToCache()
└─ SlowPath: dedupGuard → fullRebuild → setViewportContent
```

#### 新路径（2 条）

```
updateViewportContent()
├─ turn 活跃 (TurnState.Active):
│    → renderStreamingTurn()
│      History iterations (从 TurnState.History 直接渲染，小数据量)
│      + Live iteration (从 TurnState.Current 直接渲染)
│      + 前缀 (已缓存的历史消息行)
│    → viewportSetLinesBypassMaxWidth
│
└─ turn idle (!TurnState.Active):
     → renderIdleView()
       如果 msgCount 变化 → appendNewMessages
       否则 → 复用 viewportLines (零开销)
     → viewportSetLinesBypassMaxWidth
```

#### 消除的渲染函数

| 函数 | 消除原因 |
|------|----------|
| `updateStreamingOnly` 的 prefix缓存逻辑 | streaming 从 TurnState 直接渲染 |
| `fullRebuild` 的 splitIdx 逻辑 | 不需要区分 streaming/non-streaming 消息 |
| `setViewportContent` 的字符串路径 | 直接用行数组 |
| `dedupMessagesGuard` | 写入路径保证唯一 |

### 3.8 GotoBottom 统一

**问题**：当前有 11 处 `GotoBottom()` 调用，其中 1 处（`cli_viewport.go:320`）无条件强制滚动。

**新设计**：所有 `GotoBottom` 调用统一通过一个函数：

```go
func (m *cliModel) maybeScrollToBottom() {
    if !m.userScrolledUp {
        m.viewport.GotoBottom()
        m.newContentHint = false
    } else {
        m.viewport.SetYOffset(m.savedYOffset)
        m.newContentHint = true
    }
}
```

**消除**：`cli_viewport.go:319-321` 的无条件 GotoBottom（所有路径统一守卫）

---

## 4. 实施计划

### 阶段一：原地合并 ProgressState（CLI-only，最高 ROI）

**目标**：消除 carryForwardProgressState 及其所有 fallback 链

**改动**：
1. `channel/cli/cli.go` — 修改 `SendProgress` 为原地更新 `ProgressState`（mutex 保护）
2. `channel/cli/cli_update_progress.go` — `handleProgressMsg` 改为消费 `ProgressState` snapshot，不再区分 isStreamOnly
3. 删除 `carryForwardProgressState` 函数
4. 删除 `reasoningByIter`、`lastReasoning`、`lastCompletedTools`
5. 简化 `liveIterationBlocks`：直接读 `ProgressState`，无需 fallback
6. 简化 `snapshotIterationChange`：直接读 `ProgressState` 快照，无需 fallback 链

**涉及文件**：
- `channel/cli/cli.go`
- `channel/cli/cli_update_progress.go`
- `channel/cli/cli_render_turn.go`
- `channel/cli/cli_model.go`

**验证**：
- DeepSeek 模型测试：reasoning 不闪缩
- 普通模型测试：progress 正常显示
- `go test ./channel/cli/...` 全部通过

### 阶段二：CLIChannel 构建 IterationSnapshot（CLI-only，简化快照）

**目标**：消除 `snapshotIterationChange` 的 fallback 链

**改动**：
1. `channel/cli/cli.go` — 检测 iteration 变化时，从原地合并的 ProgressState 直接构建快照（所有字段可用，无需 fallback）
2. `channel/cli/cli_update_progress.go` — 简化快照逻辑为单行读取
3. 删除 `snapshotIterationChange` 的数据不匹配路径 + alreadySnapped guard

**涉及文件**：
- `channel/cli/cli.go`
- `channel/cli/cli_update_progress.go`

**验证**：
- 多迭代 turn 测试：每个迭代的工具和 reasoning 正确显示
- SubAgent 测试：SubAgent iteration 正确快照

### 阶段三：CLIChannel 合并 TurnComplete 事件（CLI-only，最大架构改进）

**目标**：消除 turnDoneFlags、pendingToolSummary、guide 跳变、dedup 链

**改动**：
1. `channel/cli/cli.go` — 缓冲 PhaseDone 和 outbound message，两者到齐后合并为 `TurnCompleteEvent` 发送给 Update loop（2s 超时保底）
2. `channel/cli/cli_agent_msg.go` — 添加 `TurnComplete` handler：原子 finalize streaming message
3. 删除 `handleProgressDone` 的正常完成路径（保留 cancel 路径）
4. 删除 `turnDoneFlags`、`pendingToolSummary`
5. 删除 `dedupMessagesGuard`、`upsertMessageByTurn`、`purgeZombieMessages`
6. 简化 `endAgentTurn`：不清除 streamingMsgIdx 的设计理由消失
7. 修复 `updateStreamingOnly` guide 颜色：只在 `typing=true` 时用 bright

**涉及文件**：
- `channel/cli/cli.go`
- `channel/cli/cli_agent_msg.go`
- `channel/cli/cli_update_progress.go`
- `channel/cli/cli_turn.go`
- `channel/cli/cli_viewport.go`
- `channel/cli/cli_cache.go`

**验证**：
- turn 结束无闪烁（guide 无跳变）
- cancel 路径正确（保留 cancel 颜色路径）
- 消息无重复
- `go test ./...` 全部通过

### 阶段四：缓存简化（CLI-only，清理）

**目标**：4 层缓存 → 2 层

**改动**：
1. `channel/cli/cli_block_cache.go` — 简化 `renderCache` 结构体
2. `channel/cli/cli_cache.go` — 重写 `fullRebuild` 和 `appendNewMessagesToCache`
3. `channel/cli/cli_viewport.go` — 简化 `updateViewportContent` 为 2 条路径
4. 删除 `setViewportContent` 字符串路径
5. 统一 `GotoBottom` 调用为 `maybeScrollToBottom`

**涉及文件**：
- `channel/cli/cli_block_cache.go`
- `channel/cli/cli_cache.go`
- `channel/cli/cli_viewport.go`

**验证**：
- 终端 resize 测试
- session 切换测试
- 长对话性能测试（CPU profiling）

---

## 5. 防御代码消除映射表

| # | 防御代码 | 文件:行 | 根因 | 消除方式 | 阶段 |
|---|---------|---------|------|----------|------|
| 1 | carryForward StartedAt | cli_update_progress.go:48-67 | 整体替换丢字段 | 原地合并 | 一 |
| 2 | carryForward CompletedTools | cli_update_progress.go:75-77 | 同上 | 同上 | 一 |
| 3 | carryForward Reasoning | cli_update_progress.go:80-82 | 同上 | 同上 | 一 |
| 4 | carryForward Thinking | cli_update_progress.go:83-85 | 同上 | 同上 | 一 |
| 5 | carryForward StreamContent | cli_update_progress.go:94-98 | 同上 | 同上 | 一 |
| 6 | carryForward ReasoningStreamContent | cli_update_progress.go:120-126 | 同上 | 同上 | 一 |
| 7 | carryForward StreamTokens | cli_update_progress.go:132-134 | 同上 | 同上 | 一 |
| 8 | carryForward StreamingTools | cli_update_progress.go:145-157 | 同上 | 同上 | 一 |
| 9 | carryForward SubAgents | cli_update_progress.go:169-181 | 同上 | 同上 | 一 |
| 10 | reasoningByIter map | cli_update_progress.go:454-459 | 结构化 Reasoning 可能丢失 | ProgressState 原地保留 | 一 |
| 11 | lastReasoning | cli_model.go | 同上 | 同上 | 一 |
| 12 | lastCompletedTools | cli_model.go | CompletedTools 可能丢失 | 同上 | 一 |
| 13 | liveIterationBlocks RSC→Reasoning fallback | cli_render_turn.go:368-371 | 流式/结构化字段割裂 | 原地合并，两字段共存 | 一 |
| 14 | liveIterationBlocks content 3级 fallback | cli_render_turn.go:384-390 | 同上 | 同上 | 一 |
| 15 | liveIterationBlocks 工具去重 | cli_render_turn.go:398-416 | coalescing 丢状态转换 | 原地合并不丢字段 | 一 |
| 16 | snapshotIterationChange 正常路径 | cli_update_progress.go:617-648 | CLI 需检测迭代变化 | 引擎发送快照 | 二 |
| 17 | snapshotIterationChange 数据不匹配路径 | cli_update_progress.go:572-615 | coalescing 导致 prev 归属不确定 | 引擎发送快照 | 二 |
| 18 | snapshotIterationChange reasoning 3级 fallback | cli_update_progress.go:626-636 | 结构化 Reasoning 可能丢失 | 引擎预构建快照 | 二 |
| 19 | handleProgressDone 正常路径 reasoning 4级 fallback | cli_update_progress.go:797-807 | 多路径到达 | TurnComplete 单事件 | 三 |
| 20 | handleProgressDone cancel 路径 reasoning 5级 fallback | cli_update_progress.go:720-734 | Ctrl+C 时结构化字段空 | TurnComplete.Cancelled | 三 |
| 21 | handleProgressDone cancel 路径 Thinking 3级 fallback | cli_update_progress.go:713-718 | 同上 | 同上 | 三 |
| 22 | turnDoneFlags | cli_turn.go:417-432 | PhaseDone/reply 谁先到 | TurnComplete 单事件 | 三 |
| 23 | pendingToolSummary | cli_update_progress.go | PhaseDone 先到时暂存 | TurnComplete 携带全部 | 三 |
| 24 | bakeIterations 3级 fallback | cli_agent_msg.go:138-146 | 三条路径可能到达 | TurnComplete 单路径 | 三 |
| 25 | endAgentTurn 保留 streamingMsgIdx | cli_turn.go:356-368 | PhaseDone-reply 中间态 | 无中间态 | 三 |
| 26 | endAgentTurn 保留 progressState | cli_turn.go:294-307 | 同上 | 同上 | 三 |
| 27 | dedupMessagesGuard | cli_turn.go:25-88 | 双路径产生重复 | 单路径无重复 | 三 |
| 28 | upsertMessageByTurn | cli_turn.go:448-479 | 同上 | 同上 | 三 |
| 29 | purgeZombieMessages | cli_turn.go:480-508 | 同上 | 同上 | 三 |
| 30 | findMessageByTurn in isPartial fallback | cli_agent_msg.go:110-116 | streamingMsgIdx 可能失效 | TurnComplete 不失效 | 三 |
| 31 | renderTurnBody content/Thinking dedup | cli_render_turn.go:130-136 | 同源数据双路径 | 引擎预构建 Thinking≠Content | 三 |
| 32 | handleAgentMessage reasoning fallback | cli_agent_msg.go:244-254 | reasoning 可能未设置 | TurnComplete 携带 | 三 |
| 33 | handleProgressDone pendingToolSummary 去重 | cli_update_progress.go:819-828 | 多 PhaseDone | 单 TurnComplete | 三 |
| 34 | cli_viewport.go:320 无条件 GotoBottom | cli_viewport.go | 缺少守卫 | 统一 maybeScrollToBottom | 四 |
| 35 | rc 4层缓存 25个失效点 | cli_block_cache.go | 层级过多 | 2层缓存 3个失效点 | 四 |

### 保留的必要防护（非防御性，异步系统固有）

| # | 防护 | 原因 |
|---|------|------|
| A | handleProgressMsg ChatID filter | progress 事件可能属于其他 session |
| B | handleAgentMessage session filter | outbound 可能属于其他 session |
| C | handleSuHistoryLoad session guard | RPC 回调可能 stale |
| D | handleHistoryReload session guard | 同上 |
| E | suLoading guard | session 切换中丢弃 stale 事件 |
| F | compReloading guard | compression 中阻止 auto-start |
| G | turnCancelled guard | cancel 后丢弃 stale progress |
| H | splashTickMsg gen 检查 | session 快切时 tick 链 stale |
| I | endAgentTurn turnID guard | stale endAgentTurn 不杀新 turn |
| J | flushMessageQueue chatID filter | 队列消息属于特定 session |
| K | handleTokenRefresh session guard | token refresh 可能 stale |
| L | cancelAckProcessed guard | cancel ack 去重 |

---

## 6. 风险与缓解

### 风险一：引擎层改动影响 server 端（Feishu/Web）

**设计修正（自审发现）**：阶段二和三**不需要改引擎**。所有合并逻辑在 CLIChannel 层完成：

- **阶段二（IterationSnapshot）**：CLIChannel 保留 Phase 1 的原地合并 ProgressState。当检测到 iteration 变化（对比 ProgressState.Iteration 与上次值），直接从 ProgressState 构建快照——因为原地合并保证所有字段都存在，无需 fallback 链。引擎零改动。
- **阶段三（TurnComplete）**：CLIChannel 层缓冲 PhaseDone 和 outbound message。两者都到达后（或 2s 超时），合并为 `TurnCompleteEvent` 发送给 Update loop。引擎零改动，Feishu/Web 零影响。

**结论**：全部 4 个阶段均为 CLI-only 改动，无引擎改动，无其他 channel 影响。风险大幅降低。

### 风险二：原地合并的锁竞争

**风险**：`ProgressState` 的 mutex 在高频流式事件下可能成为瓶颈。

**缓解**：流式事件频率约 10-50/s，mutex hold 时间极短（字段赋值）。BubbleTea Update 是串行的，drain goroutine 是唯一消费者。实测不会有竞争。

### 风险三：TurnCompleteEvent 到达前的 progress 事件

**风险**：turn 完成前的最后一个 progress 事件和 TurnCompleteEvent 之间可能有间隙。

**缓解**：TurnCompleteEvent 在 engine `Run()` 返回后发送，此时所有 progress 事件已发完。`progressFinalizer` 的 `ctx.Done()` 屏障保证 cancel 后无 stale progress。TurnCompleteEvent 是最后一个事件。

### 风险四：cancel 路径

**风险**：Ctrl+C 取消时，CLIChannel 需要在缓冲 PhaseDone + outbound 的模式中正确处理 cancel。

**缓解**：cancel 时引擎发送 PhaseDone（turnCancelled=true）作为终态。CLIChannel 检测到 `turnCancelled=true` 时不等待 outbound message，直接合成 `TurnCompleteEvent{Cancelled: true}` 发送。已完成的迭代数据从原地合并的 ProgressState 获取。outbound message（如果稍后到达）被丢弃或作为补充数据合并。这与当前 `handleProgressDone` cancel 路径的行为一致。

### 风险五：回退

**缓解**：每个阶段独立可验证。阶段一（CLI-only）可以先合入验证，不影响 engine。阶段二、三需要 engine 改动，可以在独立分支开发，充分测试后合入。如果新架构有问题，可以回退到 PhaseDone + outbound 路径（保留为 deprecated 代码）。

---

## 7. 验证方案

### 7.1 单元测试

- `TestProgressStateMerge`：流式事件和结构化事件原地合并不丢字段
- `TestTurnCompleteFinalize`：TurnCompleteEvent 原子 finalize 消息，无中间态
- `TestNoDuplicateMessages`：单路径创建消息，无重复
- `TestGotoBottomRespectsUserScroll`：所有路径统一守卫

### 7.2 集成测试

- DeepSeek 模型：reasoning 不闪缩（阶段一后验证）
- 普通模型：turn 结束无闪烁（阶段三后验证）
- Ctrl+C 取消：迭代数据保留（阶段三后验证）
- 多 session 快速切换：无 stale 状态污染（全阶段验证）
- 长对话（100+ 轮）：CPU profiling 确认无 fullRebuild 频发（阶段四后验证）

### 7.3 性能验证

- tick 路径 CPU 开销 ≤ 当前水平
- fullRebuild 触发频率降低（25→3 个触发点）
- 内存分配减少（缓存字段减少）

---

## 8. 实施顺序与依赖

```
阶段一 (CLI: 原地合并 ProgressState) ──── 独立可合入，无引擎改动
    │
    │ (依赖：需要 ProgressState 原地合并)
    ▼
阶段三 (CLI: 合并 TurnComplete) ───────── 依赖阶段一，最大 ROI
    │                                    (与阶段二并行)
    │
    ├─── 阶段二 (CLI: 简化快照) ────────── 依赖阶段一
    │
    ▼
阶段四 (CLI: 缓存简化) ──────────────── 依赖阶段一、三
```

**全部为 CLI-only 改动，无引擎改动，无 Feishu/Web 影响。**

**建议执行顺序**：阶段一 → 阶段三 → 阶段二 → 阶段四

- 阶段一消除 carryForward（9 个字段）+ 4 个 fallback map，ROI 最高
- 阶段三消除 turnDoneFlags + dedup 链 + guide 跳变，用户可感知改善
- 阶段二消除 snapshotIterationChange fallback 链，代码简化
- 阶段四缓存清理，性能改善

每个阶段独立可验证、可回退。
