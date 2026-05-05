<p align="center">
  <strong>xbot</strong> — pluggable AI Agent framework
</p>

## What is xbot

xbot is a Go framework for building AI agents. It provides a message bus + plugin architecture where an **Agent** (LLM + tools + memory) receives messages from any **Channel** (CLI, Feishu, QQ, Web) through a **Bus**, processes them in a multi-turn loop with tool calling, and sends replies back.

```
Channel → Bus → Agent → LLM ↔ Tools → Bus → Channel
```

Designed for self-hosted deployments. Supports **OpenAI** and **Anthropic** as native LLM providers, plus any OpenAI-compatible API (DeepSeek, Qwen, Ollama, etc.) via the `openai` provider with a custom `base_url`.

## Quick Start

### Install CLI

```bash
# Linux / macOS (amd64, arm64) — installs xbot-cli only
curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Specific version
VERSION=v0.0.7 curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash

# Custom install path (default: /usr/local/bin)
INSTALL_PATH=~/.local/bin curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash
```

### Build from Source

```bash
git clone https://github.com/CjiW/xbot.git && cd xbot
make build          # Builds xbot (server + runner)
make run            # Build and run server
```

To build `xbot-cli` only:

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

### Configure

On first run, `xbot-cli` launches a setup wizard. Or edit `~/.xbot/config.json`:

**OpenAI (or any compatible API):**

```json
{
  "llm": {
    "provider": "openai",
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o"
  },
  "sandbox": { "mode": "none" },
  "agent": { "memory_provider": "flat" }
}
```

**Anthropic:**

```json
{
  "llm": {
    "provider": "anthropic",
    "api_key": "sk-ant-xxx",
    "model": "claude-sonnet-4-20250514"
  },
  "sandbox": { "mode": "none" },
  "agent": { "memory_provider": "flat" }
}
```

## Channels

Each channel is a pluggable adapter on the message bus. Enable channels via environment variables.

### CLI (TUI)

The default channel — a full-featured terminal UI built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

```bash
xbot-cli                # Interactive TUI
xbot-cli "your prompt"  # One-shot mode
echo "prompt" | xbot-cli # Pipe mode
```

**Keyboard shortcuts:**

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+Enter` / `Ctrl+J` | Insert newline |
| `Tab` | Autocomplete (`/` commands, `@` file paths) |
| `↑` `↓` | Input history / scroll messages |
| `PgUp` `PgDn` | Page up / down |
| `Home` `End` | Jump to top / bottom |
| `Esc` | Cancel / clear input |
| `Ctrl+C` | Interrupt current operation |
| `Ctrl+K` | Context editing (trim history by turns) |
| `Ctrl+O` | Toggle tool summary expand/collapse |
| `Ctrl+E` | Toggle long message folding |
| `^` | Background task panel |

**Slash commands:** `/settings` `/setup` `/update` `/new` `/clear` `/compact` `/context` `/model` `/models` `/cancel` `/search` `/tasks` `/su` `/help` `/exit`

**Features:** streaming output, markdown + Mermaid rendering, 6 color themes, background tasks, message search, built-in skill/agent creator.

See [docs/cli-channel.md](docs/cli-channel.md) for full documentation.

### Feishu (Lark)

**WebSocket 长连接模式 — 不需要公网 IP 或回调 URL。** 飞书渠道使用 Lark SDK 的 WebSocket 长连接，由 xbot 主动连接飞书服务器，无需暴露端口。支持交互式消息卡片、文档/知识库/多维表格读写、文件上传、话题回复。

配置方式：编辑 `~/.xbot/config.json` 中的 `feishu` 字段，或通过 TUI `/settings` 面板修改。

| 字段 | 环境变量 | 说明 |
|------|---------|------|
| `enabled` | `FEISHU_ENABLED` | 设为 `true` 启用 |
| `app_id` | `FEISHU_APP_ID` | 飞书应用 App ID |
| `app_secret` | `FEISHU_APP_SECRET` | 飞书应用 App Secret |
| `encrypt_key` | `FEISHU_ENCRYPT_KEY` | 事件加密密钥（可选） |
| `verification_token` | `FEISHU_VERIFICATION_TOKEN` | 验证 Token（可选） |
| `domain` | `FEISHU_DOMAIN` | 飞书域名（默认 `https://open.feishu.cn`） |
| — | `FEISHU_ALLOW_FROM` | 允许使用的 `open_id` 列表（逗号分隔） |

**快速配置步骤**：
1. 在[飞书开放平台](https://open.feishu.cn/)创建企业自建应用
2. 添加"机器人"能力，开启"WebSocket 长连接"模式
3. 将 `app_id` 和 `app_secret` 填入 config.json
4. 启动 xbot，机器人自动上线（无需公网）

### QQ

**WebSocket 长连接模式 — 不需要公网 IP。** QQ 渠道通过 WebSocket 主动连接 QQ Bot 服务器，无需暴露端口。

| 字段 | 环境变量 | 说明 |
|------|---------|------|
| `enabled` | `QQ_ENABLED` | 设为 `true` 启用 |
| `app_id` | `QQ_APP_ID` | QQ Bot App ID |
| `client_secret` | `QQ_CLIENT_SECRET` | QQ Bot Client Secret |
| — | `QQ_ALLOW_FROM` | 允许使用的 `openid` 列表（逗号分隔） |

### NapCat (OneBot 11)

兼容 [NapCat](https://github.com/NapNeko/NapCatQQ) 和其他 OneBot 11 实现。xbot 主动连接 NapCat 的 WebSocket 服务。

| 字段 | 环境变量 | 说明 |
|------|---------|------|
| `enabled` | `NAPCAT_ENABLED` | 设为 `true` 启用 |
| `ws_url` | `NAPCAT_WS_URL` | NapCat WebSocket 地址（如 `ws://127.0.0.1:3001`） |
| `token` | `NAPCAT_TOKEN` | 认证 Token |
| — | `NAPCAT_ALLOW_FROM` | 允许使用的 QQ 号列表（逗号分隔） |

### Web

浏览器聊天界面，支持可选登录、邀请制、用户隔离。React 19 + Vite + TailwindCSS 4 前端。

| 字段 | 环境变量 | 说明 |
|------|---------|------|
| `enable` | `WEB_ENABLED` | 设为 `true` 启用 |
| `host` | `WEB_HOST` | 绑定地址（默认 `0.0.0.0`） |
| `port` | `WEB_PORT` | 端口（默认 `8082`） |
| — | `WEB_STATIC_DIR` | 前端静态文件目录 |
| — | `WEB_UPLOAD_DIR` | 文件上传目录 |
| — | `WEB_PERSONA_ISOLATION` | 每用户 Persona 隔离 |
| — | `WEB_INVITE_ONLY` | 仅邀请模式 |

## Features

### Tools

Built-in tools the agent can call during a conversation:

- **Shell** — Execute commands in sandbox (Docker / remote / none)
- **File I/O** — Read, write, Glob, Grep with workspace isolation
- **Web** — Fetch pages, Tavily web search
- **Context** — Edit conversation context mid-session
- **SubAgent** — Delegate tasks to specialized sub-agents
- **Cron** — Schedule tasks (cron expressions, one-shot `at`)
- **Download** — Download files from URLs
- **Feishu MCP** — Feishu API tools (doc, wiki, bitable, drive)
- **Runner** — Manage sandbox runner connections

### Memory

Two pluggable providers:

| | Flat (default) | Letta (MemGPT) |
|--|----------------|----------------|
| Core | In-memory blocks | SQLite (always in prompt) |
| Archival | Grep-searchable blob | Vector search (chromem-go) |
| Recall | Event history | FTS5 full-text search |
| Dependencies | None | Embedding model required |

Set via `MEMORY_PROVIDER=flat` or `MEMORY_PROVIDER=letta`. Letta also requires embedding config (`LLM_EMBEDDING_PROVIDER`, `LLM_EMBEDDING_MODEL`, etc.).

### Skills & Agents

- **Skills** — Markdown-defined capability packages loaded from `~/.xbot/skills/`. Two built-in: `skill-creator`, `agent-creator`.
- **SubAgents** — Delegate tasks to role-based sub-agents (e.g. `explore`, `code-reviewer`). Custom roles in `~/.xbot/agents/`. Max nesting depth: 6 (`MAX_SUBAGENT_DEPTH`).

### MCP Protocol

- **Global**: `.xbot/mcp.json` for always-on servers
- **Session**: Dynamic loading at runtime via `ManageTools` tool
- Supports stdio and HTTP transports, inactivity timeout, lazy cleanup

### Other

- **Multi-tenant** — Channel + chatID isolation
- **Hot-reload prompts** — Go templates with channel-specific overrides
- **KV-Cache optimized** — Context ordering maximizes LLM cache hits
- **OAuth 2.0** — Built-in OAuth server for web channel authentication

### Plugin System

可扩展的插件架构，支持脚本（Python/Node/Shell）、gRPC 进程、WASM 三种运行时。通过配置启用：

```json
{
  "plugins": {
    "enabled": true,
    "dirs": ["~/.xbot/plugins/"]
  }
}
```

- **Hook 集成** — `PreToolUse` / `PostToolUse` 生命周期拦截
- **Widget 系统** — 在 TUI/Web 界面注入实时信息面板（如 git branch、TODO 列表）
- **Context Enricher** — 动态注入上下文信息到 system prompt
- **自定义工具** — 插件注册新工具供 Agent 调用
- **依赖管理** — 自动解析插件间依赖，支持循环检测
- **Marketplace** — 本地插件搜索与安装

### Event Triggers

Webhook 触发器，让外部事件（GitHub PR、GitLab MR 等）自动触发 Agent 处理：

- 支持 HMAC-SHA256 签名验证
- Go template 渲染事件 payload
- 一次性触发器（`one_shot`）自动禁用
- 飞书/CLI 实时通知触发结果

## Architecture

```
┌──────────┐     ┌──────────────┐     ┌────────┐     ┌──────────┐
│  Feishu  │────▶│  Dispatcher  │────▶│ Agent  │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│ (agent/)│◀────│ (llm/)   │
│  NapCat  │     └──────────────┘     │        │     └──────────┘
│  Web     │                          │        │
│  CLI     │                          │        │────▶ Tools
└──────────┘                          │        │      (tools/)
                                      │        │
                                      │        │────▶ Memory
                                      │        │      (memory/)
                                      └────────┘
```

| Package | Role |
|---------|------|
| `bus/` | Inbound/outbound message channels |
| `channel/` | Channel adapters and message dispatcher |
| `agent/` | Agent loop (LLM → tools → response) |
| `llm/` | LLM clients (OpenAI, Anthropic) |
| `tools/` | Tool registry and implementations |
| `memory/` | Memory providers (flat / letta) |
| `config/` | Environment-based configuration |
| `storage/` | SQLite persistence (sessions, memory, tenants) |
| `session/` | Multi-tenant session management |
| `cron/` | Scheduled task execution |
| `oauth/` | OAuth 2.0 framework |
| `crypto/` | AES-256-GCM encryption for API keys |
| `logger/` | Structured logging with rotation |
| `web/` | React 19 + Vite + TailwindCSS 4 frontend |
| `agents/` | Embedded agent role definitions |
| `cmd/` | Entrypoints (`xbot-cli`, sandbox runner) |
| `prompt/` | Default system prompt template |

## Configuration

CLI 版配置存储在 `~/.xbot/config.json`，首次启动自动进入配置向导。所有配置字段均支持环境变量覆盖（优先级最高）。

### LLM

**config.json 字段**:
```json
{
  "llm": {
    "provider": "openai",          // "openai" 或 "anthropic"
    "api_key": "sk-xxx",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-4o",
    "retry_attempts": 5,
    "retry_delay": "1s",
    "retry_max_delay": "30s",
    "retry_timeout": "120s"
  }
}
```

**环境变量覆盖**:

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `LLM_PROVIDER` | `openai` | `openai` 或 `anthropic` |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | API 端点 |
| `LLM_API_KEY` | — | API Key |
| `LLM_MODEL` | `gpt-4o` | 模型名 |
| `LLM_RETRY_ATTEMPTS` | `5` | 失败重试次数 |
| `LLM_RETRY_DELAY` | `1s` | 初始退避 |
| `LLM_RETRY_MAX_DELAY` | `30s` | 最大退避 |
| `LLM_RETRY_TIMEOUT` | `120s` | 单次超时 |

### Agent

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_MAX_ITERATIONS` | `2000` | Max tool-call iterations per turn |
| `AGENT_MAX_CONCURRENCY` | `3` | Max concurrent LLM calls |
| `AGENT_MAX_CONTEXT_TOKENS` | `200000` | Max context window tokens |
| `AGENT_ENABLE_AUTO_COMPRESS` | `true` | Auto context compression |
| `AGENT_COMPRESSION_THRESHOLD` | `0.7` | Token ratio to trigger compression |
| `AGENT_CONTEXT_MODE` | — | Custom context management mode |
| `AGENT_PURGE_OLD_MESSAGES` | `false` | Purge old messages after compression |
| `MAX_SUBAGENT_DEPTH` | `6` | SubAgent max nesting depth |

### Sandbox

| Variable | Default | Description |
|----------|---------|-------------|
| `SANDBOX_MODE` | `docker` | `docker` / `remote` / `none` |
| `SANDBOX_DOCKER_IMAGE` | `ubuntu:22.04` | Docker image for sandbox |
| `SANDBOX_IDLE_TIMEOUT_MINUTES` | `30` | Idle timeout (0 = disabled) |
| `SANDBOX_WS_PORT` | `8080` | Remote sandbox WebSocket port |
| `SANDBOX_AUTH_TOKEN` | — | Runner authentication token |
| `SANDBOX_PUBLIC_URL` | — | Public URL for runner connections |

### Infrastructure

| Variable | Default | Description |
|----------|---------|-------------|
| `WORK_DIR` | `.` | Working directory |
| `PROMPT_FILE` | `prompt.md` | Custom prompt template |
| `SINGLE_USER` | `false` | Single-user mode |
| `XBOT_ENCRYPTION_KEY` | — | AES-256-GCM key (base64, 32 bytes) |
| `TAVILY_API_KEY` | — | Tavily web search API key |
| `OAUTH_ENABLE` | `false` | Enable OAuth server |
| `OAUTH_HOST` | `127.0.0.1` | OAuth bind address |
| `OAUTH_PORT` | `8081` | OAuth port |
| `OAUTH_BASE_URL` | — | OAuth callback base URL |
| `SERVER_HOST` | `0.0.0.0` | HTTP server bind address |
| `SERVER_PORT` | `8080` | HTTP server port |
| `LOG_LEVEL` | `info` | Log level |
| `LOG_FORMAT` | `json` | Log format |
| `PPROF_ENABLE` | `false` | Enable pprof endpoint |

## Deployment

### Docker

```bash
docker run -d --name xbot --restart unless-stopped \
  --security-opt seccomp=unconfined --cap-add SYS_ADMIN \
  -v /opt/xbot/.xbot:/data/.xbot \
  -e WORK_DIR=/data \
  -e LLM_PROVIDER=openai \
  -e LLM_BASE_URL=https://api.openai.com/v1 \
  -e LLM_API_KEY=your_key \
  -e LLM_MODEL=gpt-4o-mini \
  xbot:latest
```

### Makefile

```bash
make dev    # Development mode
make build  # Build binary
make run    # Build and run
make test   # Test with race detection
make fmt    # Format code
make lint   # golangci-lint
make ci     # lint → build → test
make clean  # Remove build artifacts
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md) — Detailed system design
- [CLI Channel](docs/cli-channel.md) — Full TUI documentation
- [Feishu Channel](docs/feishu-channel.md) — 飞书渠道配置与使用指南
- [Sandbox Docker](docs/sandbox_docker.md) — Docker sandbox setup
- [Web Channel](docs/web-channel-design.md) — Web channel design
- [CHANGELOG](CHANGELOG.md) — Release history

## License

MIT
