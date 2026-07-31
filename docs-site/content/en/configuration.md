---
title: "Configuration"
weight: 15
---

# Configuration Reference

All configuration lives in `~/.xbot/config.json`. **Direct editing is
preferred over environment variables.**

## Quick reference

| What you want to do | Config key |
|---------------------|------------|
| Set API key | `subscriptions[].api_key` |
| Use DeepSeek/Ollama | `subscriptions[].base_url` |
| Enable Feishu | `feishu.enabled: true` |
| Enable Web | `web.enable: true` |
| Docker sandbox | `sandbox.mode: "docker"` |
| Restrict users | `*.allow_from: [...]` |
| Max concurrent calls | `agent.max_concurrency` |
| Context compression | `agent.compression_threshold` |

## Config file location

- **Default:** `~/.xbot/config.json`
- Override with the `XBOT_HOME` environment variable (e.g.
  `XBOT_HOME=/opt/xbot`)
- In Server mode, specify via `xbot-cli serve --config /path/to/config.json`

## Minimal config

### Standalone (personal use)

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

### Server mode + Feishu (team use)

The admin creates subscriptions via `/setup` in the TUI, then enables the
Feishu app:

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  },
  "web": {
    "enabled": true
  }
}
```

{{< hint type=warning >}}
**Channel config keys:** Feishu, QQ, and NapCat use `enabled`. Web uses
`enable` (note: no `d`). Both `enable` and `enabled` are accepted at runtime
for backward compatibility, but match the struct tag for clarity.
{{< /hint >}}

---

## LLM subscriptions

xbot manages LLM configuration through a **subscription system** (not a single
global `llm` key). You can create multiple subscriptions and switch between
them per session.

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

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"default"` | Subscription name (shown in switcher) |
| `provider` | string | `"openai"` | Provider: `openai` or `anthropic` |
| `api_key` | string | `""` | API key |
| `base_url` | string | `"https://api.openai.com/v1"` | API base URL (change for compatible services) |
| `model` | string | `"gpt-4o"` | Default model |
| `max_output_tokens` | int | `0` (= 32768) | Max output tokens |
| `max_context` | int | `0` (= 200000) | Max context tokens (0 = use default) |
| `thinking_mode` | string | `""` (= auto) | Thinking mode: `auto` / `enabled` / `disabled` |
| `active` | bool | `true` | Whether this is the active subscription |
| `per_model_configs` | object | `{}` | Per-model token overrides (see below) |

**`per_model_configs`** overrides `max_output_tokens` and `max_context` per
model, taking priority over the subscription-level defaults:

```json
"per_model_configs": {
  "gpt-4o": {"max_output_tokens": 16384, "max_context": 128000},
  "deepseek-chat": {"max_context": 64000}
}
```

### Multiple subscriptions

Switch between them in the TUI via `Ctrl+N` or `/models`:

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

### Compatible APIs (DeepSeek, Qwen, Ollama, etc.)

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

{{< hint type=note >}}
**Server mode:** the `user_llm_subscriptions` table is the single source of
truth. The admin creates subscriptions via TUI `/setup`, then the whole team
shares them. The `user_settings` table must NOT contain subscription fields
(provider, model, api_key, etc.).
{{< /hint >}}

## Model tiers (user-level)

Model tiers are **user-level settings**, stored in `user_settings` (Server
mode) or the global `llm` block (CLI mode) — *not* inside a subscription.
Configure via the `/settings` panel.

| Field | Description |
|-------|-------------|
| `vanguard_model` | Strongest reasoning (used by SubAgents) |
| `balance_model` | Balanced (used by SubAgents) |
| `swift_model` | Fast/small (used by SubAgents) |

Unconfigured tiers fall back automatically: vanguard → balance → swift.

```json
{
  "llm": {
    "vanguard_model": "claude-opus-4",
    "balance_model": "claude-sonnet-4",
    "swift_model": "claude-haiku-4"
  }
}
```

## Agent configuration

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

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_iterations` | int | `2000` | Max tool calls per conversation turn |
| `max_concurrency` | int | `100` | Max concurrent LLM calls |
| `memory_provider` | string | `"flat"` | Memory system: `flat` or `letta` |
| `work_dir` | string | `"."` | Working directory |
| `prompt_file` | string | `"prompt.md"` | Custom system prompt file |
| `max_context_tokens` | int | `200000` | Max context window (tokens) |
| `model_contexts` | object | `{}` | Per-model context overrides (model → tokens) |
| `enable_auto_compress` | bool | `true` | Auto-compress context when full |
| `compression_threshold` | float | `0.9` | Token ratio that triggers compression |
| `context_mode` | string | `""` | Context management mode |
| `purge_old_messages` | bool | `false` | Purge old messages after compression |
| `max_sub_agent_depth` | int | `6` | Max SubAgent nesting depth |
| `llm_retry_attempts` | int | `5` | LLM call retry count |
| `llm_retry_delay` | duration | `"1s"` | Initial retry delay |
| `llm_retry_max_delay` | duration | `"30s"` | Max retry delay |
| `llm_retry_timeout` | duration | `"120s"` | Per-call LLM timeout |
| `mcp_inactivity_timeout` | duration | `"30m"` | MCP server inactivity timeout |
| `mcp_cleanup_interval` | duration | `"5m"` | MCP cleanup interval |
| `session_cache_timeout` | duration | `"24h"` | Session cache timeout |

{{< hint type=note >}}
Duration values are human-readable strings: `"30m"`, `"1h30m"`, `"5s"`.
For backward compatibility, legacy nanosecond numbers are also accepted.
{{< /hint >}}

### Experimental features

```json
{
  "agent": {
    "experimental": {
      "auto_worktree": false
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `auto_worktree` | `false` | Auto-create git worktrees when multiple agents share a repo |

## Sandbox configuration

```json
{
  "sandbox": {
    "mode": "none",
    "docker_image": "ubuntu:22.04",
    "host_work_dir": "",
    "idle_timeout": "30m",
    "ws_port": 8080,
    "auth_token": "",
    "public_url": ""
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mode` | string | `"none"` | Sandbox mode: `none` or `docker` |
| `remote_mode` | string | `""` | Remote sandbox mode |
| `docker_image` | string | `"ubuntu:22.04"` | Docker image |
| `host_work_dir` | string | `""` | Host working directory |
| `idle_timeout` | duration | `"30m"` | Idle timeout (0 = disabled) |
| `ws_port` | int | `8080` | Remote sandbox WebSocket port |
| `auth_token` | string | `""` | Runner auth token |
| `public_url` | string | `""` | Public URL the runner connects to |

See the [Sandbox guide](/guides/sandbox/) for Docker setup details.

## Channel configuration

See the per-channel docs:

- [Feishu](/channels/feishu/)
- [CLI](/channels/cli/)
- [Web](/channels/web/)
- [QQ](/channels/qq/)
- [NapCat](/channels/napcat/)

## Server configuration

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

## CLI configuration (Remote mode)

```json
{
  "cli": {
    "server_url": "ws://127.0.0.1:8082",
    "token": "your-admin-token"
  }
}
```

Auto-configured during Server-mode installation; usually no manual editing
needed.

| Field | Description |
|-------|-------------|
| `server_url` | WebSocket URL of the remote agent server |
| `token` | Auth token (matches the server's `admin.token`) |

## Admin configuration

```json
{
  "admin": {
    "token": "random-generated-token",
    "chat_id": ""
  }
}
```

| Field | Description |
|-------|-------------|
| `token` | Admin token (auto-generated at install) |
| `chat_id` | Admin chat ID (for startup notifications) |

## Embedding configuration (Letta memory)

Required only when using `memory_provider: "letta"`:

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

## Plugin configuration

```json
{
  "plugins": {
    "enabled": false,
    "dirs": [],
    "disabled_plugins": [],
    "allow_unverified": false
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the plugin system (opt-in) |
| `dirs` | []string | `[]` | Extra plugin scan directories (default: `~/.xbot/plugins/`) |
| `disabled_plugins` | []string | `[]` | Plugin IDs to skip |
| `allow_unverified` | bool | `false` | Load plugins without verified manifests |

## Log configuration

```json
{
  "log": {
    "level": "info",
    "format": "text"
  }
}
```

## See also
- [Channels](/channels/) — per-channel configuration
- [Sandbox guide](/guides/sandbox/) — Docker sandboxing
- [CLI Reference](/cli-reference/) — keyboard shortcuts and commands
