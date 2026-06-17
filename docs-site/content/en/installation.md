---
title: "Installation"
weight: 10
---

# Installation

## One-line installer (recommended)

```bash
# Linux / macOS (amd64, arm64)
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

Pin a version or choose an install path:

```bash
VERSION=v0.0.48 curl -fsSL ... | bash            # specific version
INSTALL_PATH=~/.local/bin curl -fsSL ... | bash  # custom path
```

{{< hint type=note >}}
**Behind a firewall (China)?** Use the mirror-accelerated installer — it
auto-detects a working CDN mirror and proxies all GitHub downloads:
```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```
{{< /hint >}}

## Build from source

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build          # build xbot (server + runner)
make run            # build and run the server
```

Build only the CLI:

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

Requirements: **Go 1.26+**. The Web UI bundles are committed, so no Node.js
is needed to build the Go binaries.

## Two installation modes

The installer lets you choose **Standalone** or **Server**.

### Standalone (single machine)

The CLI runs the agent locally with no background service.

- ✅ Simple, install-and-go
- ✅ No background process
- ❌ Stops when you close the terminal
- ❌ CLI channel only — no Feishu / QQ / Web
- ❌ No team-shared LLM

**Best for:** solo developers who want a quick test drive.

### Server (team / multi-channel)

A background server process runs continuously. CLIs connect over WebSocket,
and Feishu / QQ / Web channels are enabled simultaneously.

- ✅ Agent runs 24/7, auto-starts on boot
- ✅ Feishu / QQ / Web channels all active
- ✅ Web browser chat UI
- ✅ Admin configures the LLM key once — the whole team uses it
- ✅ Multiple CLI clients connect at once

**Best for:** teams, anyone who needs Feishu / QQ / Web, or wants a Web UI.

{{< hint type=important >}}
**Most teams should choose Server mode.**
{{< /hint >}}

### Service management (Server mode)

The installer configures a user-level system service (no `sudo` needed):

| Platform | Service |
|----------|---------|
| Linux | `systemd --user` (user-level service) |
| macOS | `launchd` (LaunchAgent) |
| Windows | Startup folder / Task Scheduler / nssm |

Start the server with: `xbot-cli serve`

### What the installer does

1. Downloads `xbot-cli` to `~/.local/bin/` (or your custom path)
2. Generates a random admin token
3. Writes / updates `~/.xbot/config.json`
4. Server mode: installs a system service + downloads the Web UI

## First-run configuration

After installing, run:

```bash
xbot-cli
```

### Setup wizard

The first run auto-launches the **Setup wizard**, which guides you through:

**LLM subscription**
1. Choose a provider (OpenAI / Anthropic / OpenAI-compatible API)
2. Enter your API key (**required**)
3. Set the API base URL (default `https://api.openai.com/v1`; change for
   compatible services)
4. Choose a model
5. Configure model tiers (Vanguard / Balance / Swift)
6. Tavily search key (optional — enables web search)

**Environment**
- Sandbox mode (default `none`; Docker users choose `docker`)
- Memory provider (default `flat`)

**Appearance**
- Color scheme (9 built-in themes)

Re-run anytime via `/setup` or `Ctrl+K → Setup`.

### Minimal config (Standalone)

You can also edit `~/.xbot/config.json` directly. See
[Configuration reference](/configuration/) for all fields.

```json
{
  "subscriptions": [
    {
      "name": "default",
      "provider": "openai",
      "api_key": "sk-xxx",
      "model": "gpt-4o"
    }
  ]
}
```

### Using DeepSeek or other compatible APIs

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

## Verify the installation

```bash
xbot-cli --version

# Server mode — check service status
# Linux:
systemctl --user status xbot-server
# macOS:
launchctl list | grep xbot
```

{{< hint type=tip >}}
**Quick health check:** Run `xbot-cli` and type "hello". If the agent
responds, everything is working correctly.
{{< /hint >}}

## See also
- [Getting Started](/getting-started/) — 5-minute quick start
- [Configuration](/configuration/) — all config.json fields
- [Channels](/channels/) — Feishu, QQ, Web, CLI setup
