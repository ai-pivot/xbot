---
type: Design Spec
title: Web UI 折叠与 Agent 窗口改进设计
description: Web 前端工具调用 UI 重构、四级折叠模型、Agent 窗口焦点切换冻结修复
tags:
  - spec
status: draft
repos:
  xbot: 9662990
---

# Web UI 折叠与 Agent 窗口改进设计

## 总目标

改进 Web 前端的工具调用显示和 Agent 窗口体验，使其达到 VSCode 级别的简洁、紧凑、优雅：
1. 修复进度摘要行重复显示
2. 工具调用 UI 重构为 Lucide 图标 + 极简格式
3. 四级折叠/展开模型（三级折叠 + 工具合并 toggle）
4. Agent 窗口焦点切换时冻结问题修复
5. SubAgent 独立面板显示修复
6. PhaseDone 空白窗口修复

## 范围

### 范围内

- `web/src/components/agent/` 下的渲染组件（AssistantMessage、TurnBody、IterationHistory、LiveIteration、FoldedToolGroup、ToolRender、SubAgentProgressTree）
- `web/src/hooks/useProgressStream.ts` — 进度事件处理与 finalize 逻辑
- `web/src/hooks/useActiveSSESubscription.ts` — SSE 订阅架构
- `web/src/components/agent/progressStore.ts` — ProgressStore 状态机
- `web/src/workspace/panels/AgentPanel.tsx` — Agent 面板入口
- `web/src/components/settings/SettingsCollapse.tsx` — 折叠级别设置面板
- `web/src/i18n/zh-CN.ts` / `en.ts` — i18n 文案

### 范围外

- 后端 Go 代码（`agent/`、`channel/web/`）— 不改动后端数据结构和事件流
- TUI 渲染逻辑
- 非 Web 渠道的显示

## 关键决策

| 决策 | 选择 | 理由 |
|------|------|------|
| SSE 订阅架构 | 方案 C：单 EventSource + 动态订阅列表 | 改动最小，复用现有 ProgressStore，不增加连接数 |
| 文本/工具显示顺序 | T→O→C（推理→文本→工具） | 更符合实际生成顺序，改动仅在前端渲染顺序 |
| 折叠模型 | 三级折叠（all/minimal/none）+ `mergeTools` 独立 toggle | 保持现有级别体系，工具合并作为正交选项 |
| 工具图标体系 | Lucide 线条图标 + 状态色点 | VSCode 风格，矢量无损，CSS 动画 |
| PhaseDone 处理 | 引入 "finalizing" 过渡态 | 平滑过渡无空白窗口 |
| SubAgent 图标 | Lucide `Asterisk` | — |
| 未映射工具图标 | Lucide `Wrench` | — |

## 共享契约

### 1. CollapseLevel 类型变更

```typescript
// types/agent.ts
type CollapseLevel = 'all' | 'minimal' | 'none'

// 新增独立 toggle（存储在 localStorage，与 collapseLevel 并列）
// key: "xbot-merge-tools"
// 值: boolean，默认 true
```

### 2. 工具图标映射表

```typescript
// web/src/components/agent/toolIcons.tsx（新建）
import { Terminal, FileText, Search, FolderSearch, FilePlus, FilePen,
         Globe, Download, Asterisk, Wrench, GitBranch, ... } from 'lucide-react'

const TOOL_ICON_MAP: Record<string, LucideIcon> = {
  Shell:        Terminal,
  Read:         FileText,
  Grep:         Search,
  Glob:         FolderSearch,
  FileCreate:   FilePlus,
  FileReplace:  FilePen,
  WebSearch:    Globe,
  Fetch:        Download,
  SubAgent:     Asterisk,
  CreateChat:   Asterisk,
  SendMessage:  Asterisk,
  Worktree:     GitBranch,
  Cd:           FolderOpen,
  // ... 其余工具按需映射
}

// 未映射工具的 fallback
const FALLBACK_ICON = Wrench
```

### 3. 工具状态图标 → 状态色点

```css
/* 工具状态色点（替代旧 emoji 状态符号） */
.tool-status-running { /* 蓝色脉冲动画 */ animation: pulse-blue 1.5s infinite; }
.tool-status-done    { /* 灰色静态点 */ }
.tool-status-error   { /* 红色静态点 */ }
.tool-status-pending { /* 透明/浅灰点 */ }
```

### 4. ProgressStore finalizing 状态

```typescript
// progressStore.ts 新增
interface ProgressSnapshot {
  // ... 现有字段
  phase: 'idle' | 'streaming' | 'finalizing' | 'done'
}
```

- `finalizing`：PhaseDone 已收到但 `text` 事件未到达
- 进入 finalizing 时保持进度快照可见，停止脉冲动画
- 3s 超时自动转为 done（reset）

## 子 Spec 索引

| Spec | 标题 | 范围 |
|------|------|------|
| [Spec A](./07-13-Web-UI-消息合并与折叠模型设计.md) | 消息合并与折叠模型 | 问题 1 + 3 + 4 |
| [Spec B](./07-13-Web-UI-工具调用新UI设计.md) | 工具调用新 UI | 问题 2 + 4 |
| [Spec C](./07-13-Web-UI-Agent窗口持久显示设计.md) | Agent 窗口持久显示 | 问题 5 + 6 + PhaseDone |

## 依赖 DAG

```
Spec A（消息合并与折叠模型）──┐
                              ├──→ 集成验收
Spec B（工具调用新 UI）──────┘    （A+B 共享折叠模型和渲染顺序）
                              
Spec C（Agent 窗口持久显示）──→ 独立验收
```

- **Spec A 和 Spec B 可并行开发**，但需共同遵守共享契约中的折叠模型和渲染顺序约定
- **Spec C 完全独立**，可单独开发和验收
- **集成验收**：Spec A + B 合并后验证四级折叠 + 工具 UI 的完整体验

## 集成策略

1. Spec A 先合并（消息合并修复 + 渲染顺序调整 + 折叠模型）
2. Spec B 在 A 的基础上合并（工具图标体系 + 极简合并行格式）
3. Spec C 独立合并（SSE 订阅架构 + finalizing 状态 + SubAgent 面板修复）
4. 全量验收：三个 Spec 的功能点同时验证

## 整体验收标准

- [ ] "已处理 N 次迭代 · 调用 N 个工具" 不再重复显示
- [ ] 工具调用使用 Lucide 图标 + 状态色点
- [ ] 四级折叠/展开模型工作正常（all/minimal/none + mergeTools toggle）
- [ ] 设置面板中折叠级别点击后立即生效（无需刷新）
- [ ] 工具运行中状态有脉冲动画，完成后平滑过渡
- [ ] 点击其他面板时 Agent 窗口保持运行状态显示
- [ ] SubAgent 独立面板正确显示消息和进度
- [ ] PhaseDone 后无空白窗口（finalizing 过渡态）
- [ ] T→O→C 渲染顺序正确（推理→文本→工具）
- [ ] 整体 UI 风格简洁紧凑，与 VSCode 一致
