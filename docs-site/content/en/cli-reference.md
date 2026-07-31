---
title: "CLI Reference"
weight: 12
---

# CLI Reference

Complete reference for the xbot CLI terminal interface.

## Keyboard shortcuts

### Global

| Shortcut | Action |
|----------|--------|
| `Ctrl+K` | Open command palette (fuzzy search all commands) |
| `Ctrl+C` | Cancel current generation / interrupt |
| `Ctrl+N` | Open the unified LLM panel — switch model + manage subscriptions in one place (`订阅名 · 模型名`, cross-subscription, per-session, persists). In-panel: `↑↓` navigate, `Enter` select/toggle, `E` edit, `D` disable/delete, `N` add model, `S` show all noise models, `/` filter, `Esc` close) |
| `Ctrl+T` | Open Sessions panel |

### Input

| Shortcut | Action |
|----------|--------|
| `Enter` | Send message |
| `Shift+Enter` | New line |
| `↑` / `↓` | Navigate input history |
| `Tab` | Autocomplete |

### Navigation

| Shortcut | Action |
|----------|--------|
| `/` | Start typing a slash command |
| `Esc` | Close palette / cancel action |
| Mouse click | Click sidebar to switch sessions, scroll messages, click settings |

## Slash commands

Type `/` in the input box to see all commands. Here's the complete list:

### Setup & Config

| Command | Description |
|---------|-------------|
| `/setup` | Open the Setup wizard (LLM, sandbox, theme) |
| `/settings` | Open settings panels (appearance, sessions, LLM, etc.) |
| `/config` | View/edit config (alias for settings) |
| `/context` | Show current token usage and context bar |
| `/usage` | Show token usage details |

### Sessions

| Command | Description |
|---------|-------------|
| `/new` | Start a new session |
| `/sessions` | List and switch sessions |
| `/su` | Switch session (quick) |
| `/chat` | Chat management |
| `/ss` | Session status |
| `/rewind` | Rewind to a previous message in the conversation |

### Conversation

| Command | Description |
|---------|-------------|
| `/clear` | Clear the conversation history |
| `/compress` | Manually compress the context |
| `/cancel` | Cancel the current generation |
| `/search` | Search message history |

### System

| Command | Description |
|---------|-------------|
| `/help` | Show all available commands |
| `/commands` | List all commands |
| `/version` | Show version information |
| `/update` | Check for and install updates |
| `/debug` | Toggle debug mode |
| `/tasks` | View background tasks |
| `/channel` | Open channel configuration panel |
| `/plugin` | Manage plugins |
| `/palette` | Open command palette |
| `/user` | User management |

### Session lifecycle

| Command | Description |
|---------|-------------|
| `/exit` | Exit xbot-cli |
| `/quit` | Quit xbot-cli (alias) |

## Mouse support

The TUI supports full mouse interaction:

- **Sidebar:** click any session to switch, scroll to see all sessions
- **Messages:** scroll to navigate conversation history
- **Settings:** click settings items, use dropdowns, toggle switches
- **Palette:** click results to select
- **Progress panel:** click to expand/collapse sub-agent details

## Themes

Switch themes via `Ctrl+K → Theme` or `/palette theme`. xbot includes 9 built-in
themes and supports custom themes (JSON files in `~/.xbot/themes/`).

{{< hint type=tip >}}
The agent can change themes for you — just ask "switch to dark theme" and it
uses the `tui_control` tool.
{{< /hint >}}

## Model tiers

xbot uses three model tiers for different complexity levels. Configure via
`/settings`:

| Tier | Use case |
|------|----------|
| **Vanguard** | Strongest reasoning — complex tasks, architecture decisions |
| **Balance** | Balanced — general-purpose work |
| **Swift** | Fast/small — quick lookups, simple operations |

SubAgents automatically select the appropriate tier. Unconfigured tiers fall
back: vanguard → balance → swift.

## See also
- [Getting Started](/getting-started/) — quick start guide
- [Channels](/channels/) — all channel options
- [Configuration](/configuration/) — config.json reference
