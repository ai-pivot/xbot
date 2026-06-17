---
title: "功能对比"
weight: 65
---

# xbot 与其他 AI Agent 对比

你听过 Codex、Claude Code、OpenCode、Gemini CLI。它们都是优秀的**终端专用**
AI 编程 Agent。**xbot 的思路完全不同**：一个 Agent，全渠道。

{{< hint type=important >}}
核心洞察：大多数 AI Agent 只活在一个终端窗口里。xbot 打破终端边界，
让你的全团队通过**飞书、QQ、浏览器、终端**访问同一个 Agent——共享凭据、
7×24 在线。
{{< /hint >}}

## 概览

| 功能 | **xbot** | Codex | Claude Code | OpenCode |
|---------|----------|-------|-------------|----------|
| **主要渠道** | 飞书 · QQ · Web · CLI | 终端 | 终端 | 终端 |
| **自托管服务器** | ✅ 常驻运行 | ❌ | ❌ | ❌ |
| **团队 LLM 共享** | ✅ 管理员配一次 | ❌ 各自配 | ❌ 各自配 | ❌ 各自配 |
| **飞书集成** | ✅ 文档、多维表格、云盘、卡片 | ❌ | ❌ | ❌ |
| **Web 聊天界面** | ✅ 内置 | ❌ | ❌ | ✅ |
| **全功能 TUI** | ✅ 鼠标、主题、面板 | ✅ | ✅ | ✅ |
| **子 Agent** | ✅ + 群聊辩论 | ✅ | ✅ | ✅ |
| **MCP 协议** | ✅ | ✅ | ✅ | ✅ |
| **插件系统** | ✅ 工具、hooks、widget | ❌ | ❌ | ✅ |
| **AI 自助配置** | ✅ `config` + `tui_control` | ❌ | ❌ | ❌ |
| **定时任务 (Cron)** | ✅ | ❌ | ❌ | ❌ |
| **技能 (能力包)** | ✅ | ❌ | ❌ | ✅ |
| **License** | MIT | Apache 2.0 | 专有 | MIT |

## xbot 的优势

### 📱 多渠道接入

这是 xbot 的杀手锏。同一个 Agent 实例可以从以下渠道访问：

- **飞书** — 在任意群里 @机器人
- **QQ / NapCat** — 通过 QQ 聊天窗口
- **Web** — 完整浏览器聊天界面，支持注册、登录、邀请制
- **CLI** — 全功能 TUI

其他 Agent 要求每个用户安装并运行本地 CLI。而 xbot 部署一次，全团队就能
通过已有的工具连接。

### 🔑 团队共享 LLM 订阅

在 Codex / Claude Code / OpenCode 中，每个团队成员都要自己配置 API Key。
在 xbot Server 模式下，**管理员配置一次**，全团队立即使用——无需个人密钥管理。

可以创建多个订阅（工作用 Claude、个人用 DeepSeek），用 `Ctrl+P` 按会话切换。

### 🏢 飞书深度集成

xbot 包含原生飞书工具集：读写飞书文档、操作多维表格、管理云盘文件、发送
交互式消息卡片、搜索知识库。没有其他 AI Agent 提供这些。

### 🤖 群聊会议模式

xbot 的 `CreateChat` + `SendMessage` 工具支持**多 Agent 主持讨论**（会议模式）。
多个子 Agent 就一个架构决策辩论，通过 `@mentions` 触发特定 Agent 发言。其他
Agent 只支持一对一子 Agent 委派。

### ⏰ 常驻运行 + 定时任务

Server 模式作为系统服务 7×24 运行。内置 `Cron` 工具让 Agent 调度任务——
"明天 9 点提醒我 review PR" 或 "运行夜间测试套件"。其他终端 Agent 关终端就停。

### 🎛️ AI 原生配置

Agent 可以通过 `config` 和 `tui_control` 工具调整自己的设置和界面。说
"切换深色主题" 或 "把最大并发设为 5"，Agent 直接处理。没有其他 Agent 提供这个。

## 诚实地说：其他工具的优势

| 领域 | 其他工具的优势 |
|------|---------------------|
| **IDE 集成** | Codex 有 VS Code 插件；xbot 仅 TUI/Web/聊天 |
| **本地代码编辑** | Claude Code / Codex 深度优化了仓库内代码编辑工作流 |
| **Git 原生** | 这些 Agent 围绕 git diff/commit 工作流构建；xbot 更通用 |
| **生态成熟度** | Codex/Claude Code 社区更大、集成更多 |

## 什么时候选 xbot

- ✅ **需要团队级访问**，超越终端（飞书、Web、QQ）
- ✅ **想要共享 LLM 凭据**，无需每人管理密钥
- ✅ **在飞书生态中工作**，需要文档/多维表格自动化
- ✅ **想要常驻 Agent**，关终端不停
- ✅ **需要定时/自动化任务**，通过 Agent 执行

## 什么时候其他工具更好

- ❌ 想要 IDE 集成（VS Code）代码编辑 → 试 Codex
- ❌ 只需要单人终端编程助手 → Claude Code / OpenCode
- ❌ 需要深度 git 工作流自动化 → Codex / Claude Code

{{< hint type=note >}}
**两全其美：** xbot 不取代你的终端编程 Agent——它是补充。用 Codex/Claude Code
做 IDE 内编辑，用 xbot 做团队可访问的飞书/QQ/Web 自动化、定时任务和共享 AI 工作流。
{{< /hint >}}

## 参见
- [快速开始](/zh-cn/getting-started/) — 5 分钟快速上手
- [使用场景](/zh-cn/use-cases/) — 真实场景
- [高级技巧](/zh-cn/tips/) — 高级用户技巧
