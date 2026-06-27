---
title: "Getting Started"
weight: 5
---

# Getting Started

Get xbot running in under 5 minutes.

{{< hint type=note >}}
**System requirements:** Any modern OS (Linux, macOS, Windows). No pre-installed
dependencies needed — the installer downloads a static binary.
{{< /hint >}}

## 1. Install

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

The installer downloads the `xbot-cli` binary, generates a random admin
token, and writes `~/.xbot/config.json`. For Server mode it also installs a
system service and downloads the Web UI.

{{< hint type=note >}}
**Behind a firewall (China)?** Use the mirror-accelerated installer:
```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```
{{< /hint >}}

## 2. Choose a mode

The installer asks you to pick:

| | Standalone | Server |
|--|-----------|--------|
| Architecture | CLI runs the agent locally | Background server + CLI connects remotely |
| Best for | Solo developer | Teams, multi-channel |
| Channels | CLI only | Feishu · QQ · Web · CLI |
| LLM sharing | Each user configures | Admin configures once, everyone uses |
| Persistence | Stops when terminal closes | System service, auto-start |

> **Most teams should choose Server mode.** Pick Standalone for a quick solo
> test drive.

## 3. Configure your LLM

Run `xbot-cli`. The first launch opens a **Setup wizard**:

1. Choose an LLM provider (OpenAI / Anthropic / OpenAI-compatible)
2. Enter your API key
3. Set the API base URL (change this for DeepSeek, Qwen, Ollama, etc.)
4. Pick a model
5. Configure model tiers (Vanguard / Balance / Swift — used for different
   scenarios)

Re-run the wizard anytime with `/setup` or `Ctrl+K → Setup`.

> **Subscriptions, not a single global key.** xbot uses a *subscription*
> system. You can create multiple subscriptions (e.g. work Claude, personal
> DeepSeek) and switch between them per session.

{{< hint type=tip >}}
**Using DeepSeek, Qwen, or Ollama?** Set `provider: "openai"` and change the
`base_url` in the Setup wizard. xbot works with any OpenAI-compatible API.
{{< /hint >}}

## 4. Start chatting

You're ready. Type a message and press Enter. The agent can call tools, run
commands, search the web, and delegate to sub-agents.

### Try these right now

```text
You: What files are in this directory?

Agent: *uses Shell tool to run ls*
Agent: The current directory contains...

You: Read the README.md and summarize it

Agent: *uses Read tool*
Agent: This project is about...

You: Create a new file called notes.txt with today's date

Agent: *uses FileCreate tool*
Agent: Done! Created notes.txt with today's date.

You: Search the web for the latest Go release

Agent: *uses WebSearch tool*
Agent: The latest Go release is...
```

The agent picks the right tool for the job — you just describe what you want.

Type `/` to see all slash commands. A few essentials:

| Command | What it does |
|---------|-------------|
| `/setup` | Reconfigure LLM, sandbox, theme |
| `/context` | Inspect token usage |
| `/clear` | Clear the conversation |
| `/new` | Start a new session |
| `/sessions` | List / switch sessions |
| `/settings` | Open settings panels |
| `/help` | Show all commands |

### Essential keyboard shortcuts

| Shortcut | Action |
|----------|--------|
| `Ctrl+K` | Command palette (fuzzy search) |
| `Ctrl+N` | Cycle model |
| `Ctrl+P` | Manage subscriptions (add/disable/delete) |
| `Ctrl+T` | Sub-agent progress panel |
| `Ctrl+L` | Switch model — searchable picker (`订阅名 · 模型名`, per-session) |
| `Ctrl+N` | Cycle to next model |

## Where to go next

{{< columns >}}

- [Installation guide](/installation/) — build from source, service management
- [Configuration reference](/configuration/) — every `config.json` field
- [Channels](/channels/) — Feishu, QQ, Web setup

<--->

- [Features](/features/) — tools, skills, sub-agents, MCP, plugins
- [Sandbox guide](/guides/sandbox/) — Docker sandboxing
- [Architecture](/architecture/) — how it all fits together

{{< /columns >}}
