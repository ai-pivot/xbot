# 计划：/new 和 /compress 执行期间的 TUI 状态指示

> 生成时间：2026-05-14
> 状态：待确认

## 背景与目标

**问题**：`/new` 和 `/compress` 是 builtin commands，不走 `engine.Run`，不发 progress 事件，不发 PhaseDone。用户执行后 TUI 主视图无任何反馈（不显示 typing、不显示 spinner、输入框可编辑），切走再切回来或远程重连后也完全看不出正在处理。

**目标**：
1. 用户输入 `/new` 或 `/compress` 后，TUI 主视图立即显示对应状态（newing/compressing spinner），输入框禁用
2. 执行期间切换到其他 session 再切回来，状态恢复可见
3. 远程 CLI 重连后，如果正在执行 /new 或 /compress，用户也能看到对应状态

## 现状分析

### 为什么之前的方案全部失败

1. **前端 `startAgentTurn`**：只设了 `typing=true`，但 agent 端的 outbound 消息到达后触发 `handleAgentMessage`，它不知道 turn 已开始，且 `/compress` 的中间消息 "🔄 开始压缩上下文..." 是完整的 outbound（非 partial），会被正常渲染为 assistant 消息。最终结果消息到达时也没有 `endAgentTurn` —— turn 永远不会结束。

2. **agent 端 `sendBuiltinProgress`**：直接调 `CLIChannel.SendProgress` 发了 progress 事件，但这绕过了 `buildCLIProgressEventHandler` 闭包 —— `lastProgressSnapshot` 没有被 Store，`GetActiveProgress` RPC 返回 nil，远程重连无法恢复。而且 Seq=0，被前端 seq 去重逻辑丢弃（因为初始 `lastProgressSeq` 也是 0）。

3. **`busyPhase` 字段方案**：不走 turn 生命周期，渲染时机不对，`handleAgentMessage` 无法正确清理。

### 根因总结

`/new` 和 `/compress` 完全不经过 `engine.Run`，而所有现有的状态机制（progress events、PhaseDone、lastProgressSnapshot、GetActiveProgress RPC）都只在 `engine.Run` 内工作。

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `agent/agent.go` | `chatProcessLoop` — 串行处理命令，defer 发 session idle + Delete lastProgressSnapshot | 修改 |
| `agent/compress.go` | `handleCompress` — 压缩逻辑，中间 `sendMessage("🔄...")` | 修改 |
| `agent/prompt_handler.go` | `handleNewSession` — /new 逻辑，返回 `session_reset=true` | 修改 |
| `agent/progress.go` | `PhaseNewing` 常量 | 新增 |
| `agent/engine_wire.go` | `buildCLIProgressEventHandler` — progress 闭包，含 lastProgressSnapshot.Store | 不改 |
| `channel/cli_message.go` | `/compress` 的 `handleSlashCommand`、`renderCurrentIteration` | 修改 |
| `channel/cli_view.go` | `renderProgressStatus` — status bar | 修改 |
| `channel/i18n.go` | `StatusNewing` 翻译 | 修改 |
| `channel/cli_test.go` | 测试 | 修改 |

### 依赖关系

```
CLI 前端                    Agent 端
────────                    ────────
handleSlashCommand          chatProcessLoop
  ↓                           ↓
sendInbound → bus →       processMessage → cmd.Execute
startAgentTurn()            handleCompress/handleNewSession
  ↓                           ↓
typing=true, progress=nil   sendMessage("🔄...")  ← outbound 到 CLI
  ↓                           ↓
handleAgentMessage          最终结果 → sendMessage
  ↓                           ↓
endAgentTurn? ← 没有!       defer: session idle, Delete snapshot
```

### 风险点
- **Seq 管理**：如果直接发 progress 但 Seq 不对，前端会丢弃
- **lastProgressSnapshot**：如果不在闭包内 Store，远程重连恢复不了
- **endAgentTurn 时机**：必须准确，否则 turn 卡住或提前结束
- **chatProcessLoop 的 defer 会 Delete lastProgressSnapshot**：builtin command 的 snapshot 必须在此 defer 之前 Store

## 详细计划

### 阶段一：Agent 端 — 让 `/compress` 和 `/new` 发 progress 事件（正确路径）

**核心思路**：在 `chatProcessLoop` 中，`processMessage` 返回后（不管成功/失败），在 defer 的 `Delete(lastProgressSnapshot)` 之前发送 `PhaseDone`。在 `handleCompress`/`handleNewSession` 执行开始时发对应 phase 的 progress。

但上一轮的 `sendBuiltinProgress` 方案失败原因是 Seq 和 lastProgressSnapshot 问题。正确的做法是：

#### 步骤 1.1：在 `agent/agent.go` 新增 `emitBuiltinProgress` 方法

- 参数：`chName, chatID, phase string, seq *atomic.Uint64`
- 功能：
  1. 构建 `protocol.ProgressEvent{ChatID: progressKey, Phase: phase, Iteration: 0, Seq: seq.Add(1)}`
  2. 通过 `channelFinder` 找到 CLI channel
  3. 调 `SendProgress` 发到 CLI
  4. `lastProgressSnapshot.Store(progressKey, payload)` — 远程重连可恢复

#### 步骤 1.2：在 `agent/agent.go` 新增 `emitBuiltinProgressDone` 方法

- 同上但 `Phase: "done"`
- 同时 `lastProgressSnapshot.Delete(progressKey)` — 命令完成，清掉 snapshot

#### 步骤 1.3：在 `chatProcessLoop` 中注入 progress 发送

`chatProcessLoop` 已经有 `sessionStateHandler` 的 busy/idle 事件发送。现在需要在它内部，对 `processMessage` 的结果判断是否是 builtin command，如果是，在开始时发 progress phase，结束时发 done。

**但 `chatProcessLoop` 不知道 `processMessage` 执行的是哪个命令**。所以更好的方案是让 `handleCompress`/`handleNewSession` 自己发。

#### 步骤 1.3（修正）：在 `handleCompress` 中发 progress

- 入口：`emitBuiltinProgress("compressing")`
- 出口：`defer emitBuiltinProgressDone()`
- 需要一个 agent 级别的 `progressSeq` atomic counter（每个 chat 独立）

**关键问题：Seq 计数器**。`buildCLIProgressEventHandler` 里用 `cfg.ProgressSeq`，这是 `engine_wire.go:331` 创建的 `atomic.Uint64`，每个 RunConfig 独立。Builtin command 不走 RunConfig，没有 ProgressSeq。

**解决方案**：在 agent 上维护一个 `builtinProgressSeq sync.Map`（key=progressKey, value=*atomic.Uint64），在 `emitBuiltinProgress` 中递增。

#### 步骤 1.4：在 `handleNewSession` 中发 progress

同上，`emitBuiltinProgress("newing")` + `defer emitBuiltinProgressDone()`。

### 阶段二：CLI 前端 — `/compress` 加 `startAgentTurn`

#### 步骤 2.1：`cli_message.go` — `/compress` 命令加 `startAgentTurn`

当前 `/compress` 只调 `sendInbound`，不加 `startAgentTurn`。需要加上，让 typing=true、inputReady=false。

#### 步骤 2.2：验证 `endAgentTurn` 时机

agent 端发完 `PhaseDone` progress → CLI 收到 `handleProgressMsg` → `handleProgressDone` → `endAgentTurn`。
`/new` 的 `session_reset` 路径也调 `endAgentTurn` —— 需要 PhaseDone 在 session_reset outbound 之前到达，否则 endAgentTurn 会被调两次（但 endAgentTurn 有 turnID guard，第二次是 no-op）。

实际上 `/new` 的流程是：
1. `handleNewSession` 发 `PhaseNewing` progress → CLI 收到，typing=true
2. `handleNewSession` 返回 OutboundMessage(session_reset=true)
3. defer 发 `PhaseDone` progress → CLI 收到，endAgentTurn
4. `chatProcessLoop` 发 outbound(session_reset=true) → CLI 收到，清 messages + endAgentTurn（但 turn 已结束，no-op）

时序问题：step 3 和 step 4 的顺序不确定。defer 在 `handleCompress`/`handleNewSession` 返回时执行（step 3），然后 `chatProcessLoop` 的 `sendMessage` 执行（step 4）。所以 step 3 在 step 4 之前。OK。

但 `/compress` 有额外问题：`handleCompress` 内部先 `sendMessage("🔄...")`，这个 outbound 到达 CLI 时，`handleAgentMessage` 会处理它。如果此时 `typing=true`（因为 `startAgentTurn` 被调了），assistant 消息会被正常追加。然后最终结果 outbound 到达时也一样。PhaseDone progress 在 defer 里发，最终在 `chatProcessLoop` 的 `sendMessage(response)` 之前。所以 PhaseDone 先到 CLI → endAgentTurn → typing=false。然后最终 outbound 到达 → 正常 append。

### 阶段三：前端渲染

#### 步骤 3.1：`cli_message.go` — `renderCurrentIteration` 加 `"newing"` spinner case

#### 步骤 3.2：`cli_view.go` — `renderProgressStatus` 加 `"newing"` case

#### 步骤 3.3：`i18n.go` — `StatusNewing` + zh/en/ja 翻译

#### 步骤 3.4：`progress.go` — `PhaseNewing` 常量

### 阶段四：测试

#### 步骤 4.1：更新 `cli_test.go` 的 `renderProgressStatus` 测试

#### 步骤 4.2：`go build ./...` + `go test ./agent/... ./channel/`

## 验证方案

1. **本地 CLI**：输入 `/compress` → 应看到 compressing spinner + 输入框禁用 → 执行完成后恢复 idle
2. **本地 CLI**：输入 `/new` → 应看到 newing spinner + 输入框禁用 → 执行完成后清空消息 + idle
3. **Session 切换**：`/compress` 执行中切走再切回来 → 应恢复 compressing 状态
4. **远程重连**：服务器执行 `/compress` 中，远程 CLI 重连 → 通过 `GetActiveProgress` 恢复 compressing 状态

## 回滚策略

`git checkout -- .` 回滚所有改动。

## 注意事项

- Seq 必须严格递增，不能从 0 开始（前端初始 `lastProgressSeq=0` 会丢弃 Seq=0 的事件）
- `lastProgressSnapshot` 必须在 PhaseDone 之前 Store（远程重连需要），在 PhaseDone 之后 Delete（避免 snapshot 泄漏）
- `chatProcessLoop` 的 defer 里已经有 `lastProgressSnapshot.Delete(key)`，这是安全兜底，不需要移除
- `/compress` 的 `handleCompress` 内部 `sendMessage("🔄...")` 会产生一条 outbound，此时 `startAgentTurn` 已被调，需要确认 `handleAgentMessage` 不会提前 endAgentTurn
- `/new` 的 `session_reset` 路径调 `endAgentTurn`，如果 PhaseDone 先到了也会调 `endAgentTurn`，但因为 turnID guard 第二次是 no-op
