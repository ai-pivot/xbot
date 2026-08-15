# Web 前端消息一致性设计（Raft 模型）

> 范围：Web Agent 面板消息列表（`useChatMessages` + `useProgressStream` + `progressStore` + `MessageStore` + `sseConnection`）。
> 目标：解释现有架构如何保证弱网下的线性一致性，以及已知风险点与修复方向。
> 设计原则：**"AppendEntries (push) is best-effort, InstallSnapshot (pull) is authoritative"**（`web_hub.go:276`）。

## 1. 架构总览（Raft 类比）

| Raft 概念 | 服务器端 | 客户端 |
|---|---|---|
| **Log** | `eventStream` ring buffer（`web_eventstream.go`，per-session-route，512 条，单调 seq）+ `session_messages`（DB append-only） | SSE 事件流 + `lastSeqCache`（envelope seq 水印） |
| **Snapshot** | `get_history`（DB 权威，带 `last_seq`）、`get_active_progress`（`lastProgressSnapshot` + `iterationHistories`，`from_iteration` 增量） | `progressStore`（live）+ `MessageStore`（committed 槽位） |
| **State Machine** | `lastProgressSnapshot` + `iterationHistories`（内存） | `ProgressStore`（live 渲染）+ `MessageStore`（每 turn 1 user + 1 assistant） |
| **追赶** | — | 重连 `last_event_id` → ring replay；ring evict → `resync_required` → DB reload；seq gap → `restoreActiveProgress`；iteration gap → `onIterationGap` → reload |

**两个独立的 seq 序列（必须区分，混用即 bug）：**
- **SSE envelope seq**（`id:` 字段）：per-route 单调，由 `eventStream.nextSeq()` 分配。用于传输层去重 / gap 检测 / 重放游标（`setLastSeq`）。
- **`ProgressEvent.Seq`**（`progress.seq`）：**per-Run**（`engine_wire.go:456` 每次 Run 新建 `atomic.Uint64`）。用于 `progressStore` 内部 stale-watermark（`setStructuredTools` 丢弃 `seq <= current.eventSeq` 的事件）。

## 2. 消息流

```
用户发送 → REST /api/message → 入队（分配 turn_id）→ user_echo（带 turn_id + requestID）
        → 处理循环 → progress_structured / stream_content / text（SSE 推送）
客户端：SSE 事件 → progressStore（live）→ MessageStore（按 turn_id 写槽位）
      text 事件 → commitAssistant（live → assistant 迁移，同对象不产生第二行）
刷新/切换会话 → fetchHistory（DB 快照）+ active_progress（hydration）
```

**追赶路径：**
1. 断线重连 → `Last-Event-ID` → `replayAfter` 追 log；不足则 `restoreActiveProgress` 拉快照。
2. ring 容量 evict → `resync_required` → 强制 DB reload（InstallSnapshot 语义）。
3. iterationHistory 内部 gap（1→3 缺 2）→ `onIterationGap` → DB reload（delta 无法自愈，只能快照）。
4. 跨 turn gap / turn 结束于 gap → `replay_gap` → DB reload。

## 3. 已保证的一致性（结构 + 守卫，非启发式）

- **渲染线性一致性**：`MessageStore` 每 turn 恰好 1 user + 1 assistant，live 是未完成态；`orderMessageRows`/`bindTurnIDs` 强制 `(turnID, role)` 排序（`messageOrder.ts` R1-R4）。
- **turn 边界原子性**：`commitAssistant` + `store.reset()` 在 `flushSync` 内（web-linearizability.md I4）。
- **内容永不消失**：cancel → `freeze()` 保留已渲染内容；iteration 边界保留 activeTools（标记 done）；`hasVisibleProgress` 的 `lastIter>0` 兜底。
- **快照权威修复链**：`resync_required` / `replay_gap` / `onIterationGap` 三路强制 DB reload。
- **半开连接检测**：heartbeat 事件行 + 45s 静默超时 watchdog + REST 轮询兜底。

## 4. 弱网一致性风险点（审计结论，2026-08）

### P1-1 `progressStore.replace()` 抬高 eventSeq（`progressStore.ts:940-942`）
`replace()`（hydration 路径）保留 `eventSeq = max(next, current)`；但 `resetAndReplace()`（`:487-495`）已按 AGENTS.md 教训改为**不取 eventSeq**。两处矛盾。
**风险链**：hydration 用旧 Run 快照（`active_progress.seq` 可达数千）+ turn_started 丢失 → 新 Run 的 `ProgressEvent.Seq` 从 1 重新开始 → 全部被 stale-watermark 丢弃 → **新 turn 不渲染**。
**修复**：`replace()` 移除 eventSeq 逻辑，与 `resetAndReplace()` 对齐。

### P1-2 `bindUser()` 绑定错误 user（`messageStore.ts:143-155`）
`bindUser` 从后往前绑定"最后一条 `!persisted` 的 pending user"。两条消息快速连发 + REST 响应慢时，`turn_started(msg1)` 可能绑定到 msg2。
**后端已提供 `TurnStartInfo.RequestID`**（`protocol/events.go:155`，"for user-typed: match optimistic message"），前端未使用。
**修复**：`bindUser(turnID, requestID?)` 优先按 RequestID 精确匹配；`useProgressStream.ts:1033` 传入 `ts.requestID`。

### P2-1 `historyReady` gate 竞态（`useProgressStream.ts:781`）
切换会话窗口（`historyReady=false`）内 `writeLiveToMessageStore` 直接丢弃。若 turn 恰好在 fetch 之后、ready 之前开始且 `active_progress=null` → **早期迭代永久丢失**。
**修复**：gate 改为缓冲回放，或 reload 完成时二次校验（active_progress=null 但已收 turn_started 则强制 reload）。

### P2-2 turn_started 丢失 → 乐观 user 不绑定（`useProgressStream.ts:1283-1301`）
`progress_structured` 的 turnID-change fallback 不调 `messageStore.beginTurn()`（仅 turn_started 分支调用）。turn_started 被 SSE drop → user 保持 `turnID=0` 渲染到底部，短暂违反 R2。
**修复**：fallback 分支补调 `beginTurn`，与 turn_started 分支对齐。

### P3-1 `writeLiveToMessageStore` 的 turnID=1 硬编码（`useProgressStream.ts:783-789`）
无 turn_id 早期流事件归到 turn 1，可能制造幽灵 live 行。低风险（commitStaleLives/mergeHistory 最终清理）。建议归 `store.lastTurnID`。

### P3-2 `commitStaleLives` 无条件覆盖 assistant（`messageStore.ts:572-591`）
当前安全依赖"`commitAssistant` 同步清 live"的顺序保证；若回调异步化会丢失更完整的迭代。建议加幂等守卫 `if (slot.assistant) return`。

## 5. 修复优先级

| # | 项 | 改动 | 回归测试 |
|---|---|---|---|
| 1 | P1-1 | 删 replace() 的 eventSeq max | 「hydration 旧 Run 高 seq + 新 Run seq=1 → 不被丢弃」 |
| 2 | P1-2 | bindUser 按 RequestID 匹配 | 「两条 pending + turn_started(req1) → 绑 msg1」 |
| 3 | P2-1 | gate 缓冲/二次校验 | 「historyReady=false 期间 turn_started + active_progress=null」 |
| 4 | P2-2 | fallback 补 beginTurn | 「turn_started 丢失 → user 仍绑定」 |

验证：`npm run build`（tsc -b）+ vitest 全量 + `go test ./channel/web/...`。

## 6. 结论

架构骨架正确（Raft 式 log + snapshot + 强制快照安装），已知 turn 消失/重复类 P0 已被方案 A + 多层 guard 覆盖。剩余风险集中在**两个数据源的边界**：水印语义（P1-1/P3 双 seq）与弱网时序窗口（P1-2/P2-1/P2-2）。修复均为小改动 + 明确回归场景。
