---
title: "xbot"
weight: 0
geekdocHidden: true
---

{{< columns >}}

**xbot** is a self-hosted AI agent framework. Deploy it once on your own
server, then talk to it through **Feishu, QQ, the terminal, or a web
browser** — it uses tools to get real work done.

![xbot CLI streaming output](/img/cli/streaming.gif)

<--->

{{< button href="/getting-started" >}}Quick Start{{< /button >}}
&nbsp;
{{< button href="/installation" >}}Installation{{< /button >}}

{{< /columns >}}

## Why xbot?

Most AI coding agents live in a single terminal. **xbot is different**: one
agent, every channel. Configure it once on your server, and your whole team
reaches the same agent through the tools they already use.

| | xbot | Codex / Claude Code / OpenCode |
|--|------|-------------------------------|
| **Multi-channel** | Feishu · QQ · Web · CLI | Terminal only |
| **Team shared LLM** | Admin configures once, everyone uses | Each user brings their own key |
| **Self-hosted** | Your data never leaves your server | ✅ (terminal) |
| **Full-featured TUI** | Mouse, themes, command palette, sessions | ✅ |
| **Feishu integration** | Docs, Bitable, Drive, cards | ❌ |
| **SubAgents + Group Chat** | Delegate, parallelize, debate | SubAgents only |
| **Plugin ecosystem** | Tools, hooks, widgets, channel plugins | Limited |

{{< hint type=important >}}
**The most common use case:** Deploy Server mode → connect a Feishu app →
your whole team @-mentions the bot in group chats. No one configures their own
API key.
{{< /hint >}}

## Core Features

- 🧠 **Multi-turn conversations + tool calling** — Shell, file I/O, web
  search, scheduled tasks, sub-agent delegation
- 📱 **Multi-channel access** — one agent, many entry points
- 🔑 **Team-shared LLM subscriptions** — admin configures once, everyone uses;
  switch between subscriptions anytime
- 🖱️ **Full-featured TUI** — mouse interaction, command palette (Ctrl+K),
  multi-session sidebar, theming
- 🏠 **Fully self-hosted** — your data stays on your server
- 🧩 **Extensible** — Skills, SubAgents, MCP protocol, Plugin system
- 🤖 **AI-Native configuration** — the agent can adjust its own settings and
  UI via the `config` and `tui_control` tools

## Which channel should I use?

| Channel | Best for | Highlights |
|---------|----------|------------|
| **CLI** | Developers, power users | Full TUI, streaming, tool calls, mouse support |
| **Feishu** | Team collaboration | @mention in group chats, interactive cards |
| **QQ / NapCat** | Individuals, small groups | Chat via QQ windows |
| **Web** | Anyone with a browser | Web chat, registration/login, invite-only mode |

## Quick Start

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

After installation, run `xbot-cli`. The first run launches a Setup wizard
that guides you through API key configuration.

See the [Getting Started guide](/getting-started/) or
[Installation guide](/installation/) for details.

## Architecture

```
┌──────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────┐
│  Feishu  │────▶│  Dispatcher  │────▶│  Backend    │────▶│   LLM    │
│  QQ      │◀────│  (channel/)  │◀────│  (RPC)      │◀────│ (llm/)   │
│  Web     │     └──────────────┘     │             │     └──────────┘
│  CLI     │                          │  Transport  │
└──────────┘                          │  (local/    │────▶ Tools
                                      │   remote)   │      (tools/)
                                      │             │
                                      │  Agent Loop │────▶ Memory
                                      │  (agent/)   │      (memory/)
                                      └────────────┘
```

Core design: **Backend** is a pure RPC client interface (zero business logic),
**Transport** is the execution layer (`localTransport` calls the Agent
directly, `remoteTransport` forwards over WebSocket).

Read the full [Architecture overview](/architecture/).

## Community

- 📖 [Documentation](/getting-started/) — full guides and references
- 🐛 [GitHub Issues](https://github.com/ai-pivot/xbot/issues) — report bugs or request features
- 💬 [GitHub Discussions](https://github.com/ai-pivot/xbot/discussions) — ask questions and share ideas
- 📦 [Releases](https://github.com/ai-pivot/xbot/releases) — download the latest version
- 📄 [Changelog](https://github.com/ai-pivot/xbot/blob/master/CHANGELOG.md) — what's new
- 🤝 [Contributing](/development/) — how to contribute
