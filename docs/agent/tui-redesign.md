# TUI 渲染系统重设计

> 目标：强线性一致性（消息不重复、不闪烁）+ O(1) 性能（与迭代数量无关）+ 消除 Thinking 命名债务

## 一、问题分析

### 1.1 命名债务：Thinking ≠ "思考"

当前代码中 `Thinking` 字名被用于表示 **assistant 的输出内容**，而非模型的 reasoning/thinking chain：

| 位置 | 字段名 | 实际含义 |
|------|--------|----------|
| `StructuredProgress.ThinkingContent` | assistant 的文本输出 | 应改名 `Content` |
| `ProgressEvent.Thinking` (JSON: `"thinking"`) | assistant 的文本输出 | 应改名 `Content` |
| `cliIterationSnapshot.Thinking` | 该迭代的 assistant 输出 | 应改名 `Content` |
| `HistoryIteration.Thinking` | 该迭代的 assistant 输出 | 应改名 `Content` |
| `SubAgentProgressDetail.Thinking` | SubAgent 的输出 | 应改名 `Content` |
| `cliMessage.thinking` | **存储的是 reasoning 文本** | 应改名 `Reasoning` |

混乱原因：早期只有 `Thinking` 一个字段表示 "AI 想的东西"，后来区分了 reasoning（推理链）和 content（输出正文），但字段名未跟进。

注意：`ProgressEvent` 中已有 `Content string` (JSON `"content"`) 字段，但它是旧版遗留、语义模糊，`Thinking` 是后来加的同义字段。合并时 JSON tag 统一用 `"content"`，旧 `"thinking"` tag 在反序列化端做兼容（首次出现时）。

### 1.2 一致性问题根源：状态碎片化

当前系统维护了 **7 层独立状态**，彼此通过脆弱的转换逻辑连接：

```
┌─────────────────────────────────────────────────────────┐
│  progressState.current   ← 实时结构化进度 (可变)         │
│  progressState.iterations ← 已完成迭代快照 (append-only) │
│  streamingMsgIdx          ← messages[] 中的流式索引       │
│  messages[]               ← 所有消息 (混合不可变+可变)    │
│  typing/turnCancelled     ← 回合状态标志                  │
│  renderCache.*            ← 渲染缓存 (20+ 字段)           │
│  lastThinking/lastIter    ← 散落的辅助状态                │
└─────────────────────────────────────────────────────────┘
```

**问题：`messages[]` 中混合了不可变消息和可变消息**。流式消息（`streamingMsgIdx`）在 turn 期间不断被修改，完成后变为不可变。这个"可变→不可变"转换发生在多个代码路径中（`handleProgressDone` 的 3 个分支 + `handleAgentMessage` + cancel 路径），每个路径都有自己的状态管理逻辑。

**防御层堆叠**：因为转换路径多、状态碎片化，系统叠加了大量防御代码：

| 防御层 | 文件 | 代码量 | 存在原因 |
|--------|------|--------|----------|
| `dedupMessagesGuard` | cli_viewport.go | O(N)/帧 | catch 重复消息（根因：多路径 append） |
| `upsertMessageByTurn` + purge zombies | cli_agent_msg.go | ~40行 | catch 重复（根因：多路径 append） |
| `alreadySnapped` guard | cli_update_progress.go | ~15行 | catch 重复快照（根因：snapshot 时机不确定） |
| `snapshotIterationChange` fallback chain | cli_update_progress.go | ~70行 | catch 数据不匹配（根因：prev 来源不确定） |
| `handleProgressDone` 3 路径 | cli_update_progress.go | ~200行 | cancel/normal/agent 三种完成方式 |
| `mergeProgressState` stream field preservation | cli_update_progress.go | ~50行 | catch 流式字段丢失（根因：whole-replacement） |
| `renderTurnBody` dedup | cli_render_turn.go | ~15行 | catch 内容重复渲染（根因：Thinking=Content 歧义） |

每一层防御本身引入新的边界条件，最终形成 **防御栈 > 业务逻辑** 的局面。

### 1.3 性能问题

| 操作 | 复杂度 | 位置 | 说明 |
|------|--------|------|------|
| `fullRebuild` | O(N) | cli_viewport.go | width 变化时全量 glamour 渲染所有消息 |
| `dedupMessagesGuard` | O(N)/帧 | cli_viewport.go | 每帧扫描所有消息做去重 |
| `renderTurnBody` | O(I) | cli_render_turn.go | I = 迭代数，每次渲染遍历所有迭代 |
| `updateStreamingOnly` width change | O(I) | cli_viewport.go | 宽度变化时重新渲染所有已完成迭代 |
| `snapshotIterationChange` alreadySnapped scan | O(I) | cli_update_progress.go | 每次迭代切换扫描已有快照 |
| `setViewportContent` slow path | O(N*W) | cli_viewport.go | N=行数, W=宽度，全量 hardWrap |
| `progressCh` coalescing | 丢事件 | cli.go | buffer=1 channel 丢弃结构化事件 |

**关键瓶颈**：`fullRebuild` 在 width 变化时触发，对 N 条消息调用 glamour.Render，是已知的最大性能热点。

---

## 二、新架构设计

### 2.1 核心原则

1. **不可变消息**：消息创建后永不修改。流式消息不是 `messages[]` 中的元素，而是独立的 `turn` 对象。
2. **单一写入点**：每条消息只有一个创建路径。消除多路径 append → 消除重复 → 消除去重防御。
3. **线性日志**：所有状态变更按时间顺序追加，不支持修改历史。
4. **O(1) 渲染**：每帧只重新渲染活跃 turn，历史消息缓存永不失效（除非 width 变化）。

### 2.2 状态模型

```go
// turnState 管理一个完整的 agent 回合。
// 在 turn 期间，它是唯一可变状态；turn 结束后转为不可变 cliMessage。
type turnState struct {
    active  bool          // 是否有活跃 turn
    turnID  uint64        // 唯一标识

    // 流式消息——turn 期间的唯一可变对象
    streaming *streamingMessage
}

// streamingMessage 是 turn 期间的流式状态。
// 由三部分组成：已完成迭代（append-only）+ 活跃迭代（可变）+ 元数据
type streamingMessage struct {
    timestamp time.Time

    // 已完成迭代——append-only，创建后不可变
    // 每个迭代维护自己的渲染缓存
    iterations []*iterationBlock

    // 活跃迭代——当前正在进行的 LLM 调用
    // 每帧从这里渲染最新内容
    live *liveBlock

    // 最终内容——reply 到达后填充
    finalContent string
    finalReason  string

    // 渲染缓存
    completedLines []string // 已完成迭代的渲染行（append-only）
    completedWidth int
    liveLines      []string // 活跃迭代的渲染行（每帧重建）
}

// iterationBlock 是一个已完成的迭代快照。创建后不可变。
type iterationBlock struct {
    iteration int
    content   string         // assistant 输出（原 Thinking）
    reasoning string         // 推理链
    tools     []ToolProgress
    elapsedMs int64
    // 渲染缓存
    renderedLines []string   // glamour 渲染后的行
    renderWidth   int
}

// liveBlock 是当前活跃迭代的实时状态。
type liveBlock struct {
    iteration    int
    phase        string      // thinking / tool_exec / done
    streamContent string     // 流式 assistant 内容
    streamReason  string     // 流式推理
    activeTools  []ToolProgress
    streamingTools []ToolProgress
    tokenUsage   *TokenUsageSnapshot
}
```

### 2.3 消息模型

```go
// cliMessage 只存储不可变消息。
// 流式消息不再是 cliMessage，而是 turnState.streaming。
type cliMessage struct {
    role       string        // "user" | "assistant" | "system" | "tool_summary"
    content    string
    timestamp  time.Time
    turnID     uint64

    // assistant 消息的迭代历史（turn 完成时从 streamingMessage 拷贝）
    iterations []iterationBlock

    // reasoning（原 cliMessage.thinking 字段，改名消除歧义）
    reasoning  string

    // 渲染缓存（一旦渲染完成，除非 width 变化，永不重新渲染）
    rendered    string
    renderWidth int
    dirty       bool
}
```

### 2.4 数据流：后端 → TUI

```
后端引擎 (agent/engine_*.go)
  │
  ├─ StructuredProgress.Content (原 ThinkingContent)
  ├─ StructuredProgress.ReasoningContent
  ├─ StructuredProgress.ActiveTools / CompletedTools
  │
  ▼
ProgressEvent (protocol/events.go)
  │  Content   string  (原 Thinking，JSON "content")
  │  Reasoning string  (JSON "reasoning")
  │  StreamContent / ReasoningStreamContent / StreamingTools
  │  Seq uint64  (单调递增)
  │
  ▼
handleProgressMsg (单一入口)
  │  1. Seq 单调检查（丢弃乱序/重复）
  │  2. ChatID 过滤
  │  3. 调用 turn.apply(event)（唯一状态变更点）
  │  4. 触发渲染
  │
  ▼
turnState.apply(event)
  │  结构化事件 → mergeIntoLive(event)
  │  迭代切换   → snapshotAndAppend()
  │  流式事件   → updateLiveStream(event)
  │  PhaseDone  → finalizeTurn()
  │
  ▼
渲染（O(1)）
  cachedHistoryLines + renderStreamingMessage()
```

### 2.5 回合生命周期（极简化）

```
startTurn():
    turn.active = true
    turn.turnID++
    turn.streaming = newStreamingMessage()
    → 创建一个 placeholder assistant 消息到 streaming.live

onProgress(event):
    if event.Seq <= turn.lastSeq: return    // 单调保证
    turn.lastSeq = event.Seq
    turn.streaming.apply(event)              // 唯一变更点

onReply(content):                             // 最终 assistant 回复
    turn.streaming.finalize(content)
    messages.append(turn.streaming.toMessage())  // append-only
    turn.streaming = nil
    turn.active = false

onCancel():
    turn.streaming.finalizeCanceled()
    messages.append(turn.streaming.toMessage())  // append-only
    turn.streaming = nil
    turn.active = false
```

**对比当前系统**：
- 无 `snapshotIterationChange`（70行）
- 无 `handleProgressDone` 3 路径（200行）
- 无 `mergeProgressState` stream field preservation（50行）
- 无 `dedupMessagesGuard`（O(N)/帧）
- 无 `upsertMessageByTurn` + purge zombies（40行）

### 2.6 O(1) 渲染架构

```
viewport 内容 = [历史缓存行] + [流式消息行] + [动态后缀]

每帧渲染：
  ┌─────────────────────────────────────┐
  │ 历史消息渲染缓存 (O(0) - 直接复用)    │
  │  m.rc.histLines []string             │
  │  m.rc.histGen uint64                 │
  ├─────────────────────────────────────┤
  │ 已完成迭代渲染缓存 (O(Δ) - 仅渲染增量)│
  │  streaming.completedLines []string   │
  │  新迭代完成时 append，不重建已有行    │
  ├─────────────────────────────────────┤
  │ 活跃迭代渲染 (O(1) - 单次渲染)        │
  │  从 liveBlock 实时渲染               │
  ├─────────────────────────────────────┤
  │ 动态后缀 (rewind block 等)           │
  └─────────────────────────────────────┘

Width 变化时：
  历史缓存失效 → 逐条重新渲染（O(N)，但仅发生在 resize）
  已完成迭代缓存失效 → 逐迭代重新渲染（O(I)，仅 resize）
  → 正常 turn 期间不受影响
```

**关键保证**：
- 正常 turn 期间（不 resize）：每帧 O(1)，只渲染活跃迭代的单次内容
- 新迭代完成：O(1) 增量，只渲染新迭代
- 历史消息：O(0)，缓存直接复用
- 无 `dedupMessagesGuard`、无 `fullRebuild`

### 2.7 消除重复的架构保证

| 当前 bug 场景 | 新架构如何杜绝 |
|---------------|---------------|
| PhaseDone + handleAgentMessage 都创建消息 | 消息只在 `onReply` 中创建一次，PhaseDone 只 finalize streaming |
| cancel 路径创建消息 | cancel 和正常完成走同一 `finalize` → `append` 路径 |
| auto-start 创建重复 assistant | streaming 是 turn 的子对象，不存在 "重复创建" |
| snapshotIterationChange 重复快照 | 迭代切换时 snapshot 由 `apply` 内部保证只执行一次 |
| renderTurnBody 内容重复 | `Content` 命名消除歧义，迭代内容和最终内容是同源数据 |

### 2.8 消除闪烁的架构保证

| 当前 bug 场景 | 新架构如何杜绝 |
|---------------|---------------|
| fullRebuild 导致全量重渲染 | 流式消息不进入 messages[]，不会触发 fullRebuild |
| cache 提前缓存不完整内容 | 已完成迭代缓存只在迭代结束时写入，活跃迭代每帧重建 |
| endAgentTurn 清除 streamingMsgIdx | streaming 是 turn 的独立子对象，endTurn 才清除 |
| PhaseDone → handleAgentMessage 间的空白期 | streaming 在 onReply 之前一直存在，保持显示 |
| guide 颜色跳变 | streaming.isLive 标志在 turn 期间为 true，onReply 后置 false |

---

## 三、实施计划

### Phase 1: 命名统一（Thinking → Content）

**目标**：消除 `Thinking` 命名歧义，所有 "assistant 输出内容" 统一叫 `Content`。

修改清单：
1. `agent/progress.go`: `StructuredProgress.ThinkingContent` → `Content`
2. `protocol/events.go`: `ProgressEvent.Thinking` → `Content`（JSON tag `"content"`，兼容旧 `"thinking"` tag）
3. `protocol/events.go`: `HistoryIteration.Thinking` → `Content`
4. `channel/cli/cli_types.go`: `cliIterationSnapshot.Thinking` → `Content`
5. `channel/cli/cli_model.go`: `cliMessage.thinking` → `reasoning`（它存的是 reasoning 文本）
6. `agent/engine_run.go`, `engine_wire.go`, `interactive.go`: 所有 `ThinkingContent` 引用改为 `Content`
7. `agent/progress.go`: `SubAgentProgressDetail.Thinking` → `Content`

### Phase 2: 状态模型重构

**目标**：将流式消息从 `messages[]` 中分离，消除多路径 append。

1. 新建 `channel/cli/turn_state.go`：定义 `turnState`、`streamingMessage`、`iterationBlock`、`liveBlock`
2. 重写 `cli_update_progress.go`：用 `turn.apply(event)` 替换 `mergeProgressState` + `snapshotIterationChange` + `handleProgressDone`
3. 重写 `cli_agent_msg.go`：`handleAgentMessage` 简化为 `onReply(content)` → `finalize` → `append`
4. 删除 `dedupMessagesGuard`、`upsertMessageByTurn`、purge zombies
5. 删除 `progressState.iterations`、`streamReasoningByIter`、`lastThinking`、`lastIter` 等散落状态

### Phase 3: 渲染管线重构

**目标**：O(1) 每帧渲染，O(1) 迭代增量。

1. 重写 `updateViewportContent`：简化为 `histLines + streamingLines + dynamic`
2. 重写 `updateStreamingOnly`：从 streamingMessage 渲染，completed 缓存只增不重建
3. 简化 `renderTurnBody`：从 `[]iterationBlock` + `liveBlock` 渲染
4. 删除 `renderCache` 中的流式缓存字段（streamAllBuf、streamPrefix* 等），由 streamingMessage 接管

### Phase 4: 边界场景处理

- Session switch（/su, /chat）：保存/恢复 turnState
- History reload（compression）：直接替换 messages[]，不影响活跃 turn
- Cancel（Ctrl+C）：统一走 finalize → append 路径
- Reconnect：从 IterationHistory 恢复 turnState

### Phase 5: 测试验证

- 零重复测试：各种事件顺序组合下，消息不重复
- 零闪烁测试：视觉 diff 验证帧间无突变
- 性能基准：1000 迭代 turn 的帧渲染时间 < 1ms

---

## 四、传输层重设计：Push → Pull（Shared State Snapshot）

### 4.1 当前架构的根本错误

当前系统把 **流式渲染** 当作 **事件流推送** 来实现：

```
LLM 每个 token chunk（~20-100次/秒）
  → onContent callback
  → SendProgress(ProgressEvent{StreamContent: content})
  → progressCh (buffer=1, 80行 coalescing/merge 逻辑)
  → handleProgressDrain goroutine
  → asyncCh (buffer=256, drop on full)
  → handleAsyncDrain goroutine
  → program.Send
  → handleProgressMsg (50+ 行守卫)
  → mergeProgressState (80行字段合并)
  → snapshotIterationChange (70行快照逻辑)
  → updateViewportContent
```

**问题**：CLI tick 是 100ms（10fps）。100ms 内 2-10 个 stream 事件全部走完整管道，只有最后一个被渲染。中间事件全是浪费的 CPU + 丢数据的温床。

**不同客户端渲染频率不同**：CLI 10fps，Web 可能 60fps，飞书卡片每 5s 刷新一次。后端把更新频率硬编码在推送管道中，是架构错误。

### 4.2 核心洞察：这是状态读取问题，不是事件流问题

TUI 每帧只需要看到 **当前最新状态**，不需要知道中间发生了什么。就像浏览器的 `requestAnimationFrame`——它不需要收到所有中间帧，只需要在每次重绘前读取最新状态。

### 4.3 新模型：Shared State Snapshot

```
后端引擎维护一个 atomic 指针：

  TurnState（单一可变状态对象）
    ├─ Completed []IterationSnapshot  (append-only)
    └─ Live StreamState               (覆盖写)

后端写入：O(1) 原子覆盖，每个 stream chunk 直接写
客户端读取：O(1) 原子读取，按自己的 tick 频率

没有 channel，没有 buffer，没有 drop，没有 merge。
```

### 4.4 数据结构

```go
// TurnState 是后端维护的单一可变状态。
// 通过 atomic.Pointer 发布，客户端无锁读取。
type TurnState struct {
    TurnID    uint64
    Seq       uint64    // 单调递增版本号（客户端用它检测变化）
    Phase     string    // thinking/tool_exec/done

    // 已完成迭代——append-only，后端在迭代切换时追加
    Completed []IterationSnapshot

    // 活跃迭代的实时状态——覆盖写，每个 stream chunk 更新
    Live      LiveState

    // 元数据
    TokenUsage *TokenUsageSnapshot
    Todos      []TodoProgressItem
    SubAgents  []SubAgentNode
    CWD        string

    // 回合元信息
    FinalContent string   // turn 完成后的 assistant 输出
    FinalReason  string   // turn 完成后的 reasoning
}

// LiveState 是活跃迭代的实时流式状态。
type LiveState struct {
    Iteration      int
    Content        string          // 累积 assistant 输出（原 ThinkingContent）
    Reasoning      string          // 累积推理链
    ActiveTools    []ToolProgress
    StreamingTools []ToolProgress
    StreamTokens   int64
}

// IterationSnapshot 是已完成的迭代快照。创建后不可变。
type IterationSnapshot struct {
    Iteration int
    Content   string         // 原 Thinking
    Reasoning string
    Tools     []ToolProgress
    ElapsedMs int64
}
```

### 4.5 后端写入（O(1)，无 channel）

```go
// 每个 LLM stream content delta
func (a *Agent) updateStreamContent(chatID, content string) {
    a.updateTurnState(chatID, func(ts *TurnState) {
        ts.Live.Content = content
    })
}

// 迭代切换——追加已完成迭代
func (a *Agent) completeIteration(chatID string, snapshot IterationSnapshot) {
    a.updateTurnState(chatID, func(ts *TurnState) {
        ts.Completed = append(ts.Completed, snapshot)
        ts.Live.Iteration = snapshot.Iteration + 1
        ts.Live.Content = ""
        ts.Live.Reasoning = ""
        ts.Live.ActiveTools = nil
    })
}

// CAS 更新——无锁
func (a *Agent) updateTurnState(chatID string, fn func(*TurnState)) {
    key := "turn:" + chatID
    for {
        old := a.turnStates.Load(key)
        if old == nil {
            // turn 未开始——不应该发生，但防御性处理
            return
        }
        oldTS := old.(*atomic.Pointer[TurnState]).Load()
        newTS := oldTS.Clone()   // 浅拷贝
        fn(newTS)                // 修改
        newTS.Seq = oldTS.Seq + 1
        if old.(*atomic.Pointer[TurnState]).CompareAndSwap(oldTS, newTS) {
            return
        }
        // CAS 失败——重试
    }
}
```

### 4.6 客户端读取

**本地模式（CLI 直连 agent）**：
```go
// CLI tick（100ms = 10fps）→ 直接读 atomic 指针
func (m *cliModel) handleTickMsg() {
    ts := m.agent.GetTurnState(m.chatID)  // atomic.Pointer.Load()
    if ts == nil || ts.Seq == m.lastRenderedSeq {
        return  // 无变化
    }
    m.lastRenderedSeq = ts.Seq
    m.renderFromSnapshot(ts)
}
```

**远程模式（CLI over WebSocket）**：

不需要高频 WS 推送。两个方案：

- **方案 A（推荐）：客户端 RPC 轮询**——CLI tick 时调用已有的 `GetActiveProgress` RPC，只是从 "reconnect 恢复" 变成 "常规 tick 读取"。100ms 一次 RPC 在本地/局域网延迟 <1ms。
- **方案 B：后端节流推送**——后端自己节流（100ms 内最多推一次最新快照），但这又把频率控制放回了后端。

方案 A 更干净：后端不知道客户端何时要渲染，客户端完全自主。

**Web 模式**：Web 自己的 tick（16ms = 60fps）通过 WS 轮询或节流推送。

**飞书模式**：飞书卡片刷新有 5s 限制——当前代码已经这样做了（定时拉取 GetActiveProgress）。

### 4.7 消除的复杂性

新模型直接删除以下代码：

| 删除的机制 | 代码位置 | 行数 | 存在原因（现已不需要） |
|-----------|----------|------|----------------------|
| `progressCh` (buffer=1) + coalescing | cli.go:42 | 80行 | channel 丢事件需要 merge |
| `handleProgressDrain` | cli.go:1002 | 35行 | progressCh → asyncCh drain |
| `SendProgress` merge 逻辑 | cli.go:472 | 130行 | channel 满时 merge stream fields |
| `asyncCh` for progress | cli.go:44 | — | progress 不再走 channel |
| `handleProgressMsg` 守卫 | cli_update_progress.go:178 | 280行 | stream-only 检测, auto-start, merge |
| `mergeProgressState` | cli_update_progress.go:55 | 120行 | stream field preservation |
| `snapshotIterationChange` | cli_update_progress.go:542 | 70行 | 从事件检测迭代切换 |
| `handleProgressDone` | cli_update_progress.go:614 | 210行 | PhaseDone 的 3 路径 |
| stream callback `SendProgress` | engine_wire.go:1989 | 80行 | stream → channel 推送 |
| stream callback `SendProgress` | interactive.go:331 | 60行 | SubAgent stream 推送 |

**总计删除约 1000+ 行防御/合并/drain 代码。**

### 4.8 需要保留 Push 的场景

只有 **状态变更信号** 需要 push（不传数据，只通知客户端 "去读取"）：

```go
// 后端在关键节点发送信号（通过极小的 channel 或 atomic flag）
type TurnSignal struct {
    Type   string  // "phase_change" | "turn_done" | "history_compacted"
    TurnID uint64
}
```

这些信号：
- PhaseDone → "turn 完成了，去读最终状态"
- HistoryCompacted → "压缩完成，重新加载历史"
- **不携带数据**——客户端收到信号后 RPC 读取完整快照

对于本地模式，甚至信号都不需要——tick 自然会读到最新状态。信号主要用于远程模式减少延迟（turn 完成后立即通知，不用等下一个 tick）。

### 4.9 并发安全保证

```
后端写入：atomic.Pointer[TurnState].CompareAndSwap
  → 无锁，多写者安全（CAS 重试）

客户端读取：atomic.Pointer[TurnState].Load
  → 无锁，读到的是完整一致的快照指针
  → 永远不会读到 "写了一半" 的状态

迭代快照追加：
  CAS 循环内 Clone → append → CAS swap
  → append 只影响新拷贝，不影响正在被读取的旧快照
```

### 4.10 完整的数据流对比

```
=== 当前（Push 模型）===

LLM chunk → callback → SendProgress → progressCh(1)
  → coalescing merge(80行) → asyncCh(256) → drain goroutine
  → program.Send → handleProgressMsg(280行)
  → mergeProgressState(120行) → snapshotIterationChange(70行)
  → handleProgressDone(210行) → updateViewportContent

管道：7 层，每层都可能丢数据/创建重复

=== 新方案（Pull 模型）===

LLM chunk → atomic.Write(TurnState.Live.Content)
  → 完成，O(1)

CLI tick(100ms) → atomic.Read(TurnState) → render
  → 完成，O(1)

管道：写入 1 步 + 读取 1 步
```

### 4.11 遗留兼容：渐进迁移

不需要一次性删除所有 push 代码。迁移路径：

1. **Phase A**：后端在现有 push 旁同时维护 `atomic.Pointer[TurnState]`。不影响现有行为。
2. **Phase B**：CLI 改为 tick 时从 TurnState 读取（而非从 progressCh）。验证一致性。
3. **Phase C**：删除 progressCh / asyncCh progress 路径 / SendProgress merge 逻辑。
4. **Phase D**：Web/飞书迁移到各自频率的 Pull。
