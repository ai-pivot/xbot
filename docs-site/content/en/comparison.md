---
title: "Comparison"
weight: 65
---

# xbot vs Other AI Agents

You've heard of Codex, Claude Code, OpenCode, and Gemini CLI. They're all
excellent **terminal-only** AI coding agents. **xbot takes a fundamentally
different approach**: one agent, every channel.

{{< hint type=important >}}
The key insight: most AI agents live in a single terminal window. xbot
breaks out of the terminal and reaches your whole team through **Feishu,
QQ, Web, and CLI** — with shared credentials and always-on availability.
{{< /hint >}}

## At a glance

| Feature | **xbot** | Codex | Claude Code | OpenCode |
|---------|----------|-------|-------------|----------|
| **Primary channels** | Feishu · QQ · Web · CLI | Terminal | Terminal | Terminal |
| **Self-hosted server** | ✅ Always-on | ❌ | ❌ | ❌ |
| **Team LLM sharing** | ✅ Admin configures once | ❌ Each user | ❌ Each user | ❌ Each user |
| **Feishu integration** | ✅ Docs, Bitable, Drive, Cards | ❌ | ❌ | ❌ |
| **Web chat UI** | ✅ Built-in | ❌ | ❌ | ✅ |
| **Full-featured TUI** | ✅ Mouse, themes, palette | ✅ | ✅ | ✅ |
| **SubAgents** | ✅ + Group Chat debate | ✅ | ✅ | ✅ |
| **MCP protocol** | ✅ | ✅ | ✅ | ✅ |
| **Plugin system** | ✅ Tools, hooks, widgets | ❌ | ❌ | ✅ |
| **AI self-configuration** | ✅ `config` + `tui_control` | ❌ | ❌ | ❌ |
| **Multi-session sidebar** | ✅ | ✅ | ✅ | ✅ |
| **Scheduled tasks (Cron)** | ✅ | ❌ | ❌ | ❌ |
| **Skills (capability packs)** | ✅ | ❌ | ❌ | ✅ |
| **License** | MIT | Apache 2.0 | Proprietary | MIT |

## Where xbot wins

### 📱 Multi-channel access

This is xbot's killer feature. The same agent instance is reachable from:

- **Feishu** — @mention the bot in any group chat
- **QQ / NapCat** — chat via QQ windows
- **Web** — a full browser chat UI with registration, login, invite-only mode
- **CLI** — the full-featured TUI

Other agents require each user to install and run a local CLI. With xbot,
deploy once and your whole team connects through tools they already use.

### 🔑 Team-shared LLM subscriptions

In Codex / Claude Code / OpenCode, every team member brings their own API
key. In xbot Server mode, the **admin configures one or more subscriptions**
once, and the entire team uses them immediately — no individual key
management.

You can create multiple subscriptions (work Claude, personal DeepSeek) and
switch per session with `Ctrl+N` (LLM panel).

### 🏢 Feishu deep integration

xbot includes a native Feishu tool set: read/write Feishu Docs, manipulate
Bitable (multidimensional tables), manage Drive files, send interactive
message cards, search the Wiki. No other AI agent offers this.

### 🤖 Group Chat meeting mode

xbot's `CreateChat` + `SendMessage` tools support **multi-agent moderated
discussions** (Meeting Mode). Multiple SubAgents debate an architecture
decision, with `@mentions` triggering specific agents to speak. Other
agents support one-to-one sub-agent delegation only.

### ⏰ Always-on with scheduled tasks

Server mode runs 24/7 as a system service. The built-in `Cron` tool lets the
agent schedule tasks — "remind me to review the PR at 9 AM" or "run the
nightly test suite." Other terminal agents stop when you close the terminal.

### 🎛️ AI-Native configuration

The agent can adjust its own settings and UI via the `config` and
`tui_control` tools. Say "switch to dark theme" or "set max concurrency to 5"
and the agent handles it. No other agent offers this.

## Where others win

Being honest about trade-offs:

| Area | Advantage of others |
|------|---------------------|
| **IDE integration** | Codex has a VS Code extension; xbot is TUI/Web/chat only |
| **Local code editing** | Claude Code / Codex are deeply optimized for in-repo code editing workflows |
| **Git-native** | These agents are built around git diff/commit workflows; xbot is more general-purpose |
| **Ecosystem maturity** | Codex/Claude Code have larger communities and more integrations |

## When to choose xbot

- ✅ **You need team-wide access** beyond the terminal (Feishu, Web, QQ)
- ✅ **You want shared LLM credentials** without each user managing keys
- ✅ **You work in the Feishu ecosystem** and need Doc/Bitable automation
- ✅ **You want an always-on agent** that survives terminal closure
- ✅ **You need scheduled/automated tasks** via the agent

## When another tool may be better

- ❌ You want IDE-integrated (VS Code) code editing → try Codex
- ❌ You only need a solo terminal coding assistant → Claude Code / OpenCode
- ❌ You need deep git-workflow automation → Codex / Claude Code

{{< hint type=note >}}
**Best of both worlds:** xbot doesn't replace your terminal coding agent — it
complements it. Use Codex/Claude Code for in-IDE editing, and xbot for
team-accessible Feishu/QQ/Web automation, scheduled tasks, and shared AI
workflows.
{{< /hint >}}

## See also
- [Getting Started](/getting-started/) — 5-minute quick start
- [Use Cases](/use-cases/) — real-world scenarios
- [Tips & Tricks](/tips/) — power-user tips
