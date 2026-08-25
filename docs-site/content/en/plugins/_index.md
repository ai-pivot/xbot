---
title: "Plugin System"
weight: 25
geekdocCollapseSection: true
---

xbot's plugin system provides VSCode-like extensibility through a unified architecture supporting multiple runtimes (native Go, stdio/gRPC, script, and future WASM). Plugins can contribute tools, hooks, widgets, context enrichers, commands, themes, channel providers, and web UI extensions.

## Key Features

- **Multiple Runtimes**: Native Go plugins, stdio/gRPC plugins (any language), script plugins (bash), and WASM (planned)
- **Declarative Manifests**: Each plugin declares its capabilities via `plugin.json`
- **Permission System**: Fine-grained capability control — plugins only access what they declare
- **Hook System**: Pre/post tool use, session lifecycle, user prompt, and custom events
- **Widget System**: CLI status bar, title bar, info bar, and footer widgets
- **Web Plugin v2**: Type-safe ESM frontend plugins with declarative UI contributions
- **Channel Plugins**: Full channel adapters (e.g., GenUI display_html) via stdio protocol
- **Event Bus**: Plugin-to-plugin pub/sub communication
- **KV Storage**: Per-plugin persistent key-value storage
- **Hot Reload**: Configuration-driven plugin activation/deactivation
- **Auto-retry**: Exponential backoff for failed plugins
- **Dependency Resolution**: Topological sort with cycle detection

## Documentation Sections

- [Getting Started](./getting-started/) — Create your first plugin in 5 minutes
- [Architecture](./architecture/) — How the plugin system works under the hood
- [Plugin Manifest](./manifest/) — Complete `plugin.json` specification
- [Plugin Lifecycle](./lifecycle/) — Activate, deactivate, and activation events
- [PluginContext API](./plugin-context/) — The plugin's gateway to xbot
- [Permissions](./permissions/) — Capability-based security model
- [Storage](./storage/) — Per-plugin KV storage
- [Event Bus](./event-bus/) — Plugin-to-plugin communication
- [Hooks](./hooks/) — Lifecycle hooks and interceptors
- [Widgets](./widgets/) — CLI and Web UI widgets
- [Tools](./tools/) — Register custom tools for the LLM
- [Script Runtime](./script-runtime/) — Bash script plugins
- [Stdio Runtime Protocol](./stdio-protocol/) — JSON-RPC protocol for any language
- [Channel Plugins](./channel-plugins/) — Full channel adapters
- [Configuration](./configuration/) — User-configurable plugin settings
- [Dependencies](./dependencies/) — Plugin dependency resolution
- [Migration](./migration/) — Plugin data migration system
- [Logging & Audit](./logging/) — Per-plugin logs and audit trail
- [Hot Reload & Monitoring](./hot-reload/) — Reload, config watching, auto-retry, health checks
- [Web Plugin System](./web/) — Frontend ESM plugin runtime
- [Cookbook](./cookbook/) — Step-by-step development guides
- [API Reference](./api-reference/) — Complete API reference
- [Built-in Plugins](./builtin/) — xbot's bundled plugins

## Quick Example

A minimal script plugin (`plugin.json`):

```json
{
  "id": "my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "runtime": "script",
  "entry": "bash main.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute"],
  "contributes": {
    "ui": [
      {
        "id": "greeting",
        "slot": "statusBarRight",
        "description": "Show a greeting"
      }
    ]
  }
}
```

The script `main.sh`:

```bash
#!/bin/bash
echo "Hello from my plugin!"
```

Place both files in `~/.xbot/plugins/my-plugin/` and restart xbot. The greeting appears in the status bar.
