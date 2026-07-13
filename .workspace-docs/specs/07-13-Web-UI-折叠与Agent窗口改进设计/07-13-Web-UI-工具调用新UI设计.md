---
type: Design Spec
title: Web UI 工具调用新 UI 设计
description: Lucide 图标体系、状态色点、极简合并行格式、工具内容渲染优化
tags:
  - spec
status: draft
repos:
  xbot: 9662990
---

# Web UI 工具调用新 UI 设计

> 主 Spec: [Web UI 折叠与 Agent 窗口改进设计](./07-13-Web-UI-折叠与Agent窗口改进设计.md)

## 目标

1. 工具调用图标替换为 Lucide 线条图标体系
2. 状态指示器从 emoji 字符改为状态色点 + CSS 动画
3. 合并行格式从 `C1 · C2 · C3 (N 个工具)` 改为图标极简格式
4. 优化工具运行中等状态的显示动效
5. 保持整体 UI 风格简洁紧凑，与 VSCode 一致

## 范围

### 范围内行为

- `web/src/components/agent/toolIcons.tsx`（新建）— 工具名→Lucide 图标映射表
- `web/src/components/agent/FoldedToolGroup.tsx` — 合并行格式、状态指示器
- `web/src/components/agent/ToolRender.tsx` — 工具内容渲染中的图标使用
- `web/src/components/agent/LiveIteration.tsx` — 流式工具的图标使用
- `web/src/components/agent/SubAgentProgressTree.tsx` — SubAgent 图标替换为 Asterisk
- `web/src/index.css` 或 `web/src/components/agent/agent.css`（新增/修改）— 状态色点 CSS 动画

### 范围外行为

- 折叠模型和 mergeTools toggle 逻辑（→ Spec A）
- SSE 订阅架构和 Agent 窗口冻结（→ Spec C）
- 后端数据结构

## 依赖

- Spec A 中的 `mergeTools` toggle（合并行格式受 mergeTools 控制）
- 共享契约中的工具图标映射表和状态色点 CSS 定义

## 输入

- 后端 `WebToolProgress` 结构不变：`{ name, label, status, summary, args?, detail? }`
- 工具状态值：`generating` / `running` / `done` / `error` / `pending`

## 输出

- 所有工具调用显示统一 Lucide 图标
- 状态用色点指示，running 有脉冲动画
- 合并行极简格式
- VSCode 风格的紧凑布局

## 详细设计

### 1. 工具图标映射表

新建 `web/src/components/agent/toolIcons.tsx`：

```typescript
import {
  Terminal, FileText, Search, FolderSearch, FilePlus, FilePen,
  Globe, Download, Asterisk, Wrench, GitBranch, FolderOpen,
  Calendar, MessageSquare, Users, Settings, ListTodo, Bot,
  Edit, FileSearch, Clock, Link2, Layers, Zap, type LucideIcon
} from 'lucide-react'

const TOOL_ICON_MAP: Record<string, LucideIcon> = {
  // 文件操作
  Shell:        Terminal,
  Read:         FileText,
  Grep:         Search,
  Glob:         FolderSearch,
  Cd:           FolderOpen,
  FileCreate:   FilePlus,
  FileReplace:  FilePen,
  Edit:         Edit,

  // 网络
  WebSearch:    Globe,
  Fetch:        Download,

  // Agent 相关
  SubAgent:     Asterisk,
  CreateChat:   Asterisk,
  SendMessage:  MessageSquare,
  Worktree:     GitBranch,

  // 工具管理
  ManageTools:  Wrench,
  Skill:        Zap,
  config:       Settings,
  TodoWrite:    ListTodo,
  context_edit: Edit,
  tui_control:  Settings,

  // 时间/任务
  Cron:         Clock,

  // 文件搜索
  WebFetch:     Download,

  // 记忆
  memory_write: Layers,
  memory_list:  Layers,
  DownloadFile: Download,

  // 其他
  ChatHistory:  MessageSquare,
  EventTrigger: Zap,
  JoinGroup:    Users,
  LeaveGroup:   Users,
  ListGroupMembers: Users,
}

export function getToolIcon(toolName: string): LucideIcon {
  return TOOL_ICON_MAP[toolName] ?? Wrench
}
```

### 2. 状态色点

替代旧的 emoji 状态符号（✓✗◑），使用 CSS 色点：

```css
/* web/src/components/agent/agent.css */

.tool-status-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  transition: background-color 0.3s ease;
}

.tool-status-running {
  background-color: var(--status-running, #3b82f6);
  animation: tool-pulse 1.5s ease-in-out infinite;
}

.tool-status-generating {
  background-color: var(--status-pending, #60a5fa);
  animation: tool-blink 1s ease-in-out infinite;
}

.tool-status-done {
  background-color: var(--text-muted, #6b7280);
}

.tool-status-error {
  background-color: var(--status-error, #ef4444);
}

.tool-status-pending {
  background-color: var(--border-color, #e5e7eb);
}

@keyframes tool-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.5; transform: scale(0.85); }
}

@keyframes tool-blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}
```

### 3. 单工具标题行格式

**当前格式**：
```
[✓] Read  1.2s
```

**新格式**：
```
[Lucide FileText icon] [色点] Read: file.go  1.2s
```

- 图标：16px Lucide 图标，颜色 `var(--text-muted)`
- 色点：6px 圆点，紧跟图标右侧
- 工具名：加粗，从 `label` 解析或使用 `name`
- 参数/路径：紧跟工具名后，颜色 `var(--text-muted)`
- 耗时：右对齐，颜色 `var(--text-muted)`

### 4. 合并行极简格式（mergeTools=true 时）

**当前格式**：
```
▸ C1 · C2 · C3 (3 个工具)
```

**新格式**：
```
[Terminal] [Search] [FilePlus] 3 次调用
```

设计细节：
- 合并行显示所有工具的图标（最多 5 个，超出显示 `+N`）
- 图标后跟工具数量：`N 次调用`（如果只有一个工具则显示工具名）
- 展开后（如果有 mergeTools + 展开）：各工具的标题行，可再展开看详情
- 箭头指示器 `▸`/`▾` 放在图标组左侧
- 高度紧凑：单行，图标 14px

**单工具合并行**（mergeTools=true 但只有一个工具）：
```
[FileText] Read: file.go  1.2s
```
不显示 `1 次调用`，直接显示工具名和参数。

### 5. SubAgent 图标替换

**当前**：`SubAgentProgressTree.tsx` 使用 `Bot` 图标。

**改为**：使用 `Asterisk` 图标。

```tsx
import { Asterisk } from 'lucide-react'

// SubAgentNode 中
const Icon = Asterisk
```

### 6. 工具内容渲染优化

`ToolRender.tsx` 中各工具类型的特殊渲染也使用 Lucide 图标：

- **Shell**：`Terminal` 图标 + `$ <command>` 格式不变
- **FileCreate**：`FilePlus` 图标 + `✓ <path>` 格式不变
- **FileReplace**：`FilePen` 图标 + diff 风格不变
- **Read**：`FileText` 图标 + `📖` emoji 移除，改为图标
- **Grep**：`Search` 图标 + `🔍` emoji 移除
- **Glob**：`FolderSearch` 图标 + `📂` emoji 移除
- **默认**：`Wrench` 图标

### 7. 图标样式规范

```css
/* 工具图标通用样式 */
.tool-icon {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
  color: var(--text-muted);
}

/* 合并行中的图标组 */
.tool-icon-group {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.tool-icon-group .tool-icon {
  width: 14px;
  height: 14px;
}

/* 单工具标题行中的图标 */
.tool-icon-single {
  width: 16px;
  height: 16px;
}
```

## 错误语义

- 工具名不在映射表中 → 使用 `Wrench` 图标
- 工具状态未知 → 当作 `pending` 处理
- 图标加载失败 → Lucide 图标是内联 SVG 组件，不存在加载失败场景

## 验收标准

- [ ] 所有工具调用使用 Lucide 线条图标，无 emoji
- [ ] 工具状态用色点指示：running=蓝色脉冲，done=灰色，error=红色
- [ ] running→done 过渡平滑（CSS transition）
- [ ] 合并行显示工具图标组 + 调用数量
- [ ] 单工具标题行显示图标 + 色点 + 工具名 + 参数
- [ ] SubAgent 使用 Asterisk 图标
- [ ] 未映射工具使用 Wrench 图标
- [ ] 图标尺寸紧凑（14-16px），整体布局简洁
- [ ] CSS 动画不影响滚动性能（使用 transform/opacity，避免 reflow）

## 验证范围

- 手动测试：触发各种工具调用（Shell、Read、Grep、Glob、FileCreate、FileReplace、SubAgent），验证图标正确
- 手动测试：工具执行过程中观察色点动画和状态过渡
- 手动测试：mergeTools=true 时合并行格式正确
- 手动测试：未映射工具使用 Wrench 图标
