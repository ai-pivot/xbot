---
title: "Plugin Manifest"
weight: 3
---

The `plugin.json` manifest is the declarative description of a plugin. It declares metadata, runtime type, entry point, permissions, and contributions.

## Schema Reference

### Required Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique plugin identifier (reverse DNS recommended, e.g. `xbot.git-fancy`). Must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` |
| `name` | string | Human-readable plugin name |
| `version` | string | Semantic version (e.g. `1.0.0`). Must be strict `MAJOR.MINOR.PATCH` |
| `runtime` | string | Runtime type: `native`, `stdio`, `grpc` (alias for `stdio`), or `script` |

### Entry Point

| Field | Type | Description |
|-------|------|-------------|
| `entry` | string | Entry point. For `script`: command to execute (e.g. `bash main.sh`). For `stdio`: command to start the process |
| `entry_windows` | string | Platform-specific override for Windows |
| `entry_darwin` | string | Platform-specific override for macOS |
| `entry_linux` | string | Platform-specific override for Linux |
| `executable` | string | Explicit executable path (takes precedence over `entry`). Use for security |
| `args` | string[] | Command-line arguments passed to `executable` |

### Activation

| Field | Type | Description |
|-------|------|-------------|
| `activation_events` | string[] | Events that trigger activation. Supports: `onStart`, `onTool:<name>`, `onHook:<event>`, `onCommand:<cmd>`. Empty = `onStart` |

### Permissions

| Field | Type | Description |
|-------|------|-------------|
| `permissions` | string[] | Required capabilities. The plugin can only access APIs for declared permissions |

Available permissions:

| Permission | Description |
|-----------|-------------|
| `tools.register` | Register tools for the LLM |
| `hooks.register` | Register lifecycle hooks |
| `bus.read` | Subscribe to event bus |
| `bus.write` | Publish to event bus |
| `bus.plugin` | Plugin-to-plugin events (requires `bus.read` + `bus.write`) |
| `ui.contribute` | Contribute UI widgets |
| `ui.themes` | Contribute themes |
| `channels.register` | Register channel providers |
| `storage` | Access per-plugin KV storage |
| `cron` | Schedule cron jobs |
| `rpc` | Make RPC calls to the backend (Web plugins) |
| `ui` | Access UI API (Web plugins) |
| `events` | Access event bus (Web plugins) |
| `commands` | Register commands (Web plugins) |
| `state` | Access shared state (Web plugins) |
| `plugins` | Access plugin management API (Web plugins) |
| `config` | Access configuration API (Web plugins) |

### Contributions

The `contributes` object declares what the plugin provides:

```json
{
  "contributes": {
    "tools": [...],
    "hooks": [...],
    "context_enrichers": [...],
    "commands": [...],
    "crons": [...],
    "themes": [...],
    "overlays": [...],
    "configuration": {...},
    "ui": [...]
  }
}
```

#### Tools

```json
{
  "tools": [
    {
      "name": "my-tool",
      "description": "Does something useful",
      "input_schema": {
        "type": "object",
        "properties": {
          "input": { "type": "string" }
        }
      }
    }
  ]
}
```

#### Hooks

```json
{
  "hooks": [
    {
      "event": "PreToolUse",
      "matcher": "Shell"
    }
  ]
}
```

Hook events: `PreToolUse`, `PostToolUse`, `UserPrompt`, `AgentStop`, `SessionStart`, `SessionEnd`, `OnError`, `AllToolUse`.

The `matcher` field is a tool name pattern. Empty string = all tools.

#### UI Widgets

```json
{
  "ui": [
    {
      "id": "my-widget",
      "slot": "statusBarRight",
      "priority": 100,
      "description": "Shows status info"
    }
  ]
}
```

Widget zones: `titleBarLeft`, `titleBarRight`, `statusBarLeft`, `statusBarRight`, `infoBar`, `footer`.

#### Configuration

```json
{
  "configuration": {
    "title": "My Plugin Settings",
    "properties": {
      "apiKey": {
        "type": "string",
        "label": "API Key",
        "description": "Your API key",
        "secret": true,
        "required": true
      },
      "maxItems": {
        "type": "number",
        "label": "Max Items",
        "default": 10,
        "minimum": 1,
        "maximum": 100
      },
      "enabled": {
        "type": "boolean",
        "label": "Enabled",
        "default": true
      },
      "mode": {
        "type": "select",
        "label": "Mode",
        "default": "auto",
        "options": [
          {"label": "Auto", "value": "auto"},
          {"label": "Manual", "value": "manual"}
        ]
      }
    }
  }
}
```

Config property types: `string`, `number`, `boolean`, `select`, `multiselect`.

#### Crons

```json
{
  "crons": [
    {
      "message": "Check for updates",
      "every_seconds": 3600
    }
  ]
}
```

Cron fields: `message` (required), `cron_expr` (standard cron), `every_seconds`, `at` (absolute time), `delay_seconds` (one-shot relative).

#### Themes

```json
{
  "themes": [
    {
      "id": "dracula",
      "file": "themes/dracula.json"
    }
  ]
}
```

### Dependencies

```json
{
  "dependencies": [
    {
      "id": "xbot.utils",
      "version": "^1.0.0"
    }
  ]
}
```

Dependencies are resolved via topological sort (Kahn's algorithm). Circular dependencies return an error.

### Web Plugin Declaration

```json
{
  "web": {
    "entry": "index.js",
    "contributes": [...]
  }
}
```

The `web` field declares a frontend ESM module. The `contributes` field is an opaque JSON blob passed verbatim to the frontend runtime — the backend does not validate web contribution semantics.

### Timeout

```json
{
  "timeout": "30s"
}
```

Maximum duration for plugin activation and tool operations. Accepts Go duration strings (`30s`, `1m`, `500ms`). Default: 30s. Maximum: 5 minutes.

## Complete Example

```json
{
  "id": "xbot.example",
  "name": "Example Plugin",
  "version": "1.0.0",
  "description": "An example plugin demonstrating all features",
  "author": "xbot",
  "homepage": "https://github.com/user/example-plugin",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register", "hooks.register", "ui.contribute", "storage"],
  "timeout": "60s",
  "contributes": {
    "tools": [
      {
        "name": "greet",
        "description": "Greet someone",
        "input_schema": {
          "type": "object",
          "properties": {
            "name": {"type": "string"}
          },
          "required": ["name"]
        }
      }
    ],
    "hooks": [
      {"event": "PreToolUse", "matcher": "Shell"}
    ],
    "ui": [
      {"id": "status", "slot": "statusBarRight", "description": "Status indicator"}
    ],
    "configuration": {
      "title": "Example Settings",
      "properties": {
        "greeting": {"type": "string", "default": "Hello", "label": "Greeting"}
      }
    }
  },
  "dependencies": [
    {"id": "xbot.utils", "version": "^1.0.0"}
  ]
}
```

## Validation Rules

- `id` must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` (prevents path traversal, null bytes, injection)
- `version` must be strict semver `MAJOR.MINOR.PATCH`
- `runtime` must be one of: `native`, `stdio`, `grpc`, `script`
- At least one entry point must be defined (`entry` or platform-specific)
- `permissions` are validated against the known permission list

## See Also

- [Plugin Lifecycle](./lifecycle/) — Activation and deactivation
- [Permissions](./permissions/) — Permission system details
- [API Reference: Manifest Schema](./api-reference/manifest-schema/) — JSON schema
