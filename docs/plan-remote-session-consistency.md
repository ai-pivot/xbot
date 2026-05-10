# 计划：Remote TUI 会话切换状态一致性修复

> 生成时间：2026-05-09
> 状态：待确认

## 背景与目标

Remote TUI 模式下频繁切换 sidebar 会话时，typing/busy 状态不一致，导致本该排队的消息被直接渲染。需要添加严格的数据一致性保证，确保无论怎么切 session，客户端和服务器状态始终一致。

## 根因分析

### 核心竞态场景

**场景：用户在 Session A（agent turn 进行中）→ 切到 Session B → 快速切回 Session A**

时序问题链：

```
1. Session A: typing=true, progress=thinking, agentTurnID=5
2. 用户点击 sidebar → saveCurrentSession(A) → 切到 B
3. restoreSession(B) → typing=false (idle) → m.inputReady=true
4. handleSuHistoryLoad(B) 发起 RPC...
5. 服务器仍在推送 Session A 的 progress_structured 事件
6. 用户快速切回 Session A
7. saveCurrentSession(B) → restoreSession(A)
   - 恢复 typing=true, agentTurnID=5, progress=thinking
   - m.inputReady = true (switchToSession line 3504 强制设置)
8. 但此时 Session A 的 progress 事件可能在 restoreSession 之前就已经到达
   - handleProgressMsg ChatID 过滤通过了（因为 m.chatID 已经切回来了）
   - 但 turnCancelled 可能因为中间的切换被设置了旧值
9. handleSuHistoryLoad(A) 的 RPC 返回
   - activeProgress 权威判定 → 但可能和 restoreSession 恢复的状态冲突
10. 如果 agent turn 在切回 A 之后结束：
    - handlePhaseDone → endAgentTurn(turnID=5) → typing=false
    - needFlushQueue=true (如果 A 有排队消息)
    - 但 inputReady 已经是 true
    - flushMessageQueue → sendMessageFromQueue → sendMessage → 直接发送
    - 而不是排队！因为此时 inputReady=true
    
    关键问题：flushMessageQueue 在 switchToSession 设置 inputReady=true 之后执行
```

### 具体竞态 #1：flushMessageQueue 绕过排队检查

`flushMessageQueue()` → `sendMessageFromQueue()` → `sendMessage()` → `startAgentTurn()`

这条路径不检查 `inputReady`，直接发送。而 `handleEnterKey()` 中排队消息的条件是 `!m.inputReady`。

在快速切换场景中：
1. Session A 有排队消息 messageQueue=[msg1]
2. 用户切到 B → 切回 A → restoreSession 恢复 messageQueue=[msg1]
3. handleSuHistoryLoad 尚未到达，但旧 turn 的 PhaseDone 先到达
4. endAgentTurn → needFlushQueue=true → inputReady=true
5. handleTickMsg → flushMessageQueue → 发送 msg1

**结果：msg1 被直接发送而非排队。** 但此时 agent 可能还在处理中（服务器和客户端状态不一致），msg1 直接渲染到了 UI。

### 具体竞态 #2：restoreSession 恢复的 typing 状态与实际不符

`restoreSession()` 恢复了旧的 `typing=true` + `agentTurnID=5`，但服务器可能已经完成了 turn 5。`handleSuHistoryLoad` 是异步的，在它到达之前：
- 客户端认为 typing=true，不会让用户输入
- 但服务器已经 idle
- PhaseDone 的 turnID 不匹配（因为中间可能有新 turn）→ 被忽略
- 客户端永远停留在 typing 状态

### 具体竞态 #3：handleProgressMsg auto-start 路径

```go
if !m.typing && msg.payload != nil && msg.payload.Phase != "done" {
    m.startAgentTurn()  // auto-start
}
```

切到 Session B 时，如果 B 有活跃的 agent turn：
1. restoreSession → typing=false (B 没有保存状态)
2. 服务器推送 B 的 progress_structured (thinking)
3. handleProgressMsg ChatID 匹配 → auto-start turn
4. 但 handleSuHistoryLoad(B) 还没到达
5. 此时 startAgentTurn 设置 typing=true，但消息历史还是空的
6. 后续的 agent 回复直接渲染到空的消息列表上

## 修复方案

### 策略：防御性重置 + 异步权威校验 + 严格的状态守卫

核心原则：
1. **切换时强制清理** — switchToSession 时强制重置所有 turn 状态
2. **异步加载期间禁止 action** — suLoading=true 时禁止 flush/input
3. **服务器权威** — handleSuHistoryLoad 的结果覆盖所有客户端状态
4. **turnID monotonic guard** — flushMessageQueue 增加 turnID 一致性检查

### 修改点

| 文件 | 修改 | 说明 |
|------|------|------|
| `cli_panel.go:switchToSession` | 强制重置 turn 状态 | 切换时清空 typing/progress/queue flush，强制等 RPC |
| `cli_update_handlers.go:handleTickMsg` | suLoading 守卫 | suLoading 期间禁止 flushMessageQueue |
| `cli_update_handlers.go:handleProgressMsg` | suLoading 守卫 | suLoading 期间禁止 auto-start turn |
| `cli_update_handlers.go:handlePhaseDone` | chatID 一致性检查 | PhaseDone 的 ChatID 必须匹配当前 session |
| `cli_helpers.go:flushMessageQueue` | 增加状态检查 | 确保在安全状态下才 flush |
| `cli_message.go:handleAgentMessage` | suLoading 守卫 | suLoading 期间丢弃 agent 回复（RPC 会处理） |

### 详细步骤

#### Step 1: switchToSession — 强制重置 turn 状态

在 `restoreSession()` 之后、`m.inputReady = true` 之前，强制重置：

```go
// 在 remote 模式下，切换会话时强制进入"加载中"状态。
// 不信任 restoreSession 恢复的 typing/progress 状态，
// 等 handleSuHistoryLoad (RPC) 返回后再决定。
if m.isRemote() {
    m.typing = false
    m.progress = nil
    m.needFlushQueue = false
    m.turnCancelled = false
    m.fastTickActive = false
    m.typewriterTickActive = false
}
m.inputReady = false  // 加载完成前不允许输入
```

注意：`suLoading = true` 已经在设置了。关键是 inputReady 也要 false。

#### Step 2: handleTickMsg — suLoading 守卫

在 `needFlushQueue` 检查前增加：

```go
if m.needFlushQueue && !m.typing && !m.suLoading && len(m.messageQueue) > 0 {
```

#### Step 3: handleProgressMsg — suLoading 守卫

auto-start 路径增加 suLoading 检查：

```go
if !m.typing && !m.suLoading && msg.payload != nil && msg.payload.Phase != "done" {
    m.startAgentTurn()
}
```

同时在 suLoading 期间，仅接受 PhaseDone 事件（用于清理可能的旧 turn 状态），非 PhaseDone 事件静默丢弃。

#### Step 4: handleSuHistoryLoad — 完善权威校验

在设置 typing=true 和恢复 progress 之后，确保 turnDoneFlags 正确：
- 如果 acceptProgress=true，需要确保 turnDoneFlags 没有 stale entry
- 如果 acceptProgress=false，确保 typing=false, needFlushQueue=false

#### Step 5: handleAgentMessage — suLoading 期间丢弃

suLoading 期间收到 agent 回复，说明这是旧 turn 的回复。RPC 加载会处理消息历史。直接丢弃避免重复。

## 验证方案

- `go build ./...` 编译通过
- `go test ./channel/ -count=1` 全部通过
- 手动验证：remote TUI 模式下快速切换 5+ 次，消息不应丢失或重复

## 回滚策略

所有修改在客户端侧，不影响服务器。回退 git diff 即可。

## 注意事项

- `m.suLoading` 在 handleSuHistoryLoad 开头被设为 false，这是全局唯一的清除点
- local CLI 不受 suLoading 影响（因为它没有异步 RPC 延迟）
- `turnDoneFlags` 需要在 handleSuHistoryLoad 中根据服务器状态重建
