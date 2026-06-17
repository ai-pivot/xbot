---
title: "FAQ"
weight: 60
---

# Frequently Asked Questions

## Key Terms

| Term | Meaning |
|------|---------|
| **Subscription** | An LLM provider configuration (API key, model, base URL). You can have multiple. |
| **Channel** | A communication endpoint (CLI, Feishu, QQ, Web). |
| **SubAgent** | A child agent with its own context, delegated by the main agent. |
| **Skill** | A Markdown capability pack that guides the agent on specific tasks. |
| **Tier** | Model classification (Vanguard/Balance/Swift) for SubAgent task complexity. |
| **Session** | A conversation context. Multiple sessions can run independently. |
| **Sandbox** | Execution isolation for Shell commands (none or Docker mode). |

## Installation

### The installer fails with "connection refused" or "timeout"

If you're behind the GFW (China), use the mirror-accelerated installer:

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

You can also set `GH_MIRROR=gh-proxy.com` or `GH_MIRROR=ghfast.top` manually.

### Standalone vs Server — which should I pick?

- **Standalone**: solo developer, quick test drive. CLI only, stops when you
  close the terminal.
- **Server**: teams, multi-channel (Feishu/QQ/Web), shared LLM, always-on.
  **Most teams should choose Server mode.**

### How do I build from source?

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build
```

Requires Go 1.26+. The Web UI bundles are committed, so Node.js is not
needed for Go builds.

## LLM Configuration

### How do I use DeepSeek / Qwen / Ollama / other OpenAI-compatible APIs?

Set `provider: "openai"` and change the `base_url`:

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

### Can I use multiple LLM subscriptions?

Yes. Create multiple subscriptions in `config.json` (or via `/setup`), then
switch between them with `Ctrl+P` or `/models`. In Server mode, the admin
creates subscriptions once and the whole team shares them.

### What are model tiers (Vanguard / Balance / Swift)?

Model tiers let SubAgents use different models for different complexity
levels. Configure via `/settings`:
- **Vanguard** — strongest reasoning (complex tasks)
- **Balance** — balanced (general work)
- **Swift** — fast/small (quick lookups)

Unconfigured tiers fall back: vanguard → balance → swift.

### The Setup wizard didn't show the model list

The model list loads asynchronously from the provider. If your provider's
`/models` endpoint is slow or blocked, you can type the model name manually.
Use `/setup` → select the subscription → enter the model name.

## Channels

### How do I connect xbot to Feishu?

1. Create an app on the [Feishu Open Platform](https://open.feishu.cn)
2. Enable the bot capability and event subscriptions
3. Add the required permissions (`im:message`, `im:message.receive_v1`,
   `im:message:send_as_bot`, `contact:user.base:readonly`)
4. Add credentials to `~/.xbot/config.json`:

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

See the [Feishu channel guide](/channels/feishu/) for details.

### Can I restrict who can talk to the bot?

Yes. Use the `allow_from` field to whitelist user IDs:

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx",
    "allow_from": ["ou_xxx", "ou_yyy"]
  }
}
```

This works for all channels (Feishu, QQ, NapCat).

## TUI / CLI

### The agent is slow — how to improve performance?

- Use `/compress` regularly to keep context small
- Switch to a faster model for simple tasks (`Ctrl+L`)
- Use SubAgents for parallel work (each has its own context)
- Increase `max_concurrency` for parallel tool execution
- Use background mode for long-running commands

### How do I switch sessions?

Open the sidebar (it's always visible by default). Click any session to
switch. Or use `/sessions` to list, `/su` to switch, `/new` to create.

### Which model should I use?

- **Complex reasoning** (architecture, debugging): GPT-4o, Claude Sonnet/Opus
- **General work** (writing, editing): GPT-4o-mini, Claude Haiku, DeepSeek
- **Quick lookups** (simple Q&A): Any fast model works
- **Budget-conscious**: DeepSeek, Qwen, or local models via Ollama

Switch models per-session with `Ctrl+L`, or globally with `Ctrl+N`.

### How do I change the theme?

`Ctrl+K → Theme`, or type `/palette theme`. You can also create custom
themes — see the [ai-config skill](/features/).

### The agent seems slow — how do I check token usage?

Type `/context` to see current prompt token usage and context bar. Use
`/clear` to reset the conversation, or `/compress` to manually compress.

## Sandbox

### Should I enable Docker sandboxing?

If the agent runs untrusted commands or works in a shared environment, yes.
Docker sandboxing isolates shell execution. For personal development,
`mode: "none"` (the default) is fine.

See the [Sandbox guide](/guides/sandbox/) for Docker setup.

### Security best practices

- Use `allow_from` to restrict who can talk to the agent
- Enable Docker sandboxing on shared servers
- Use permission control for privileged operations
- Keep API keys in subscriptions (never in environment variables for production)
- Regularly review `/context` to monitor what the agent is doing

## Troubleshooting

### "connection refused" when CLI connects to Server

Make sure the server is running: `xbot-cli serve`. Check that `~/.xbot/config.json`
has the correct `cli.server_url` and `cli.token` matching the server's
`admin.token`.

### MCP server tools not appearing

The agent discovers MCP tools dynamically. Use the `ManageTools` tool to list
and manage MCP servers. MCP servers connect via stdio or HTTP — check that the
executable path is correct and accessible from the xbot process PATH.

### SubAgent seems to hang

SubAgents run in their own context. If a SubAgent is stuck, you can
interrupt it with Ctrl+C, or check its progress via the SubAgent panel
(`Ctrl+T`).

### Agent can't access files outside the working directory

The agent's working directory is set via the `work_dir` config or inherited
from where you launch `xbot-cli`. The agent can use the `Cd` tool to navigate.
If files are outside the expected directory, tell the agent the full path.

### Context fills up too quickly

Use `/compress` to summarize old messages and free up context space. For a
fresh start, use `/clear`. Check token usage with `/context`. For long
sessions, consider using SubAgents to delegate work — they have their own
context windows.

### How to export a conversation

Ask the agent: "Export this conversation as Markdown" — it will use the
`FileCreate` tool to write the conversation to a file. You can also use
`/rewind` to go back to a specific point in the conversation.

### How does xbot protect my data?

xbot is **fully self-hosted** — all data stays on your server. Conversations
are stored in local SQLite databases. API keys are in your config file, never
sent to third parties. No telemetry or analytics are collected.

### Agent repeats the same action

If the agent gets stuck in a loop, use `Ctrl+C` to interrupt, then `/clear`
to reset the conversation. You can also use `context_edit` to remove
specific messages that might be causing the loop.

{{< hint type=note >}}
**Need more help?** Check the [full documentation](/) or open an issue on
[GitHub](https://github.com/ai-pivot/xbot/issues).
{{< /hint >}}
