---
type: Design Spec
title: Web UI 消息合并与折叠模型设计
description: 修复摘要行重复显示、T→O→C 渲染顺序、四级折叠模型（三级+mergeTools toggle）
tags:
  - spec
status: draft
repos:
  xbot: 9662990
---

# Web UI 消息合并与折叠模型设计

> 主 Spec: [Web UI 折叠与 Agent 窗口改进设计](./07-13-Web-UI-折叠与Agent窗口改进设计.md)

## 目标

1. 修复 "已处理 N 次迭代 · 调用 N 个工具" 摘要行重复显示
2. 将单迭代内的渲染顺序从 T→C→O 改为 T→O→C（推理→文本→工具）
3. 实现四级折叠/展开模型：三级折叠（all/minimal/none）+ `mergeTools` 独立 toggle
4. 设置面板中折叠级别点击后立即生效
5. 优化工具运行中等的显示动效

## 范围

### 范围内行为

- `web/src/components/agent/AssistantMessage.tsx` — 摘要行渲染、折叠入口
- `web/src/components/agent/IterationHistory.tsx` — 单迭代渲染顺序 T→O→C
- `web/src/components/agent/LiveIteration.tsx` — 流式迭代渲染顺序 T→O→C
- `web/src/components/agent/FoldedToolGroup.tsx` — 工具合并逻辑（受 mergeTools 控制）
- `web/src/hooks/useProgressStream.ts` — finalize 去重逻辑
- `web/src/components/agent/progressStore.ts` — `dedupMessages` 去重
- `web/src/hooks/useCollapseLevel.ts` — 折叠级别 hook（新增 mergeTools）
- `web/src/components/settings/SettingsCollapse.tsx` — 设置面板
- `web/src/i18n/zh-CN.ts` / `en.ts` — 新增文案
- `web/src/types/agent.ts` — 类型定义

### 范围外行为

- 工具图标体系和极简合并行格式（→ Spec B）
- SSE 订阅架构和 Agent 窗口冻结（→ Spec C）
- 后端数据结构和事件流

## 依赖

- 无前置依赖，可与 Spec B 并行开发
- 需遵守主 Spec 共享契约中的 CollapseLevel 类型变更

## 输入

- 后端 `protocol.ProgressEvent` 结构不变
- `message.iterations` 格式不变：`{ iteration, thinking, reasoning, tools, toolCount }[]`

## 输出

- 摘要行不再重复显示
- T→O→C 渲染顺序
- 四级折叠模型（三级 + mergeTools toggle）
- 设置面板点击即生效

## 详细设计

### 1. 摘要行重复显示修复

**根因**：`onAssistantComplete` 可能被多次调用。`finalizedRef` 被 `stream_content` 事件重置为 `false`，导致 `text` 事件和 `session(idle)` 事件都触发 finalize。

**修复方案**：

```
useProgressStream.ts:
- 去掉 finalizedRef 被 stream_content 重置的逻辑
- finalizedRef 在 onAssistantComplete 调用后立即设为 true，只有在新 turn 开始（session(busy)）时才重置为 false
- text 事件和 session(idle) 事件共享同一个 finalizedRef 守卫，先到者执行 finalize，后到者跳过
```

**兜底**：`dedupMessages`（`progressStore.ts:145`）作为第二道防线，但不应依赖它。

### 2. T→O→C 渲染顺序

**当前**（`IterationHistory.tsx:36-56`）：
```
T: reasoning  →  C: tools  →  O: thinking(文本)
```

**改为**：
```
T: reasoning  →  O: thinking(文本)  →  C: tools
```

**改动点**：
- `IterationHistory.tsx`：调整 reasoning/thinking/tools 的 JSX 顺序
- `LiveIteration.tsx`：同样调整流式渲染顺序

### 3. 四级折叠模型

#### 3.1 mergeTools toggle

新增 `useMergeTools` hook（或合并到 `useCollapseLevel` 中）：

```typescript
// localStorage key: "xbot-merge-tools"
// 默认值: true
// 行为:
//   true  — 连续工具调用合并为一行极简格式（[图标] ×N）
//   false — 连续工具调用各自独立显示
```

与 `collapseLevel` 正交，任何折叠级别下都可切换。

#### 3.2 各级别行为

**`all` 级别**（全部折叠）：
- 只显示最后一个 Text（O）内容
- 如果最后一个 Text 之后还有工具调用，也显示这些工具调用
- 所有非最后的 Text 折叠为一行摘要
- 工具调用：mergeTools=true 时合并为极简行，false 时各自显示标题行
- 摘要行格式不变：`已处理 N 次迭代 · 调用 N 个工具`

**`minimal` 级别**（折叠详细）：
- 显示所有 Text（O）内容
- 推理（T）折叠为一行（显示字符数）
- 工具调用：mergeTools=true 时连续工具合并为 `图标+工具名 ×N`，false 时各自显示标题行
- 展开后显示工具详情

**`none` 级别**（完全展开）：
- 显示所有 Text（O）内容
- 推理（T）仍然默认折叠（可手动展开）
- 工具调用不合并，各自展开显示
- mergeTools 在此级别无效（always expand）

#### 3.3 级别矩阵

| 级别 | Text(O) | Reasoning(T) | Tools(C) mergeTools=true | Tools(C) mergeTools=false |
|------|---------|---------------|--------------------------|---------------------------|
| all | 只显示最后一个（后跟工具也显示） | 折叠为摘要 | 合并极简行 | 各自标题行 |
| minimal | 全部显示 | 折叠（显示字符数） | 合并为图标行 | 各自标题行 |
| none | 全部显示 | 折叠（可展开） | 不合并，各自展开 | 不合并，各自展开 |

### 4. 设置面板即时生效

**当前问题**：`SettingsCollapse.tsx` 中切换折叠级别后需要刷新页面。

**修复**：
- `useCollapseLevel` 已通过 `storage` 事件支持跨窗口同步
- 需要确保设置面板中的级别变更能通过 React state 立即传播到所有使用 `useCollapseLevel` 的组件
- 方案：在 `useCollapseLevel` 中使用 `useSyncExternalStore` 订阅一个全局 emitter（或复用现有的 `window.dispatchEvent` + `storage` 事件），确保同一窗口内的所有组件实例同步更新

### 5. 工具运行中显示动效

**当前**：running 状态使用 ◑ 字符，无动画。

**改为**：
- running 状态：状态色点使用 CSS `pulse` 动画（蓝色脉冲）
- generating 状态：状态色点使用 CSS `bounce` 动画（淡蓝色闪烁）
- done 状态：灰色静态点
- error 状态：红色静态点
- 过渡动画：running → done 时色点颜色通过 CSS transition 平滑过渡

注意：动效的 CSS 类名和色值定义在共享契约中，具体图标渲染在 Spec B 实现。此处只定义动效行为。

## 错误语义

- `finalizedRef` 守卫失败（onAssistantComplete 被多次调用）→ `dedupMessages` 兜底去重
- mergeTools localStorage 读取失败 → 默认 true
- 折叠级别 localStorage 读取失败 → 默认 all

## 验收标准

- [ ] 流式过程中不出现重复的 "已处理 N 次迭代" 摘要行
- [ ] 单迭代内渲染顺序为 T→O→C
- [ ] all 级别：只显示最后一个 Text，前面的 Text 折叠为摘要
- [ ] all 级别：如果最后一个 Text 后有工具调用，工具调用也显示
- [ ] minimal 级别：显示所有 Text，工具按 mergeTools 设置合并或各自显示
- [ ] none 级别：工具完全展开，不合并
- [ ] mergeTools toggle 在 all 和 minimal 级别下生效
- [ ] 设置面板中切换折叠级别后立即生效，无需刷新
- [ ] 工具 running 状态有脉冲动画
- [ ] 工具 done 状态过渡平滑

## 验证范围

- 手动测试：发送消息触发多轮迭代+多工具调用，验证各折叠级别和 mergeTools 组合
- 手动测试：流式过程中观察是否出现重复摘要行
- 手动测试：切换设置面板中的折叠级别，验证即时生效
- 手动测试：工具执行过程中观察状态色点动画
