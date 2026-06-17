---
name: plugin-creator
description: Create, modify, and manage xbot plugins. Use when the user asks to create a plugin, set up script plugins, configure widgets, register custom tools via plugins, or create channel plugins.
---

# Plugin Creator

Create and manage xbot plugins.

## When to Activate

- User wants to create/modify/delete plugins
- User wants to add custom tools, widgets, context enrichers, or channels
- User asks about plugin system configuration
- User wants a plugin that connects to an external service (GitHub App, Telegram bot, etc.)
- User wants channel plugins that provide their own tools or channel-specific prompts

## Plugin Types

| Type | Complexity | Use Case |
|------|-----------|----------|
| **script** | Low | Shell-based plugins: widgets, notifications, simple tools |
| **native** | Medium | Go in-process plugins (requires compilation) |
| **stdio** | High | External process plugins: channels, channel-scoped tools, Python/Go/any language |

**Prefer script plugins** for widgets and hooks. **Use stdio for channel plugins and channel-scoped tools.**

## Plugin Location

- User plugins: `~/.xbot/plugins/<plugin-id>/`
- Project plugins: `<project>/.xbot/plugins/<plugin-id>/`
- Built-in examples: `plugin/examples/`

**IMPORTANT**: The directory name MUST match the plugin ID in `plugin.json`.

## Decision Tree

```
User wants to...
├─ Show info in TUI (git branch, tokens) → script plugin + widget → read script-plugins.md
├─ React to tool events (post-edit diff) → script plugin + hook → read script-plugins.md
├─ Add a new message channel (Telegram) → stdio channel plugin → read channel-plugins.md
├─ Channel + custom agent prompt (Telegram formatting rules) → stdio channel + channel_prompt → read channel-plugins.md
├─ Channel + custom tools (GitHub App CR) → stdio channel + channel_tools → read channel-tools.md
└─ Register tools/hooks without channel → stdio plugin + contributes.tools
```

## Files in This Skill

| File | Content |
|------|---------|
| `script-plugins.md` | Script plugin: manifest format, UI slots, triggers, env vars, examples |
| `channel-plugins.md` | stdio channel plugin: protocol, RPC methods, event types, **channel prompt**, Python example |
| `channel-tools.md` | Channel-scoped tools: `channel_tools` protocol, `execute_tool` RPC, complete example |

When you need detailed guidance on a specific plugin type, load the relevant file:
```
Skill(name="plugin-creator", file="channel-tools.md")
```

## Workflow

1. **Understand requirement**: Widget? Tool? Channel? Channel+Tools? Channel+Prompt?
2. **Choose plugin type**: script for widgets/hooks, stdio for channels/tools
3. **Choose plugin location**: `~/.xbot/plugins/<id>/` for user-level
4. **Create plugin.json**: Ensure directory name matches plugin ID
5. **Create entry point**: Script or compiled binary
6. **Ensure executable**: `chmod +x` for scripts and binaries
7. **Reload plugins**: Use `tui_control(action="reload_plugins")` to hot-reload

## After Creating/Modifying Plugins

**IMPORTANT**: Always reload plugins so changes take effect immediately:

```
tui_control(action="reload_plugins")
```

## Available Permissions

| Permission | Description |
|-----------|-------------|
| `tools.register` | Register new tools |
| `tools.call` | Call other tools |
| `hooks.subscribe` | Subscribe to lifecycle hooks |
| `context.enrich` | Inject system prompt content |
| `storage.private` | Plugin private KV storage |
| `storage.shared` | Cross-plugin shared storage |
| `network.outbound` | Make network requests |
| `ui.contribute` | Contribute UI widgets |
| `channels.register` | Register custom channels |

## Gotchas

- **Directory name MUST match plugin ID** in plugin.json. `"id": "my.telegram"` → `~/.xbot/plugins/my.telegram/`
- Script must be executable: `chmod +x my-script.sh`
- `sync: true` means the widget blocks tool execution until rendered (use for toolHint)
- Widget output is limited in size — keep it short
- Plugin ID must be unique across all plugins
- Script output without `style|` prefix is treated as plain text
- stdio channel plugins: `entry` command spawns a **dedicated** process for the channel (separate from activation)
- stdio plugins use bidirectional JSON-RPC (same as WS protocol), NOT the old request-response protocol
- Plugin must handle both old-style activation (`{"method":"activate"}`) and new-style JSON-RPC events
- stdio plugins receive `channel_config` event with initial configuration, NOT `channel_start` request
- stdio plugins send `send_inbound` RPC to push messages, NOT `channel_inbound` async push
- Channel plugins can declare channel-specific agent prompts via `channel_prompt` message (see channel-plugins.md)
- Channel plugins can declare channel-scoped tools via `channel_tools` message (see channel-tools.md)
- Both `channel_prompt` and `channel_tools` support **hot-update**: sending a new message replaces the previous set
- Channel names cannot be: `feishu`, `qq`, `napcat`, `web`, `cli`
