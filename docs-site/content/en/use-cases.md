---
title: "Use Cases"
weight: 62
---

# Real-World Use Cases

See how teams use xbot in practice.

## Team Feishu Assistant

**Scenario:** A 20-person engineering team wants AI help without everyone
managing their own API keys.

**Setup:**
1. Deploy xbot in Server mode on a team server
2. Admin creates an LLM subscription via `/setup`
3. Connect a Feishu bot app
4. Add the bot to team group chats

**Result:** Anyone in the team @mentions the bot to:
- Generate code snippets and review PRs
- Look up documentation and answer technical questions
- Operate Feishu Docs and Bitable (create reports, update trackers)
- Run shell commands on the server (with permission control)

No individual API key management. The admin controls access via `allow_from`.

## Personal Coding Copilot

**Scenario:** A solo developer wants a powerful terminal AI assistant.

**Setup:**
1. Install in Standalone mode
2. Run `xbot-cli` in your project directory
3. The agent inherits your working directory and can read/write files

**Result:**
- Ask the agent to explore an unfamiliar codebase (`explore` SubAgent)
- Delegate focused tasks (code review, test writing) to SubAgents
- Run commands and debug issues interactively
- Use `/context` to manage token usage during long sessions

## Scheduled Automation

**Scenario:** You want the agent to run periodic checks and send alerts.

**Setup:**
```text
You: "Every morning at 9 AM, check if the nightly CI passed.
If any job failed, summarize the errors and notify me."

Agent: *uses Cron tool to schedule*
Agent: "Done. I'll check CI status every morning at 9 AM and
notify you of any failures."
```

**Result:** The agent schedules itself via the `Cron` tool. In Server mode,
the schedule survives restarts.

## Multi-Agent Architecture Review

**Scenario:** You need multiple expert perspectives on a design decision.

**Setup:**
```text
You: "Review this API design. Get input from a security expert,
a performance expert, and a UX expert, then synthesize."

Agent: *creates a Group Chat with three SubAgents*
Agent: "@security-expert what are the auth risks?"
Agent: "@performance-expert any bottlenecks?"
Agent: "@ux-expert is the API ergonomic?"
Agent: *synthesizes all three perspectives into a recommendation*
```

**Result:** The Group Chat Meeting Mode lets multiple specialized SubAgents
debate and converge on a recommendation.

## Feishu Document Automation

**Scenario:** Your team tracks work in Feishu Bitable and writes reports in
Feishu Docs.

**Setup:** The agent has native Feishu tools:

```text
You: "Read the project tracker Bitable, summarize the status of all
in-progress tasks, and create a weekly report Doc."

Agent: *uses feishu_bitable_list to read the tracker*
Agent: *uses feishu_docx_create to write the report*
Agent: "Done. I've created a weekly report Doc with the status summary."
```

**Result:** The agent reads Bitable records, processes data, and creates
Feishu Docs — all through conversational commands.

## Web Chat for Non-Technical Users

**Scenario:** You want to offer AI chat access to team members who don't use
the terminal.

**Setup:**
1. Deploy Server mode with `web.enabled: true`
2. Enable invite-only mode for access control
3. Share invite links with team members

**Result:** Non-technical users access the agent through a browser-based
chat UI with markdown rendering, code highlighting, and file uploads.

---

{{< hint type=note >}}
Have a use case we should document? Open an issue on
[GitHub](https://github.com/ai-pivot/xbot/issues).
{{< /hint >}}
