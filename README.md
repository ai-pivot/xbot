<p align="center">
  <strong>xbot</strong> — 自托管 AI Agent，接入你的飞书 / QQ / 终端 / 浏览器
</p>

<p align="center">
<img alt="Streaming" src="docs-site/static/img/cli/streaming.gif" width="720">
</p>

## xbot 解决什么问题

你想让 AI 帮团队做事——写代码、查文档、跑命令、操作飞书表格——但又不想把数据交给第三方 SaaS。xbot 让你**在自己的服务器上部署一个全功能 AI Agent**，通过飞书/QQ/终端/浏览器与它对话，它能调用工具完成实际工作。

**核心能力：**
- 🧠 多轮对话 + 工具调用（Shell、文件读写、网页搜索、定时任务…）
- 📱 多渠道接入（飞书、QQ、终端 TUI、Web 浏览器）——同一套 Agent，不同入口
- 🔑 团队共享 LLM 订阅（管理员配置一次，所有人直接用）
- 🧩 可扩展（Skills、SubAgents、MCP 协议、Plugin 系统）
- 🖱️ 全功能 TUI（鼠标交互、命令面板、多会话管理、主题系统）
- 🏠 完全自托管，数据不出你的服务器

## 我该用哪个渠道

| 渠道 | 适合谁 | 特点 |
|------|--------|------|
| **CLI（TUI）** | 开发者、终端重度用户 | 全功能 TUI，鼠标交互、命令面板(Ctrl+K)、多会话侧边栏、主题切换、流式输出 |
| **飞书** | 团队协作 | 在群里 @机器人 即可对话，支持消息卡片交互、SubAgent 进度穿透 |
| **QQ / NapCat** | 个人或小圈子 | 通过 QQ 聊天窗口与 Agent 交互 |
| **Web** | 任何有浏览器的人 | 网页聊天界面，支持注册/登录和邀请制 |

> 💡 **飞书场景最常见**：部署 server 模式 → 配置飞书应用 → 全团队在飞书群里 @机器人对话，无需各自配置 API Key。

## 快速开始

### 1. 安装

安装器会问你选哪种模式（区别见下文）：

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

<details>
<summary>🇨🇳 国内用户（无需翻墙）</summary>

通过公共 CDN 镜像加速，零配置即可安装：

```bash
# Linux / macOS（选一个能用的镜像）
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
# 或
curl -fsSL https://gh-proxy.com/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

```powershell
# Windows PowerShell（选一个能用的镜像）
irm https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.ps1 | iex
# 或
irm https://gh-proxy.com/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.ps1 | iex
```

脚本会自动检测可用的镜像并代理所有 GitHub 下载。也可手动指定镜像：

```bash
# Linux / macOS
GH_MIRROR=ghfast.top bash scripts/install-cn.sh
```

```powershell
# Windows
$env:GH_MIRROR="ghfast.top"; .\scripts\install-cn.ps1
```

</details>

指定安装路径：

```bash
INSTALL_PATH=~/.local/bin curl -fsSL ... | bash  # 自定义路径
```

### 2. 两种安装模式

安装器会让你选择：

| | Standalone（单机） | Server（服务端） |
|--|---------------------|------------------|
| **架构** | CLI 直接运行 Agent | 后台 Server + CLI 远程连接 |
| **适合** | 个人使用 | 团队使用、多渠道接入 |
| **多渠道** | ❌ 仅 CLI | ✅ 飞书/QQ/Web 同时启用 |
| **Web UI** | ❌ | ✅ 浏览器聊天界面 |
| **LLM 共享** | 各自配置 | 管理员配一次，全团队用 |
| **后台运行** | 关终端就停 | 系统服务，开机自启 |

> **大多数团队应选 Server 模式。** 个人开发者想快速体验可选 Standalone。

### 3. 首次运行 & 配置 LLM

安装完成后直接运行：

```bash
xbot-cli
```

首次运行会自动弹出 **Setup 向导**，引导你创建 LLM 订阅：

1. 选择 LLM 提供商（OpenAI / Anthropic / 自定义兼容 API）
2. 输入 API Key
3. 配置 API 地址（使用第三方服务时修改，如 DeepSeek、Qwen、Ollama）
4. 选择模型
5. 配置模型层（Vanguard / Balance / Swift，可按不同场景选用不同模型）

也可以随时用 `/setup` 或 `Ctrl+K → Setup` 重新配置。

> ⚠️ **提示**：xbot 使用**订阅（Subscription）系统**管理 LLM 配置，而非全局单一 `llm` 字段。你可以创建多个订阅（如工作用 Claude、个人用 DeepSeek），在不同会话中随时切换。

### 4. 配置文件

配置文件位于 `~/.xbot/config.json`。首次运行 Setup 向导会自动生成。

**Server 模式管理员配置订阅示例：**

管理员在 TUI 中通过 `/setup` 创建订阅后，团队成员即可直接使用，无需各自配置。

**Standalone 模式配置示例：**

```json
{
  "subscriptions": [
    {
      "name": "default",
      "provider": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4o"
    }
  ]
}
```

**使用 DeepSeek：**

```json
{
  "subscriptions": [
    {
      "name": "deepseek",
      "provider": "openai",
      "api_key": "your-deepseek-key",
      "base_url": "https://api.deepseek.com/v1",
      "model": "deepseek-chat"
    }
  ]
}
```

## TUI 功能速览

xbot CLI 提供了强大的终端用户界面：

| 功能 | 操作 |
|------|------|
| **命令面板** | `Ctrl+K` 打开，模糊搜索所有命令和操作 |
| **多会话管理** | 侧边栏显示所有会话，鼠标点击切换，`/new` 或 `Ctrl+K → New Session` 新建 |
| **主题切换** | `Ctrl+K → Theme` 或 `/palette theme`，支持自定义主题 |
| **模型切换** | `Ctrl+N` 循环切换模型，`Ctrl+P` 快速切换订阅，`/model` 查看/切换 |
| **上下文管理** | `/context` 查看 token 用量，`/clear` 清理对话 |
| **SubAgent 查看** | 侧边栏可直接查看子 Agent 会话的实时进度 |
| **鼠标交互** | 点击侧边栏、滚动消息、点击设置项、选择 Palette 选项 |

> 💡 在 TUI 中输入 `/` 查看所有可用 slash 命令。

## 渠道配置

每个渠道通过 `~/.xbot/config.json` 启用。

### 飞书

在 [飞书开放平台](https://open.feishu.cn) 创建应用后：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

最小必需的应用权限：`im:message`、`im:message.receive_v1`、`im:message:send_as_bot`、`contact:user.base:readonly`

详见 [飞书配置指南](https://cjiw.github.io/xbot/channels/feishu/)。

### QQ / NapCat / Web

详见各渠道的配置指南：[Channels](https://cjiw.github.io/xbot/channels/)

## 从源码构建

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build          # 构建 xbot (server + runner)
make run            # 构建并运行 server
```

仅构建 CLI：

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

## 功能概览

### 内置工具

Agent 在对话中可以调用这些工具：

| 分类 | 工具 | 说明 |
|------|------|------|
| **命令执行** | `Shell` | 在沙箱中执行命令（支持 Docker / 远程 / 无沙箱） |
| | `Cd` | 切换工作目录，后续工具自动跟随 |
| **文件操作** | `Read` | 读取文件（带行号） |
| | `FileCreate` / `FileReplace` | 创建和编辑文件（支持行范围限定、正则替换） |
| | `Grep` | 正则搜索文件内容 |
| | `Glob` | Glob 模式匹配文件 |
| | `DownloadFile` | 从 URL 下载文件 |
| **网页** | `Fetch` | 抓取网页内容 |
| | `WebSearch` | Tavily 网络搜索 |
| **会话管理** | `CreateChat` | 创建 SubAgent 或 Group 会话 |
| | `SubAgent` | 委派任务给专门化的子 Agent |
| | `SendMessage` | 向 SubAgent 或群组发送消息 |
| **上下文** | `context_edit` | 编辑对话上下文（裁剪/替换/删除历史消息） |
| | `offload_recall` / `recall_masked` | 取回被压缩或屏蔽的工具结果 |
| **任务调度** | `Cron` | 定时任务（cron 表达式或一次性延迟） |
| | `TodoWrite` / `TodoList` | 管理结构化 TODO 列表（跨会话持久化） |
| **配置** | `config` | AI 自助读写 xbot 配置 |
| | `tui_control` | AI 自助操作 TUI 界面 |
| **协作** | `Worktree` | Git worktree 多 Agent 工作区隔离 |
| | `EventTrigger` | Webhook 事件订阅 |
| **飞书** | 飞书工具集 | 操作飞书文档、多维表格、云盘等 |
| **其他** | `AskUser` | 向用户提问 |
| | `ChatHistory` | 查询聊天历史 |
| | `ManageTools` | 管理 MCP Server |
| | `Skill` | 加载 Skill 指导文档 |
| | `task_status` / `task_kill` | 管理后台任务 |

### 记忆系统

| | Flat（默认） | Letta (MemGPT) |
|--|-------------|----------------|
| 核心记忆 | 内存块 | SQLite（始终在上下文中） |
| 归档记忆 | Grep 搜索 | 向量搜索 |
| 依赖 | 无 | 需要嵌入模型 |

### Skills & SubAgents

- **Skills** — Markdown 定义的能力包，放在 `~/.xbot/skills/`。Agent 按需加载获取专门指导
- **SubAgents** — 基于角色的子 Agent（explore、code-reviewer 等），自定义角色放 `~/.xbot/agents/`
- **Group Chat** — 多 SubAgent 主持讨论（Meeting Mode），通过 @mentions 触发发言

### MCP 协议

支持全局和会话级 MCP Server，stdio 和 HTTP 传输。通过 `ManageTools` 工具动态管理。

### Plugin 系统

GUI 插件扩展系统，支持工具、hooks、上下文增强器的动态加载。

## 架构

```
┌──────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────┐
│  飞书    │────▶│  Dispatcher  │────▶│  Backend    │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│  (RPC)      │◀────│ (llm/)   │
│  Web     │     └──────────────┘     │             │     └──────────┘
│  CLI     │                          │  Transport  │
└──────────┘                          │  (local/    │────▶ 工具
                                      │   remote)   │      (tools/)
                                      │             │
                                      │  Agent Loop │────▶ 记忆
                                      │  (agent/)   │      (memory/)
                                      └────────────┘
```

核心设计：
- **Backend** — 纯 RPC 客户端接口，零业务逻辑，方法均为 1-3 行类型化调用
- **Transport** — 执行层，`localTransport` 直接调用 Agent，`remoteTransport` 通过 WebSocket 转发
- **Pipeline** — 中间件链组装系统提示（prompt → 全局上下文 → Skills → Agents → Memory → 用户消息）
- **并发** — 全局 LLM 信号量控制、Read 工具并行执行、SubAgent 独立 goroutine

## 文档

完整文档：[cjiw.github.io/xbot](https://cjiw.github.io/xbot/)

| 文档 | 说明 |
|------|------|
| [安装指南](https://cjiw.github.io/xbot/installation/) | 两种模式详解、Setup 向导 |
| [渠道配置](https://cjiw.github.io/xbot/channels/) | 飞书 / QQ / NapCat / Web / CLI |
| [配置参考](https://cjiw.github.io/xbot/configuration/) | config.json 完整字段说明 |
| [沙箱指南](https://cjiw.github.io/xbot/guides/sandbox-docker/) | Docker / 远程沙箱配置 |
| [架构](https://cjiw.github.io/xbot/architecture/) | 系统设计和数据流 |
| [CHANGELOG](CHANGELOG.md) | 版本历史 |

## License

MIT
