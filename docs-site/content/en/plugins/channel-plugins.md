---
title: "Channel Plugins"
weight: 14
---

Channel plugins are full channel adapters that extend xbot with new communication channels (e.g., GenUI display_html). They use the stdio protocol with additional channel-specific messages.

## Overview

A channel plugin is a stdio plugin that declares a `channel_provider` in its activate response. xbot creates a `ChannelProvider` bridge that connects the plugin process to xbot's channel system.

## Channel Provider Declaration

In the activate response, return a `channel_provider`:

```json
{
  "channel_provider": {
    "name": "my-channel",
    "config_schema": [
      {
        "key": "enabled",
        "label": "Enable",
        "description": "Enable this channel",
        "type": "toggle",
        "default_value": "true"
      }
    ]
  }
}
```

### ChannelProviderDecl (Go)

```go
type ChannelProviderDecl struct {
    Name         string           `json:"name"`
    ConfigSchema []map[string]any `json:"config_schema,omitempty"`
    // Entry info populated by xbot from the plugin manifest
    Entry      string   `json:"-"`
    Executable string   `json:"-"`
    Args       []string `json:"-"`
    Dir        string   `json:"-"`
}
```

## Channel Plugin Protocol

Channel plugins use additional inbound messages beyond the standard stdio protocol:

### `channel_tools`

Declares tools for a specific channel. Sent as an inbound message from the plugin:

```json
{
  "method": "channel_tools",
  "params": {
    "tools": [
      {
        "name": "display_html",
        "description": "Display HTML content",
        "input_schema": {
          "type": "object",
          "properties": {
            "code": {"type": "string"}
          }
        }
      }
    ]
  }
}
```

Tools are registered with `RegisterForChannel("channel-name", tool)` — only visible when the channel is active.

### `channel_prompt`

Declares channel-specific system prompt parts:

```json
{
  "method": "channel_prompt",
  "params": {
    "system_parts": {
      "05_channel_myplugin": "You have access to display_html tool..."
    }
  }
}
```

Key naming convention: `"05_channel_xxx"` prefix (after `"00_base"`, before `"10_skills"`).

### `web_ui`

Declares web UI components for the channel:

```json
{
  "method": "web_ui",
  "params": {
    "widgets": [...]
  }
}
```

### `channel_inbound`

Pushes user messages from the channel to xbot:

```json
{
  "method": "channel_inbound",
  "params": {
    "message": "user input from channel"
  }
}
```

## ChannelToolBridge

`ChannelToolBridge` wraps channel-declared tools. When the LLM calls a channel tool:

1. xbot routes the call to `ChannelToolBridge.Execute`
2. The bridge sends an `execute_tool` request to the plugin process via `Call("execute_tool")`
3. The plugin processes the request and returns a result
4. The bridge wraps the result with `Detail` (for UI rendering) and `ui_code` (for web)

### Tool Execution Flow

```
LLM calls tool → ChannelToolBridge.Execute
  → Call("execute_tool", {toolName, input})
  → Plugin processes and returns result
  → Bridge wraps result with Detail/ui_code
  → Result returned to LLM
```

## Channel Configuration

Channel plugins declare config schema in the `channel_provider` response. Users configure channels in `config.json`:

```json
{
  "channels": {
    "my-channel": {
      "enabled": "true"
    }
  }
}
```

**Important**: The channel provider's `IsEnabled` check requires `channels.<name>.enabled=true` in config.json. Without this, the channel is never created.

## ChannelProviderFactory

`ChannelProviderFactory` is registered by `serverapp` during initialization to create channel provider instances without a `plugin → channel` import cycle:

```go
type ChannelProviderFactory func(decl *ChannelProviderDecl, process *StdioPluginProcess) (any, error)
```

## WireChannelProviders

After `ActivateAll()`, `WireChannelProviders(pm)` connects all active channel providers to the external registry:

```go
func WireChannelProviders(pm *PluginManager) {
    // Iterates active plugins
    // For each, gets ChannelProviders from context
    // Registers each via globalChannelProviderRegistrar
}
```

## Complete Example: GenUI Plugin

The `xbot-genui` plugin is a real channel plugin example:

```json
{
  "id": "xbot.genui",
  "name": "GenUI (display_html)",
  "version": "1.0.0",
  "runtime": "grpc",
  "entry": "./bin/genui-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["channels.register", "tools.register", "ui.contribute"],
  "contributes": {
    "channelProvider": {
      "name": "genui",
      "config_schema": [
        {
          "key": "enabled",
          "label": "Enable",
          "type": "toggle",
          "default_value": "true"
        }
      ]
    }
  }
}
```

The plugin process:
1. On `activate`, returns `channel_provider` with name `"genui"`
2. Sends `channel_tools` to declare the `display_html` tool
3. Sends `channel_prompt` to declare system prompt parts
4. On `execute_tool`, renders HTML and returns the result

## See Also

- [Stdio Protocol](./stdio-protocol/) — Base JSON-RPC protocol
- [Architecture](./architecture/) — How channel plugins fit in
- [Configuration](./configuration/) — Channel configuration
- [Built-in Plugins: GenUI](./builtin/genui/) — Real channel plugin example
