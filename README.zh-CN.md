<p align="center">
  <strong>xbot</strong> — 自托管 AI Agent，以 Web UI 为一等公民
</p>

<p align="center">
  浏览器 · 飞书 · QQ · 终端 —— 一个 Agent，一份配置，跑在你自己的服务器上
</p>

<p align="center">
  <a href="https://github.com/ai-pivot/xbot/actions/workflows/ci.yml"><img src="https://github.com/ai-pivot/xbot/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/ai-pivot/xbot/blob/master/LICENSE"><img src="https://img.shields.io/github/license/ai-pivot/xbot" alt="License"></a>
  <a href="https://github.com/ai-pivot/xbot/releases"><img src="https://img.shields.io/github/v/release/ai-pivot/xbot?display_name=tag" alt="Release"></a>
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go" alt="Go Version">
</p>

<p align="center">
  <a href="README.md">English</a>
  &nbsp;·&nbsp;
  <a href="https://ai-pivot.github.io/xbot/">文档</a>
  &nbsp;·&nbsp;
  <a href="CHANGELOG.md">更新日志</a>
</p>

<p align="center">
  <img alt="xbot Web UI — 真实会话：多轮对话、工具调用、逐迭代进度" src="docs-site/static/img/app/hero.png" width="860">
</p>

---

## xbot 是什么？

**xbot** 是跑在你自己服务器上的自托管 AI Agent，**以浏览器为主要使用界面**。
它通过工具干活 —— Shell、文件读写、联网搜索、定时任务、子代理、插件 ——
所有数据都留在你自己的服务器上。

**Web UI 是主界面**：会话与实时流式输出、文件预览、Git diff、内置终端、
插件面板、模型选择器，全都在浏览器里。飞书 / QQ / 终端通道把**同一个**
Agent 接到团队日常使用的工具上，共用同一份 LLM 配置。

| | xbot | 纯终端 Agent |
|--|------|-------------|
| **主界面** | **浏览器 Web UI**（+ 飞书 · QQ · CLI） | 仅终端 |
| **团队 LLM** | 管理员配置一次，所有人共用 | 每人自带 key |
| **自托管** | ✅ 数据不出你的服务器 | ✅ |
| **插件系统** | Web 视图、面板、工具、Hook、通道插件 | 有限 |
| **子代理 + 群聊** | 委派、并行、多专家讨论 | 仅子代理 |
| **飞书工具** | 文档、多维表格、云盘、交互卡片 | ❌ |

## 快速开始

### 1. 安装（一条命令）

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

<details>
<summary>🇨🇳 国内网络（无需 VPN）</summary>

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

脚本会自动探测可用的 CDN 镜像并代理所有 GitHub 下载，也可以手动设置
`GH_MIRROR=ghfast.top`。

</details>

安装脚本一条命令装齐：**二进制 + Web UI + 全部内置插件，并自动打开 web 通道**。

| 组件 | 位置 |
|------|------|
| CLI + server 二进制 | `~/.local/bin/xbot-cli` |
| Web UI + 内置插件 + 通道激活 | `$XBOT_HOME`（`xbot-cli setup` 自动完成） |

> 已经是装好的状态？`xbot-cli setup` 重跑补齐，`xbot-cli setup --check` 自检。

**或者：把这句话复制给你的 AI agent，让它帮你装**

```text
帮我在这台机器上安装并启动 xbot：
1. curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
2. xbot-cli setup --check      # 退出码 0 才算装好
3. nohup xbot-cli serve >/tmp/xbot.log 2>&1 &
4. curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8082   # 必须输出 200
失败就读 https://ai-pivot.github.io/xbot/zh-cn/installation/ 的排错章节。
```

可选参数只有 5 个（`MODE` / `PORT` / `XBOT_HOME` / `INSTALL_PATH` / `CHANNEL`），
其余全部配置见 [安装与配置](https://ai-pivot.github.io/xbot/zh-cn/installation/)。

### 2. 启动服务

```bash
xbot-cli serve
```

就这一步 —— 安装脚本已经开启了 web 通道。想让它开机自启？
`xbot-cli serve --install-service`（或见 [安装指南](https://ai-pivot.github.io/xbot/zh-cn/installation/)）。

### 3. 打开 Web 界面并完成配置

浏览器访问 **http://localhost:8082**：

1. **创建账号** —— 新装机的**第一个注册无需邀请码**，它就是管理员账号。
2. **配置 LLM** —— 右下角齿轮 → **LLM** → 添加订阅：供应商、**Base URL**
   （形如 `https://…/v1`）、API Key，然后刷新并选择模型。
3. **开始聊天** —— 左侧「+ New Session」建会话，在下方输入框说话。

配置模型

打开 **设置 → LLM**，添加一个订阅：

| 字段 | 示例 |
|------|------|
| Provider | `openai`（或 `anthropic`，以及任何 OpenAI 兼容端点） |
| Base URL | `https://api.deepseek.com/v1`（DeepSeek / Qwen / Ollama / vLLM …） |
| API key | `sk-…` |
| Model | `deepseek-chat`、`glm-5.3`、`gpt-5` … |

xbot 使用**订阅系统** —— 可以创建多个订阅（工作用 Claude、个人 DeepSeek），
在输入框的模型选择器里按会话切换。模型档位（**vanguard / balance / swift**）
让子代理自动选用更便宜的模型。

## Web UI 一览

| 功能 | 位置 |
|------|------|
| **活动栏（Activity Bar）** | 最左侧 48px 图标列 —— 点击图标切换侧栏内容（会话 / 文件 / 搜索 / 任务 / 终端 / 统计 / 插件 / 技能 / Git） |
| **会话** | 左侧栏；「+ New Session」、搜索、星标、右键重命名 / 分叉 / 删除 |
| **实时进度** | 按迭代流式输出，含工具卡片、思考过程、tok/s 与 TTFT |
| **输入框** | 支持 Markdown、图片、文件附件，`Ctrl+Enter` 发送，`@` 引用文件 |
| **模型选择器** | 输入框 →「Choose model and thinking mode」；按会话选择模型与思考等级 |
| **文件预览** | 文件树里点击文件 → 语法高亮预览、图片、Mermaid 图表 |
| **Git 面板** | 内置（插件 `xbot.git-fancy`）—— 分支、diff、提交详情 |
| **终端** | 标签页里的完整 PTY（本地或远程 runner） |
| **插件** | 管理面板 + 插件提供的视图、挂件、信息栏条目 |
| **移动端** | 响应式布局，底部导航 + 触屏友好的点击区域 |

## 内置插件

随每个 release 分发，由 `xbot-cli setup` 安装：

| 插件 | 类型 | 功能 |
|------|------|------|
| **`xbot.genui`** | 通道 + 工具 | 生成式 UI —— 模型输出 TSX，在浏览器里实时渲染成可交互面板（`display_html` 工具） |
| **`xbot.git-fancy`** | 工具 + Web 视图 | Git 状态面板、逐文件 diff、提交详情，渲染在编辑区 |
| **`xbot.ambience`** | UI | 壁纸、毛玻璃效果、动态桌宠、粒子特效 |

插件激活：通道插件还需要在 `config.json` 里设置
`channels.<name>.enabled = true` —— `xbot-cli setup` 会自动写入
（且绝不会覆盖用户显式的 `false`）。

完整的 manifest / 权限 / Web 视图参考见
[插件文档](https://ai-pivot.github.io/xbot/zh-cn/plugins/)。

## 通道

所有通道驱动同一个 Agent，共用同一份 LLM 配置。

### Web（主界面）

```json
{ "web": { "enable": true, "port": 8082 } }
```

### 飞书

在[飞书开放平台](https://open.feishu.cn)创建应用，然后：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

所需权限：`im:message`、`im:message.receive_v1`、
`im:message:send_as_bot`、`contact:user.base:readonly`

### QQ / NapCat / CLI

见[通道文档](https://ai-pivot.github.io/xbot/zh-cn/channels/)。

## 内置工具

Agent 可以在对话中调用这些工具：

| 分类 | 工具 |
|------|------|
| **执行** | `Shell`（前台 → 可转后台）、`Cd` |
| **文件** | `Read`、`FileCreate`、`FileReplace`、`Grep`、`Glob`、`DownloadFile` |
| **联网** | `Fetch`、`WebSearch` |
| **视觉** | `view_image`（多模态图片输入） |
| **会话** | `CreateChat`、`SubAgent`、`SendMessage` |
| **上下文** | `context_edit`、`offload_recall`、`recall_masked` |
| **调度** | `Cron`、`TodoWrite`、`TodoList` |
| **配置** | `config`、`tui_control` |
| **协作** | `Worktree`、`EventTrigger` |
| **任务** | `task_status`、`task_kill`、`task_wait`、`task_read` |
| **其他** | `AskUser`、`ChatHistory`、`ManageTools`、`Skill`、飞书工具 |

## 扩展能力

- **技能（Skills）** —— `~/.xbot/skills/` 下的 Markdown 能力包
- **子代理（SubAgents）** —— 基于角色的子代理（`explore`、`code-reviewer` …）；自定义角色放在 `~/.xbot/agents/`
- **群聊** —— 多代理主持人制讨论（Meeting Mode）
- **MCP** —— 全局与按会话的 MCP 服务器（stdio + HTTP），跨会话连接池复用
- **Hook** —— 工具与生命周期事件的命令 / 脚本处理器

## 文档

| | |
|--|--|
| [快速开始](https://ai-pivot.github.io/xbot/zh-cn/getting-started/) | 安装 → 第一次对话 |
| [安装](https://ai-pivot.github.io/xbot/zh-cn/installation/) | 所有安装方式、离线与镜像 |
| [配置](https://ai-pivot.github.io/xbot/zh-cn/configuration/) | `config.json` 参考 |
| [插件](https://ai-pivot.github.io/xbot/zh-cn/plugins/) | Manifest、权限、Web 视图 |
| [通道](https://ai-pivot.github.io/xbot/zh-cn/channels/) | Web · 飞书 · QQ · CLI |
| [常见问题](https://ai-pivot.github.io/xbot/zh-cn/faq/) | FAQ |

## 许可证

[MIT](LICENSE)
