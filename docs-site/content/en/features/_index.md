---
title: "Features"
weight: 30
---

# Features

xbot packs a comprehensive set of capabilities that make it more than just a chat
interface — it's a fully autonomous agent that can read, write, execute, search,
delegate, and schedule.

{{< columns >}}

## Core Tools

- [Built-in Tools](tools/) — 50+ tools: Shell, file I/O, web search, scheduling, cards
- [Skills & Agents](skills-agents/) — Markdown-based skill packs and role-based SubAgents
- [MCP Integration](mcp/) — Connect external tools via the Model Context Protocol
- [Memory System](memory/) — Pluggable memory: flat file-based or Letta (vector search + SQLite)

<--->

## Extensibility

- [Plugin System](plugins/) — Script-based plugins: widgets, hooks, custom tools
- [Hooks System](hooks/) — 17 lifecycle events with command/HTTP/MCP handlers

{{< /columns >}}

## Feature Overview

| Feature | What it does | Guide |
|---------|-------------|-------|
| **Tools** | 50+ built-in tools for file ops, execution, web, scheduling, context | [tools.md](tools/) |
| **Skills** | Markdown capability packs that guide the agent on specific tasks | [skills-agents.md](skills-agents/) |
| **SubAgents** | Role-based child agents for delegation and parallel work | [skills-agents.md](skills-agents/) |
| **Group Chat** | Moderated multi-agent meetings with @mention triggers | [skills-agents.md](skills-agents/) |
| **MCP** | Model Context Protocol for external tool integration | [mcp.md](mcp/) |
| **Memory** | Flat (default) or Letta (MemGPT) memory providers | [memory.md](memory/) |
| **Plugins** | Script-based plugins: info bar widgets, tool hints, custom tools | [plugins.md](plugins/) |
| **Hooks** | Lifecycle event hooks with command/HTTP/MCP handlers | [hooks.md](hooks/) |

## Quick Decision Guide

**I want to...** → **Use this:**

- Run shell commands, edit files, search code → [Built-in Tools](tools/)
- Teach the agent a workflow (checklist, convention) → [Skills](skills-agents/#skills)
- Delegate work to a specialist agent → [SubAgents](skills-agents/#subagents)
- Connect an external API or service → [MCP Integration](mcp/)
- Show a status bar widget in the TUI → [Plugins](plugins/)
- Run a script before/after tool execution → [Hooks](hooks/)
- Persist information across conversations → [Memory](memory/)
