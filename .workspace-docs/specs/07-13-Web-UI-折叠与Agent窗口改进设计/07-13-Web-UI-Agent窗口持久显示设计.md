---
type: Design Spec
title: Web UI Agent 窗口持久显示设计
description: SSE 动态订阅列表、PhaseDone finalizing 过渡态、SubAgent 独立面板修复
tags:
  - spec
status: draft
repos:
  xbot: 9662990
---

# Web UI Agent 窗口持久显示设计

> 主 Spec: [Web UI 折叠与 Agent 窗口改进设计](./07-13-Web-UI-折叠与Agent窗口改进设计.md)

## 目标

1. Agent 窗口在焦点切换时保持运行状态显示（不冻结）
2. SubAgent 独立面板正确显示消息和进度
3. PhaseDone 后无空白窗口（finalizing 过渡态）

## 范围

### 范围内行为

- `web/src/hooks/useActiveSSESubscription.ts` — 从"独占"改为"动态订阅列表"
- `web/src/hooks/useProgressStream.ts` — PhaseDone finalizing 逻辑
- `web/src/components/agent/progressStore.ts` — finalizing 状态字段 + 3s 超时
- `web/src/workspace/panels/AgentPanel.tsx` — SubAgent 面板修复
- `web/src/workspace/DockviewContainer.tsx` — 面板 active 状态传递
- `web/src/providers/sseConnection.ts` — 多 chatID 订阅支持（如需）

### 范围外行为

- 工具图标和折叠模型（→ Spec A / B）
- 后端 SSE 推送逻辑
- Dockview 布局结构变更

## 依赖

- 完全独立，可与 Spec A / B 并行开发
- 需遵守共享契约中的 ProgressStore finalizing 状态定义

## 输入

- 后端 SSE 支持订阅多个 session（`subscribe` 事件可多次发送）
- 后端 `GetActiveProgress` RPC 返回指定 chatID 的活跃进度快照
- 后端 `progress_structured` 事件携带 `chat_id` 字段区分不同 Agent

## 输出

- 切换面板时 Agent 窗口保持运行状态
- SubAgent 面板正确显示消息和进度
- PhaseDone 后无空白窗口

## 详细设计

### 1. SSE 动态订阅列表

**当前架构**：

```
useActiveSSESubscription:
  - 整个应用只有一个 EventSource
  - 活跃面板独占：切换 Tab 时旧面板 disconnect，新面板 subscribe
  - 非活跃面板的 useProgressStream 被设为 disabled
```

**问题**：切换到其他面板时，Agent 面板 SSE 断开，进度冻结。

**新架构**：方案 C — 单 EventSource + 动态订阅列表

```
useActiveSSESubscription:
  - 仍然只有一个 EventSource
  - 维护一个订阅集合 Set<string>（所有打开的 Agent 面板的 chatID）
  - 活跃面板切换时：
    1. 新活跃面板的 chatID 加入订阅集合（如尚未在集合中）
    2. 旧活跃面板不从订阅集合中移除（仍保持订阅）
    3. 只有面板关闭时才从集合中移除
  - 每个 Agent 面板的 useProgressStream 始终启用（不再 disabled）
  - 事件按 chat_id 路由到各自的 ProgressStore
```

**核心改动**：

#### 1.1 订阅集合管理

```typescript
// useActiveSSESubscription.ts 重构

// 全局订阅管理器（单例）
class SubscriptionManager {
  private chatIDs = new Set<string>()
  private ws: EventSource | null = null
  private listeners = new Map<string, Set<(msg: WSMessage) => void>>()

  // 添加订阅
  add(chatID: string, channel: string, handler: (msg: WSMessage) => void) {
    const key = `${channel}:${chatID}`
    this.chatIDs.add(key)
    // 注册 handler
    if (!this.listeners.has(key)) this.listeners.set(key, new Set())
    this.listeners.get(key)!.add(handler)
    // 通知后端订阅
    this.sendSubscribe(chatID, channel)
  }

  // 移除订阅（面板关闭时调用）
  remove(chatID: string, channel: string, handler: (msg: WSMessage) => void) {
    const key = `${channel}:${chatID}`
    this.chatIDs.delete(key)
    this.listeners.get(key)?.delete(handler)
    // 通知后端取消订阅
    this.sendUnsubscribe(chatID, channel)
  }

  // 事件路由
  dispatch(msg: WSMessage) {
    // 按 chat_id 路由到对应 handler
    const chatID = extractChatID(msg)
    const channel = extractChannel(msg)
    const key = `${channel}:${chatID}`
    this.listeners.get(key)?.forEach(h => h(msg))
  }
}
```

#### 1.2 AgentPanel 改动

```typescript
// AgentPanel.tsx
// 去掉 shouldSubscribe = params.active !== false
// 改为：只要面板存在就订阅
const shouldSubscribe = true  // 面板存在即订阅

// useProgressStream 不再 disabled
const progress = useProgressStream({
  chatID: progressChatID,
  disabled: false,  // 始终启用
  // ...
})
```

#### 1.3 DockviewContainer 改动

```typescript
// DockviewContainer.tsx
// 面板关闭时通知 SubscriptionManager 移除订阅
// onDidActivePanelChange 不再影响 SSE 订阅
// active 状态仅用于 UI 高亮，不影响数据订阅
```

### 2. PhaseDone finalizing 过渡态

**当前行为**：`progress_structured` 事件 `phase='done'` 到达时，`useProgressStream` 立即调用 `store.reset()`，清空所有进度状态。最终回复的 `text` 事件可能还没到达，导致短暂空白。

**新行为**：

```
PhaseDone 到达:
  → store 进入 finalizing 状态
  → 保持进度快照可见（工具标记为 done，停止脉冲动画）
  → 等待 text 事件到达
  → text 事件到达 → onAssistantComplete → store.reset()
  → 3s 超时 → store.reset()（兜底）
```

**ProgressStore 改动**：

```typescript
// progressStore.ts

interface ProgressSnapshot {
  // ... 现有字段
  phase: 'idle' | 'streaming' | 'finalizing' | 'done'
}

// setStructuredTools 中处理 phase='done'
setStructuredTools(opts): void {
  this.mutate((draft) => {
    if (opts.phase === 'done') {
      draft.phase = 'finalizing'
      // 不清空数据，保留快照
      // 标记所有 active tools 为 done
      draft.completedTools = [...draft.completedTools, ...draft.activeTools]
      draft.activeTools = []
      // 停止动画（CSS 通过 phase=finalizing 控制）
      // 启动 3s 超时
      this.startFinalizingTimeout()
      return
    }
    // ... 正常处理
  })
}

private startFinalizingTimeout() {
  if (this.finalizingTimer) clearTimeout(this.finalizingTimer)
  this.finalizingTimer = setTimeout(() => {
    this.reset()  // 超时兜底
  }, 3000)
}

// text 事件到达时调用
reset(): void {
  if (this.finalizingTimer) {
    clearTimeout(this.finalizingTimer)
    this.finalizingTimer = null
  }
  // ... 现有 reset 逻辑
}
```

**useProgressStream 改动**：

```typescript
// useProgressStream.ts
case 'progress_structured':
case 'sync_progress':
  if (p.phase === 'done') {
    // 不再立即 store.reset()
    // 改为：让 store 进入 finalizing 状态
    store.setStructuredTools({ phase: 'done', ... })
    // 不调用 finalizedRef 相关逻辑
    return
  }

case 'text':
  // text 事件到达，正常 finalize
  completeRef.current?.(finalText, iterations, msg.seq)
  store.reset()  // 现在 reset 会清除 finalizing 状态
  return
```

### 3. SubAgent 独立面板修复

**当前问题**：

1. SubAgent 面板 `onAssistantComplete: undefined`，最终回复不追加到消息列表
2. 依赖 `ws.onSession()` 事件触发 `reloadChat()`，事件丢失则不显示
3. 非活跃面板 SSE 断开，进度冻结

**修复方案**：

#### 3.1 SubAgent 面板也接收 text 事件

```typescript
// AgentPanel.tsx
// SubAgent 面板也设置 onAssistantComplete
const onAssistantComplete = isSubAgent
  ? (finalText: string, iterations: WebIteration[], eventSeq?: number) => {
      // SubAgent 也追加最终回复到消息列表
      chat.appendAssistant(finalText, iterations, eventSeq)
      void chat.reload()
    }
  : (finalText: string, iterations: WebIteration[], eventSeq?: number) => {
      chat.appendAssistant(finalText, iterations, eventSeq)
      void chat.reload()
    }

// 实际上两者逻辑相同，SubAgent 不再特殊处理
```

#### 3.2 SubAgent 面板始终订阅

通过第 1 节的 SSE 动态订阅列表改造，SubAgent 面板在非活跃时也保持订阅，进度不会冻结。

#### 3.3 SubAgent 完成后的状态保留

```typescript
// progressStore.ts — mergeSubAgentTrees 修改
// 当前：跳过 status='done'/'error' 的节点
// 改为：保留 done/error 节点，但用不同样式渲染（灰色，不闪烁）

function mergeSubAgentTrees(prev: WebSubAgentProgress[], next: WebSubAgentProgress[]): WebSubAgentProgress[] {
  // 不再跳过 done/error 节点
  // done 节点保留，desc 更新为最终状态
  // 前端用灰色图标渲染 done 节点
}
```

```typescript
// SubAgentProgressTree.tsx — 渲染 done 节点
// done: 灰色 Asterisk 图标，不闪烁
// running: 蓝色脉冲 Asterisk 图标
// error: 红色 Asterisk 图标
```

## 错误语义

- SubscriptionManager 中 handler 未注册 → 事件被忽略（不影响其他面板）
- finalizing 超时后 text 事件才到达 → text 事件被忽略（store 已 reset，finalizedRef 守卫）
- SubAgent chatID 格式不匹配 → `matchesChatID` 的 3 层过滤兜底
- 后端不支持多 session 订阅 → 回退到单 session 订阅（功能降级，不崩溃）

## 验收标准

- [ ] 打开 Agent 面板（正在运行），切换到其他面板，切回后进度状态正确
- [ ] Agent 正在运行时，切换到文件面板，Agent 面板不冻结为"未进行中"
- [ ] SubAgent 独立面板正确显示消息历史
- [ ] SubAgent 完成后最终回复显示在消息列表中
- [ ] SubAgent 进度树中已完成的节点保留显示（灰色，不闪烁）
- [ ] PhaseDone 后无空白窗口（进度快照保持可见直到 text 到达）
- [ ] finalizing 3s 超时后正常 reset（不卡死）

## 验证范围

- 手动测试：主 Agent 运行中切换到其他面板再切回，验证进度正确
- 手动测试：SubAgent 运行中切换面板再切回，验证进度正确
- 手动测试：Agent 完成后观察无空白窗口
- 手动测试：SubAgent 完成后最终回复正确显示
- 手动测试：SubAgent 进度树中完成节点保留显示
