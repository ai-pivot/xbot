# Spec: AskUser 持久化 — 刷新恢复 + 跨 session 可见

## Problem

AskUser 事件是一次性 WS 消息。当前 session 不匹配时被过滤；刷新页面后 React state 丢失。用户看到黄灯但切换到 session 后看不到 AskUser 面板，无法回答 → 死锁。

## Solution

两层修复：

### Layer 1: 前端 — useSessionStore 存 AskUser prompt

`useSessionStore` 加 `askUserPrompts: Map<chatID, AskUserPrompt>`：
- WS 收到 `ask_user` 时，存入 `askUserPrompts[chatID]` + 设 status 为 `waiting_input`
- AgentPanel 从 store 读取 `askUserPrompts[当前chatID]`，有则渲染 AskUserPanel
- 回答/取消时清除 `askUserPrompts[chatID]` + 设 status 为 `idle`
- 切换 session 不丢（store 是全局 Context state）

### Layer 2: 后端 — WS 重连重发 pending AskUser

后端在 WS 客户端订阅 session 时（`handleSubscribe` 路径），检查该 session 是否有 pending AskUser，有则重发 `ask_user` 消息。

**实现**：Agent 的 `lastProgressSnapshot` 不含 WaitingUser 状态。需要：
1. Agent 加 `waitingUserSessions sync.Map` (key: `channel:chatID`, value: `*protocol.ProgressEvent`)
2. `buildWaitingUserOutbound` 时存入 `waitingUserSessions`
3. AskUser 回答后从 `waitingUserSessions` 删除
4. 新增 `GetPendingAskUser(ch, chatID)` 方法
5. WS 订阅路径调 `GetPendingAskUser`，有则重发

## Changes

### 后端 (4 files)

| File | Change |
|------|--------|
| `agent/agent.go` | 加 `waitingUserSessions sync.Map`；加 `GetPendingAskUser(ch, chatID)` 方法；AskUser 回答后 Delete |
| `agent/agent_process.go` | `buildWaitingUserOutbound` 存入 `waitingUserSessions` |
| `agent/client.go` | 加 `GetPendingAskUser` RPC client method |
| `channel/web/web.go` | WS 订阅路径（~line 952）调 `GetPendingAskUser`，有则重发 |

### 前端 (3 files)

| File | Change |
|------|--------|
| `hooks/useSessionStore.ts` | 加 `askUserPrompts` state + `setAskUserPrompt`/`clearAskUserPrompt` actions |
| `hooks/useAskUser.ts` | 从 store 读写 prompt，不再用本地 state |
| `workspace/panels/AgentPanel.tsx` | 从 store 读取 prompt，传给 MessageList footer |

## Risks

- **后端 `waitingUserSessions`**：只读不修改 agent 执行逻辑，只是额外存一份 pending 状态。AskUser 回答后 Delete 防止泄漏。
- **WS 重发**：只在订阅时发一次，不会循环。和现有 progress snapshot 重发是同一模式。
- **前端 store**：内存 state，刷新丢失——但 Layer 2 的后端重发会恢复。

## Acceptance

- [ ] 触发 AskUser → 切换到别的 session → 切回来 → AskUserPanel 仍在
- [ ] 触发 AskUser → 刷新页面 → AskUserPanel 恢复
- [ ] 触发 AskUser → 左侧黄灯亮 → 回答后黄灯灭
- [ ] 不在当前 session 时看不到 AskUserPanel（只在对应 session 显示）
- [ ] build-and-sync 通过
