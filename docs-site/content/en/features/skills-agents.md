---
title: "Skills & Agents"
weight: 40
---

# Skills & Agents

xbot has two extension mechanisms that work together: **Skills** provide
knowledge, and **Agents** provide execution capacity. Both are plain Markdown
files — the AI can create and manage them for you.

{{< hint type=tip >}}
**Just tell the agent what you want.** You don't need to write files manually.
Say "create a skill for debugging Go concurrency bugs" or "make a security
reviewer agent" — the AI handles everything from creation to activation.
{{< /hint >}}

## Skills

A Skill is a **capability pack** — a Markdown document that instructs the agent
how to handle a specific type of task. Think of it as a playbook: "when
debugging, follow this checklist," or "before committing, run these checks."

### How Skills Work

Skills are loaded **on demand**. When your conversation matches a skill's
domain, the agent automatically loads the relevant guidance. You can also
trigger skills explicitly:

- Type `/skill-name` in conversation (e.g., `/debug`, `/plan`)
- Or describe your need naturally — the agent matches the right skill

### Built-in Skills

| Skill | Purpose |
|-------|---------|
| `debug` | Debug bugs: locate, analyze, and fix |
| `plan` | Plan code changes before implementing |
| `post-dev` | Post-development cleanup: update docs, commit |
| `agent-creator` | Create new Agent roles |
| `skill-creator` | Create new Skills |
| `plugin-creator` | Create plugins |
| `hook-creator` | Create lifecycle hooks |
| `ai-config` | Configure themes, subscriptions, TUI layout |
| `worktree` | Multi-agent parallel workspace isolation |

### Where Skills Live

```
~/.xbot/skills/          ← User-level (available in all projects)
└── my-skill/
    └── SKILL.md         ← Skill definition file
```

Skills can also be embedded in projects for team sharing:

```
<project>/.xbot/skills/  ← Project-level (committed to git)
└── project-skill/
    └── SKILL.md
```

### Skill File Format

Each skill is a directory containing a `SKILL.md` file:

```markdown
---
name: my-skill
description: A one-line summary of what this skill does
---

# Skill Title

## Goals
...

## Steps
...
```

The `---` delimited section is **frontmatter** (metadata). Everything below is
the guidance document — plain instructions for the AI.

## SubAgents

A SubAgent is an **independent assistant** with its own context, tools, and
role. You delegate tasks to it, and it works autonomously.

### When to Use a SubAgent

- **Need a specialist role:** "Have the security expert review this code"
- **Need parallel work:** "Review this PR from security, performance, and
  style perspectives simultaneously"
- **Need isolated context:** "Explore this module without cluttering the
  current conversation"

### SubAgent Calling Modes

| Mode | Behavior |
|------|----------|
| **One-shot** (default) | Run once in foreground, return the final result. |
| **Interactive** | Persistent multi-turn session. Create, send messages, unload when done. |
| **Background** | Interactive session that runs while you do other work. Check back later. |

### Built-in Agents

xbot ships with 10 role-specific agents modeled after the Three Departments and
Six Ministries of classical Chinese governance:

| Agent | Role | Best at |
|-------|------|---------|
| `explore` | Explorer | Code analysis, tracing call chains, summarizing modules |
| `chancellery` | Reviewer | Plan review, quality assurance |
| `secretariat` | Planner | Architecture design, requirement analysis |
| `department-state` | Dispatcher | Breaking plans into tasks and delegating |
| `ministry-works` | Engineer | Writing code, refactoring |
| `ministry-justice` | Bug Hunter | Bug detection, edge case analysis |
| `ministry-personnel` | Style Reviewer | Code style, naming conventions |
| `ministry-revenue` | Performance Analyst | Performance optimization, dependency review |
| `ministry-defense` | Security Reviewer | Vulnerability scanning, permission checks |
| `ministry-rites` | Doc Reviewer | Documentation quality, comment standards |

### How to Use

Just talk to xbot naturally:

> "Explore how this module works"

> "Have the security expert review this code"

> "Review this PR from security, performance, and style perspectives"

The agent automatically selects the right SubAgent and delegates the task.

### Agent File Format

Each agent is a Markdown file stored in `~/.xbot/agents/`:

```markdown
---
role: my-role
description: A one-line summary of what this agent does
tools: Read, Grep, Glob, Shell
model: swift
---

# Agent Role Description

You are XXX, specializing in XXX...
```

| Field | Purpose |
|-------|---------|
| `role` | Unique role name used in `SubAgent(role="...")` |
| `description` | What the agent does — shown in listings |
| `tools` | Comma-separated list of tools the agent can use |
| `model` | Model tier: `vanguard` (strongest), `balance` (default), or `swift` (fastest) |

### Agent Storage

```
~/.xbot/agents/
├── explore.md          # Code exploration
├── chancellery.md      # Plan review
├── secretariat.md      # Architecture planning
└── ...
```

## Group Chat (Meeting Mode)

![SubAgents and Group Chat](/img/cli/subagents.gif)

Group Chat is a **moderated multi-agent discussion**. You create a group,
invite agents, and control who speaks via @mentions.

### How Meeting Mode Works

1. **Create a group** with `CreateChat(type="group", members=[...])`
2. **Send messages** — messages **without** @mentions just add to the history
3. **Trigger speakers** — `@agent:role/instance` in your message triggers
   that agent to respond
4. **Full context** — triggered agents see the entire discussion history plus
   your message
5. **All responses preserved** — every agent reply is added to the history for
   future reference

### Example Workflow

```
1. CreateChat(type="group", members=["agent:reviewer/r1", "agent:tester/t1"])
   → returns "group:g1"

2. SendMessage(to="group:g1", message="Let's discuss the API design.")
   → No agent triggered. Just adds to history.

3. SendMessage(to="group:g1", message="@agent:reviewer/r1 What do you think?")
   → Reviewer responds with full context.

4. SendMessage(to="group:g1", message="@agent:tester/t1 Any concerns?")
   → Tester responds, seeing reviewer's earlier reply too.
```

{{< hint type=note >}}
Groups auto-close after `max_rounds` moderator messages with @mentions (default
10). Use `SendMessage(to="group:...", message="...")` to continue the discussion.
{{< /hint >}}

## Market

xbot supports publishing, browsing, and installing skills and agents:

| Command | Description |
|---------|-------------|
| `/browse` | Browse the market |
| `/install <name>` | Install a skill or agent |
| `/uninstall <name>` | Uninstall |
| `/publish` | Publish your skill or agent |
| `/unpublish` | Remove from market |

## See also
- [Built-in Tools](/features/tools/) — all available tools
- [MCP](/features/mcp/) — external tool integration
- [Tips & Tricks](/tips/) — SubAgent delegation patterns
