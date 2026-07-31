# TUI 统一渲染重构计划

## 目标

消除 idle/busy 视图分裂，将 agent turn 渲染为**一条大的 assistant 消息**。

## 当前 vs 目标

### 当前架构

```
Turn 进行中（busy）：
┌─────────────────────────────────┐
│ history messages (cached)       │  ← fullRebuild → renderMessage
├─────────────────────────────────┤
│ progress block (独立面板)        │  ← renderProgressBlock
│   iteration history (dimmed)    │
│   current iteration:            │
│     reasoning (typewriter+cursor)│
│     thinking                    │
│     completed tools             │
│     stream content (typewriter) │
│     active tools (spinner)      │
│     SubAgent tree               │
├─────────────────────────────────┤
│ rewind block (可选)             │
└─────────────────────────────────┘

Turn 结束后（idle）：
┌─────────────────────────────────┐
│ user message (右对齐气泡)       │
│ tool_summary message            │  ← renderMessage(tool_summary)
│   iterations + tools + body     │
│ assistant message               │  ← renderMessage(assistant) → glamour md
│   content (只有最后 iter)       │
└─────────────────────────────────┘
```

### 目标架构

```
Turn 进行中（busy）和结束后（idle）统一：
┌─────────────────────────────────┐
│ user message (右对齐气泡)       │
│ ┊ iter#0 content (glamour md)   │  ← 已完成 iter 的 content，stream 也渲染 md
│ ┊ [Shell ✓ Read ✓]             │  ← tools 内联标签（可展开）
│ ┊                               │
│ ┊ ╭ Reasoning ────────╮        │  ← reasoning 可折叠，默认折叠
│ ┊ │ reasoning text... │        │     方框左上角线条替换为 "Reasoning"
│ ┊ ╰───────────────────╯        │
│ ┊                               │
│ ┊ iter#1 content (流式 md)      │  ← 当前 iter 的 content（流式打字）
│ ┊ 🔄 Grep ●                     │  ← 当前活跃工具（带 spinner）
│ ┊   └─ explore [mem-1]: ...    │  ← SubAgent tree 保留
└─────────────────────────────────┘

Turn 结束后：格式完全相同，去掉 spinner，内容不再流式。
不再有 tool_summary 和 assistant 两条消息，只有一条 assistant 消息。
```

## 设计决策

1. **消除 `tool_summary` 消息类型**：iterations/tools 全部内联到 assistant 消息中
2. **消除 `renderProgressBlock`**：不再有独立的 progress panel
3. **统一为单条 assistant 消息**：所有 iter 的 content + tools 都是一条消息
4. **流式 content 渲染为 md**：stream content 和 reasoning 都用 glamour 渲染
5. **去掉 typewriter 光标**：md 渲染后宽度固定，光标不需要
6. **Reasoning 可折叠**：特殊方框，默认折叠，左上角线条替换为 "Reasoning"
7. **Tools 内联标签**：`[Shell ✓ Read ✓]` 格式，可展开查看输出
8. **SubAgent tree 保留**：当前活跃的 SubAgent 仍显示树形结构

## 实施阶段

### Phase 1: 数据模型重构

**目标**：将 `iterationHistory` 数据统一到 `cliMessage` 中

1.1 修改 `cliMessage` 结构体：
- 添加 `iterations []cliIterationSnapshot` 字段（已有，tool_summary 用）
- assistant 消息的 iterations 存储所有已完成的 iterations
- 去掉独立的 `tool_summary` 角色的必要性

1.2 修改 iteration 快照管理：
- `snapshotIterationChange()` 不再追加到 `m.iterationHistory`
- 而是追加到当前 assistant 消息的 `iterations` 中
- assistant 消息在 turn 开始时创建（而不是结束时）

1.3 修改 `handleProgressDone()`：
- 不再创建 `tool_summary` 消息
- 快照最后一个 iteration 到 assistant 消息

1.4 修改 `handleAgentMessage()`：
- 不再创建 `tool_summary` 消息
- 直接更新已有 assistant 消息的 content

### Phase 2: 渲染统一

**目标**：消除 progress panel，统一为 assistant 消息渲染

2.1 新建 `cli_render_turn.go`：单次 turn 渲染

核心函数：
```go
// renderTurnMessages 渲染一条 assistant 消息的所有 iterations
// 包含已完成 iterations + 当前 iteration
func (m *cliModel) renderTurnMessages(msg *cliMessage) string

// renderIteration 渲染单个已完成 iteration
func (m *cliModel) renderIteration(iter cliIterationSnapshot, width int) string
// → content (glamour md)
// → [Tool1 ✓ Tool2 ✓] tags（可展开）
// → reasoning（可折叠方框）

// renderCurrentIteration 渲染当前正在进行的 iteration
func (m *cliModel) renderCurrentIteration(width int) string
// → content (glamour md, 流式)
// → active tools (spinner)
// → SubAgent tree
```

2.2 修改 `updateViewportContent()`：
- 去掉 `renderProgressBlock()` 调用
- 改为：历史消息 + 当前 assistant 消息（包含所有 iterations + live progress）

2.3 修改 `fullRebuild()`：
- assistant 消息渲染时，调用 `renderTurnMessages()`
- 去掉 `tool_summary` 渲染分支

### Phase 3: 消除旧代码

3.1 删除 `renderProgressBlock()` 和相关函数
3.2 删除 `renderHistoryRange()` — 不再有 dimmed history
3.3 清理 `renderCache` 中 progress 相关的缓存字段
3.4 清理 `handleProgressMsg()` 中与旧 progress panel 相关的逻辑

### Phase 4: 视觉打磨

4.1 Reasoning 折叠方框样式设计
4.2 Tool 标签展开/折叠交互
4.3 SubAgent tree 样式统一
4.4 流式 md 渲染性能优化

## 关键文件变更

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `cli_model.go` | 修改 | cliMessage 结构体、turn 状态管理 |
| `cli_agent_msg.go` | 重写 | 消息创建逻辑（提前创建 assistant 消息） |
| `cli_progress.go` | 删除/重写 | 消除 progress panel，改为 inline 渲染 |
| `cli_cache.go` | 简化 | 去掉 progress 相关缓存 |
| `cli_viewport.go` | 简化 | 统一 viewport 更新路径 |
| `cli_msg_render.go` | 修改 | 去掉 tool_summary 分支，统一 assistant 渲染 |
| `cli_render_turn.go` | 新建 | turn 渲染核心逻辑 |
| `cli_block_cache.go` | 简化 | 去掉 progressBlock/reasoning/stream 等 cache |
| `cli_update_handlers.go` | 修改 | progress 事件处理简化 |
