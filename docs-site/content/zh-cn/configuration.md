---
title: "配置参考"
weight: 15
---

# 配置参考

所有配置通过 `~/.xbot/config.json` 文件管理。**不推荐使用环境变量**，请直接编辑配置文件。

## 配置文件位置

- **默认位置**：`~/.xbot/config.json`
- 可通过环境变量 `XBOT_HOME` 覆盖（如 `XBOT_HOME=/opt/xbot`）
- Server 模式下通过 `xbot-cli serve --config /path/to/config.json` 指定

## 最小配置示例

### Standalone 模式（个人使用）

```json
{
  "subscriptions": [
    {
      "name": "default",
      "provider": "openai",
      "api_key": "sk-xxx",
      "model": "gpt-4o"
    }
  ],
  "sandbox": {
    "mode": "none"
  }
}
```

### Server 模式 + 飞书（团队使用）

管理员在 TUI 中通过 `/setup` 创建订阅，配置飞书应用：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  },
  "web": {
    "enable": true
  }
}
```

## 完整配置参考

### LLM 订阅配置

xbot 使用**订阅（Subscription）系统**管理 LLM 配置，不再使用全局单一 `llm` 字段。你可以创建多个订阅，在不同场景中切换。

```json
{
  "subscriptions": [
    {
      "name": "default",
      "provider": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4o",
      "max_output_tokens": 0,
      "max_context": 0,
      "thinking_mode": "",
      "active": true,
      "per_model_configs": {}
    }
  ]
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `name` | string | `"default"` | 订阅名称（用于切换和显示） |
| `provider` | string | `"openai"` | LLM 提供商：`openai` 或 `anthropic` |
| `api_key` | string | `""` | API Key |
| `base_url` | string | `"https://api.openai.com/v1"` | API 地址（兼容服务时修改） |
| `model` | string | `"gpt-4o"` | 默认模型 |
| `max_output_tokens` | int | `0`（= 32768） | 最大输出 token 数 |
| `max_context` | int | `0`（=200000） | 最大上下文 token 数（0 使用默认值） |
| `thinking_mode` | string | `""`（=auto） | 思考模式：`auto` / `enabled` / `disabled` |
| `active` | bool | `true` | 是否为当前激活的订阅 |
| `per_model_configs` | object | `{}` | 按模型覆盖 token 配置（见下方说明） |

**per_model_configs**：按模型覆盖 `max_output_tokens` 和 `max_context`，优先级高于订阅级默认值。

```json
"per_model_configs": {
  "gpt-4o": {"max_output_tokens": 16384, "max_context": 128000},
  "deepseek-chat": {"max_context": 64000}
}
```

**使用 DeepSeek 等兼容 API：**

```json
{
  "subscriptions": [
    {
      "name": "DeepSeek",
      "provider": "openai",
      "api_key": "your-key",
      "base_url": "https://api.deepseek.com/v1",
      "model": "deepseek-chat"
    }
  ]
}
```

**多订阅示例**（在 TUI 中通过 `/model` 切换）：

```json
{
  "subscriptions": [
    {
      "name": "GPT-4o",
      "provider": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4o",
      "active": true
    },
    {
      "name": "Claude",
      "provider": "anthropic",
      "api_key": "sk-ant-xxx",
      "model": "claude-sonnet-4-20250514",
      "active": false
    }
  ]
}
```

> ⚠️ **Server 模式**：`user_llm_subscriptions` 表中的订阅为单一真相来源，管理员通过 TUI `/setup` 创建后全团队共享。`user_settings` 表不应包含订阅字段（provider, model, api_key 等）。

### Model Tier 设置（用户级）

Model Tier 是**用户级设置**，存储在 `user_settings` 表（Server 模式）或 `config.json` 的全局 `llm` 字段（CLI 模式），不在 Subscription 中。通过 TUI `/settings` 面板配置。

| 字段 | 类型 | 说明 |
|------|------|------|
| `vanguard_model` | string | Vanguard 级别模型（最强推理，SubAgent 用） |
| `balance_model` | string | Balance 级别模型（均衡，SubAgent 用） |
| `swift_model` | string | Swift 级别模型（快速，SubAgent 用） |

未配置的层自动回退：vanguard → balance → swift。

**CLI 模式 config.json 示例：**

```json
{
  "llm": {
    "vanguard_model": "claude-opus-4",
    "balance_model": "claude-sonnet-4",
    "swift_model": "claude-haiku-4"
  }
}
```

**Server 模式**：通过 `/settings` → LLM Settings 配置，存储在 `user_settings` 表中。

### Agent 配置

```json
{
  "agent": {
    "max_iterations": 2000,
    "max_concurrency": 100,
    "memory_provider": "flat",
    "work_dir": ".",
    "prompt_file": "prompt.md",
    "max_context_tokens": 200000,
    "enable_auto_compress": true,
    "compression_threshold": 0.9,
    "context_mode": "",
    "purge_old_messages": false,
    "max_sub_agent_depth": 6,
    "llm_retry_attempts": 5,
    "llm_retry_delay": "1s",
    "llm_retry_max_delay": "30s",
    "llm_retry_timeout": "120s",
    "mcp_inactivity_timeout": "30m",
    "mcp_cleanup_interval": "5m",
    "session_cache_timeout": "24h"
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `max_iterations` | int | `2000` | 单次对话最大工具调用次数 |
| `max_concurrency` | int | `100` | 最大并发 LLM 调用数 |
| `memory_provider` | string | `"flat"` | 记忆系统：`flat` 或 `letta` |
| `work_dir` | string | `"."` | 工作目录 |
| `prompt_file` | string | `"prompt.md"` | 自定义系统 prompt 文件 |
| `max_context_tokens` | int | `200000` | 最大上下文窗口 token |
| `enable_auto_compress` | bool | `true` | 自动压缩上下文 |
| `compression_threshold` | float | `0.9` | 触发压缩的 token 比例 |
| `context_mode` | string | `""` | 上下文管理模式 |
| `purge_old_messages` | bool | `false` | 压缩后清除旧消息 |
| `max_sub_agent_depth` | int | `6` | SubAgent 最大嵌套深度 |
| `llm_retry_attempts` | int | `5` | LLM 调用失败重试次数 |
| `llm_retry_delay` | duration | `"1s"` | 重试初始延迟 |
| `llm_retry_max_delay` | duration | `"30s"` | 重试最大延迟 |
| `llm_retry_timeout` | duration | `"120s"` | 单次 LLM 调用超时 |
| `mcp_inactivity_timeout` | duration | `"30m"` | MCP Server 非活动超时 |
| `mcp_cleanup_interval` | duration | `"5m"` | MCP 清理间隔 |
| `session_cache_timeout` | duration | `"24h"` | Session 缓存超时 |

### 沙箱配置

```json
{
  "sandbox": {
    "mode": "docker",
    "docker_image": "ubuntu:22.04",
    "host_work_dir": "",
    "idle_timeout": "30m",
    "ws_port": 8080,
    "auth_token": "",
    "public_url": ""
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `mode` | string | `"docker"` | 沙箱模式：`none` / `docker` |
| `docker_image` | string | `"ubuntu:22.04"` | Docker 镜像 |
| `host_work_dir` | string | `""` | 宿主机工作目录 |
| `idle_timeout` | duration | `"30m"` | 空闲超时（0 = 禁用） |
| `ws_port` | int | `8080` | 远程沙箱 WebSocket 端口 |
| `auth_token` | string | `""` | Runner 认证 Token |
| `public_url` | string | `""` | Runner 连接的公共 URL |

### 渠道配置

详见各渠道文档：

- [飞书](/zh-cn/channels/feishu/)
- [QQ](/zh-cn/channels/qq/)
- [NapCat](/zh-cn/channels/napcat/)
- [Web](/zh-cn/channels/web/)
- [CLI](/zh-cn/channels/cli/)

### Server 配置

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8082,
    "read_timeout": "30s",
    "write_timeout": "120s"
  }
}
```

### CLI 配置（Remote 模式）

```json
{
  "cli": {
    "server_url": "ws://127.0.0.1:8082",
    "token": "your-admin-token"
  }
}
```

Server 模式安装时自动配置，一般不需要手动修改。

### 管理员配置

```json
{
  "admin": {
    "token": "random-generated-token",
    "chat_id": ""
  }
}
```

| 字段 | 说明 |
|------|------|
| `token` | 管理员 Token（安装时自动生成） |
| `chat_id` | 管理员 chat ID（用于接收启动通知） |

### Embedding 配置（Letta 记忆模式需要）

```json
{
  "embedding": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "api_key": "",
    "model": "text-embedding-3-small",
    "max_tokens": 2048
  }
}
```

### 日志配置

```json
{
  "log": {
    "level": "info",
    "format": "json"
  }
}
```

### 其他配置

```json
{
  "tavily_api_key": "",
  "oauth": {
    "enable": false,
    "host": "127.0.0.1",
    "port": 8081,
    "base_url": ""
  },
  "pprof": {
    "enable": false,
    "host": "localhost",
    "port": 6060
  }
}
```

| 字段 | 说明 |
|------|------|
| `tavily_api_key` | Tavily 网页搜索 API Key（配置后 Agent 可搜索网页） |
| `oauth` | OAuth 2.0 服务配置（Web 渠道认证用） |
| `pprof` | 性能分析端点（开发调试用） |

### 多 LLM 订阅（CLI 模式）

CLI 模式支持在 `config.json` 中配置多个 LLM 订阅，通过 `/model` 或 `Ctrl+K → Model` 实时切换。

订阅的完整字段见上方「LLM 订阅配置」节。

> **Server 模式**：管理员在 TUI 中通过 `/setup` 创建订阅后存储在 `user_llm_subscriptions` 表，全团队共享。团队成员无需在 `config.json` 中配置。

## AI-Native 配置

xbot 的 Agent 可通过内置工具自行调整配置，无需用户手动编辑文件：

| 工具 | 能力 | 示例 |
|------|------|------|
| `config` | 读写 xbot 配置（主题、布局、订阅、沙箱等） | 「帮我把主题换成 dracula」→ Agent 调用 `config set theme dracula` |
| `tui_control` | 操作 TUI 界面（切换会话、调整侧边栏、切换主题等） | 「侧边栏收窄一点」→ Agent 调用 `tui_control set_layout sidebar_width 25` |

`config` 工具读取时会自动屏蔽敏感字段（`api_key` 显示 `sk-a***`），但不阻止写入。

## Hooks 配置

通过 `~/.xbot/hooks.json` 或项目级 `<project>/.xbot/hooks.json` 配置生命周期钩子。

### 配置层级

三层合并：`~/.xbot/hooks.json` → `<project>/.xbot/hooks.json` → `<project>/.xbot/hooks.local.json`，后者覆盖前者。项目级可提交 git。

### 基本结构

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [{ "type": "command", "command": ".xbot/hooks/lint.sh" }]
      }
    ]
  }
}
```

### 关键参数

| 参数 | 说明 |
|------|------|
| `enable_command_hooks` | **必须设为 `true`**，command 类型默认禁用 |
| `hooks.<事件名>` | 17 种事件之一 |
| `matcher` | 工具名匹配，`""` 表示匹配所有 |
| `hooks[].type` | `command` / `http` / `mcp_tool` / `callback` |
| `hooks[].command` | Shell 命令，stdin 接收事件 JSON |
| `hooks[].if` | 细粒度过滤，如 `"Shell(*git commit*)"` |
| `hooks[].async` | 异步执行，不阻塞 agent |
| `hooks[].timeout` | 超时秒数（默认 30） |

详见 [Hooks 系统设计](/design/hooks-system/)。

## 参见
- [渠道](/zh-cn/channels/) — 各渠道配置
- [沙箱指南](/zh-cn/guides/sandbox/) — Docker 沙箱
- [CLI 参考](/zh-cn/cli-reference/) — 键盘快捷键和命令
