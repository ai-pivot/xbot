---
title: "xbot"
weight: 0
geekdocHidden: true
---

{{< columns >}}

**xbot** 是一个自托管 AI Agent 框架。部署一次在你自己的服务器上，通过
**飞书、QQ、终端、浏览器**与它对话，它能调用工具完成实际工作。

![xbot CLI 流式输出](/img/cli/streaming.gif)

<--->

{{< button href="/zh-cn/getting-started" >}}快速开始{{< /button >}}
&nbsp;
{{< button href="/zh-cn/installation" >}}安装指南{{< /button >}}

{{< /columns >}}

## 为什么选 xbot？

大多数 AI 编程 Agent 只活在终端里。**xbot 不一样**：一个 Agent，全渠道。
在你的服务器上配置一次，全团队就能通过已有的工具与同一个 Agent 对话。

| | xbot | Codex / Claude Code / OpenCode |
|--|------|-------------------------------|
| **多渠道** | 飞书 · QQ · Web · CLI | 仅终端 |
| **团队共享 LLM** | 管理员配一次，所有人用 | 各自配置 API Key |
| **自托管** | ✅ 数据不出服务器 | ✅ |
| **飞书集成** | 文档、多维表格、云盘、卡片 | ❌ |
| **子 Agent + 群聊** | 委派、并行、辩论 | 仅子 Agent |
| **插件系统** | 工具、hooks、widget、渠道插件 | 有限 |

{{< hint type=important >}}
**最常见场景**：部署 Server 模式 → 连接飞书应用 → 全团队在群里 @机器人对话，
无需各自配置 API Key。
{{< /hint >}}

## 核心特性

- 🧠 **多轮对话 + 工具调用** — Shell、文件读写、网页搜索、定时任务、子 Agent 委派
- 📱 **多渠道接入** — 同一个 Agent，飞书 / QQ / 终端 / 浏览器不同入口
- 🔑 **团队共享 LLM 订阅** — 管理员配置一次，全团队直接使用；支持多订阅切换
- 🖱️ **全功能 TUI** — 鼠标交互、命令面板(Ctrl+K)、多会话侧边栏、主题系统
- 🏠 **完全自托管** — 数据不出你的服务器
- 🧩 **可扩展** — Skills、SubAgents、MCP 协议、Plugin 系统
- 🤖 **AI-Native 配置** — Agent 可通过 `config` 和 `tui_control` 工具自行调整配置和界面

## 我该用哪个渠道

| 渠道 | 适合谁 | 特点 |
|------|--------|------|
| **CLI** | 开发者、终端用户 | 全功能 TUI，流式输出，工具调用 |
| **飞书** | 团队协作 | 在群里 @机器人 对话，支持消息卡片 |
| **QQ / NapCat** | 个人或小圈子 | QQ 聊天窗口交互 |
| **Web** | 任何有浏览器的人 | 网页聊天，注册/登录，邀请制 |

## 快速开始

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

安装完成后运行 `xbot-cli`，首次会弹出 Setup 向导引导你配置 API Key。

详见 [快速开始](/zh-cn/getting-started/) 或 [安装指南](/zh-cn/installation/)。

## 架构

```
┌──────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────┐
│  飞书    │────▶│  Dispatcher  │────▶│  Backend    │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│  (RPC)      │◀────│ (llm/)   │
│  Web     │     └──────────────┘     │             │     └──────────┘
│  CLI     │                          │  Transport  │
└──────────┘                          │  (local/    │────▶ 工具
                                      │   remote)   │      (tools/)
                                      │  Agent Loop │────▶ 记忆
                                      │  (agent/)   │      (memory/)
                                      └────────────┘
```

核心设计：**Backend** 为纯 RPC 客户端接口（零业务逻辑），**Transport** 层负责实际执行
（local 直接调用 Agent，remote 通过 WebSocket 转发）。

阅读完整 [架构概览](/zh-cn/architecture/)。
