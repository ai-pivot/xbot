---
title: "Tips & Tricks"
weight: 63
---

# Tips & Tricks

Advanced usage patterns and power-user tips for xbot.

## Conversations

### Use `/clear` liberally

Long conversations fill the context window. When you switch to a new task,
run `/clear` to start fresh. The agent's memory system retains important
context across sessions.

### Compress instead of clearing

If you want to keep the conversation but reduce token usage, use `/compress`.
This summarizes older messages and frees up context space without losing the
thread.

### Use context_edit for surgical cleanup

The `context_edit` tool lets you delete specific turns or messages from the
conversation — useful for removing a large tool output that's no longer
relevant.

## Model management

### Per-session model switching

Use `Ctrl+L` to switch the model for the current session only — without
affecting other sessions. This is great for using a cheaper model for simple
tasks and a stronger model for complex ones.

### Multiple subscriptions

Create subscriptions for different providers and switch with `Ctrl+P`:
- One for daily work (balanced cost/quality)
- One for complex reasoning (premium model)
- One for quick lookups (fast/cheap model)

### Model tiers for SubAgents

Configure Vanguard / Balance / Swift tiers in `/settings` so SubAgents
automatically use the right model for the task complexity.

## SubAgents

### Delegate to specialized agents

Don't do everything yourself. Delegate focused tasks:
- `explore` — codebase exploration and analysis
- Custom agents in `~/.xbot/agents/` — your own specialized roles

### Group Chat for architecture decisions

Use the Meeting Mode to get multiple expert perspectives:
```
"Review this API design. Get input from security, performance, and UX
experts, then synthesize."
```

## Automation

### Scheduled tasks with Cron

The agent can schedule tasks for itself:
```
"Every morning at 9 AM, check the CI status and notify me of any failures."
```

### Background tasks for long-running commands

Use background mode for build processes, dev servers, and tests:
```
"Run the test suite in the background and tell me the results."
```

The agent gives you a task ID — check progress with `task_status` without
blocking the conversation.

## Feishu power features

### Interactive message cards

The agent can create rich Feishu cards with buttons, images, and interactive
elements. Just describe what you want:
```
"Create a Feishu card showing the project status with clickable buttons for
each section."
```

### Bitable automation

Read, write, and manage Feishu Bitable (multidimensional tables):
```
"Read the project tracker Bitable and create a summary of overdue tasks."
```

### Feishu Docs

Create and edit Feishu Docs programmatically:
```
"Create a meeting notes Doc with today's action items."
```

## Configuration

### AI self-configuration

The agent can adjust its own settings — no manual config editing needed:
- "Switch to dark theme"
- "Set max concurrency to 5"
- "Change the font size to large"
- "Resize the sidebar to 40 characters"

### Custom themes

Create custom themes as JSON files in `~/.xbot/themes/`. Ask the agent:
"Create a custom theme with a warm color palette."

### Skills for common workflows

Create Skills (Markdown files in `~/.xbot/skills/`) for workflows you repeat:
- A GitHub PR review skill
- A deployment checklist skill
- A code style guide skill

{{< hint type=tip >}}
**The best tip:** Just ask the agent to do things. xbot is designed to be
conversational — if you can describe what you want, the agent can probably
figure out how to do it.
{{< /hint >}}
