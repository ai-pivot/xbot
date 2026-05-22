---
name: agent-creator
description: "Manage SubAgent roles: create, view, modify, or delete agent definitions. MUST activate when user asks to create/edit/view/inspect any agent, or when you need to look at agent files under ~/.xbot/agents/."
---

# Agent Creator

Create new SubAgent roles for specialized tasks.

## Instructions

### Step 1: Understand the Agent's Purpose

Ask the user:
1. What task should this agent handle?
2. What tools does it need?
3. Any specific output format or workflow?

### Step 2: Create Agent File

**IMPORTANT**: Agents can be created in two locations:

1. **Global agents** — Create in the system prompt's **"Agents 存储目录"** path (e.g. `~/.xbot/agents/{agent-name}.md`). These are available in ALL projects and sessions. This is the default choice for general-purpose agents.

2. **Project-local agents** — Create in the current project's `.xbot/agents/` directory (e.g. `<project-root>/.xbot/agents/{agent-name}.md`). These are ONLY available when working inside that project. This is ideal for project-specific roles, domain-specialized agents, or team-shared agents that live alongside the code.

   To determine the project root, check the system prompt's **"📂 默认工作目录"** or the **"项目 Agents 目录"** line if present.

   **When to use project-local**: the agent is specific to this codebase, understands project conventions, works with project-specific tools/workflows, or should be version-controlled with the project (commit the `.xbot/agents/` directory).

The system prompt also shows a **"项目 Agents 目录"** line when project-local agents are detected — use this path when creating project-local agents.

Each agent's `<dir>` field in the system prompt also shows its definition location; `embed` means built-in.

Agent definition uses YAML frontmatter + Markdown body:

```markdown
---
name: {agent-name}
description: "{What this agent does. Use WHEN to use it — this is the trigger.}"
model: balance
tools:
  - ToolName1
  - ToolName2
capabilities:
  memory: true
  send_message: false
  spawn_agent: true
---

You are a {agent-name} agent. Your job is to {one-sentence purpose}.

## Process

1. **Step 1** — Description
2. **Step 2** — Description
3. **Step 3** — Description

## Output Format

### Summary
One paragraph: what was done, overall result.

### Details
Structured output based on task type.

## Rules

- **Rule 1** — What to do
- **Rule 2** — What to avoid
- **Rule 3** — Specific constraints
```

### Step 3: Choose Tools

Common tools for agents:
- **Code**: Read, Grep, Glob, Shell, Edit
- **Research**: WebSearch, Fetch, Grep, Glob
- **Testing**: Shell, Read, Glob
- **Communication**: feishu_send_message, feishu_docx_*

If `tools` is omitted, the agent gets the full dynamic tool set (search_tools + load_tools).
If `tools` is specified, only those tools are directly available — no search/load needed.

### Step 3.5: Choose Model Tier

The `model` field in frontmatter controls which LLM model the agent uses. Three tiers are available:

| Tier | When to use | Examples |
|------|-------------|---------|
| `vanguard` | Complex reasoning, architecture decisions, multi-step analysis | Code review of critical PRs, complex refactoring plans |
| `balance` (default) | General tasks, most agents | Code exploration, test writing, docs, debugging |
| `swift` | Simple/fast tasks, bulk operations | Log parsing, file searching, formatting, trivial edits |

**Rules:**
- If `model` is omitted, defaults to `balance` (NOT the parent agent's model).
- The caller can override with the `model_tier` parameter in SubAgent() at invocation time.
- Think about cost vs quality: don't use vanguard for grep/search tasks; don't use swift for design tasks.
- Examples: exploration agents → `swift`; code reviewer → `vanguard`; most others → omit (gets `balance`).

### Step 4: Configure Capabilities

Capabilities control what extra powers the agent has:

| Capability | Default | Description |
|------------|---------|-------------|
| `memory` | false | Access to Letta memory system (core/archival/recall) |
| `send_message` | false | Can send messages directly to IM channels |
| `spawn_agent` | true | Can create sub-agents (watch recursion depth) |

### Step 5: Write Quality Content

Follow `code-reviewer.md` quality standard:
- ✅ Specific process steps (not vague)
- ✅ Clear output format with examples
- ✅ Explicit rules and constraints
- ✅ Edge case handling
- ❌ Avoid generic descriptions like "analyze code" — specify how

**🚫 NEVER use absolute paths** (e.g. `/home/user/...`, `/opt/...`) in agent definition files. Use relative paths, environment variables (`$HOME`, `$XBOT_SRC`), or let the agent discover paths at runtime. Absolute paths break portability across machines.

### Step 6: Verify

List available agents to confirm:
```bash
ls -la ~/.xbot/agents/           # global agents
ls -la .xbot/agents/             # project-local agents (if applicable)
```

## Agent Naming Convention

- Use lowercase with hyphens: `code-reviewer`, `explorer`, `tester`
- Name should reflect its role/function
- Description must include "Use when..." trigger phrase

## Example

```markdown
---
name: data-analyst
description: "Data analysis agent. Use when user needs to analyze data, generate insights, or create visualizations."
tools:
  - Read
  - Grep
  - Shell
capabilities:
  memory: true
---

You are a data analyst agent. Your job is to analyze data and generate actionable insights.

## Process

1. **Understand data** — Read data files, identify structure and fields.
2. **Explore patterns** — Use shell commands (awk, sed, sort, uniq) to find patterns.
3. **Generate insights** — Summarize findings with specific numbers.

## Output Format

### Summary
Key findings in one paragraph.

### Statistics
| Metric | Value |
|--------|-------|
| Total records | X |
| Unique values | Y |

### Insights
- Finding 1
- Finding 2

## Rules
- Always provide specific numbers, not vague statements
- Use tables for structured data
- Cite file:line references when analyzing code
```
