# xbot Plugin Development Guide

A comprehensive guide for plugin developers — from getting started to advanced topics.

## Table of Contents

- [1. Quick Start (5 minutes)](#1-quick-start-5-minutes)
- [2. Choosing a Plugin Type](#2-choosing-a-plugin-type)
- [3. Manifest Guide (plugin.json)](#3-manifest-guide-pluginjson)
- [4. Permissions](#4-permissions)
- [5. Plugin Capabilities](#5-plugin-capabilities)
- [6. gRPC Runtime Development](#6-grpc-runtime-development)
- [7. Native Runtime Development (Go)](#7-native-runtime-development-go)
- [8. Storage](#8-storage)
- [9. Debugging](#9-debugging)
- [10. Deployment](#10-deployment)
- [11. Best Practices](#11-best-practices)
- [12. FAQ](#12-faq)

---

## 1. Quick Start (5 minutes)

### Python (gRPC runtime)

**Step 1**: Create the plugin directory:

```bash
mkdir -p ~/.xbot/plugins/my-plugin
cd ~/.xbot/plugins/my-plugin
```

**Step 2**: Create `plugin.json`:

```json
{
  "id": "com.example.my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "description": "My first xbot plugin",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register"]
}
```

**Step 3**: Create `main.py`:

```python
import sys
import json

def handle_activate(params):
    return {
        "tools": [{
            "name": "echo",
            "description": "Echo back the input message",
            "parameters": [
                {"name": "message", "type": "string", "description": "Message to echo", "required": True}
            ],
            "inputSchema": {
                "type": "object",
                "properties": {"message": {"type": "string", "description": "Message to echo"}},
                "required": ["message"]
            }
        }]
    }

def handle_execute_tool(params):
    tool = params.get("toolName", "")
    if tool == "echo":
        data = json.loads(params.get("input", "{}"))
        return {"result": f"Echo: {data.get('message', '')}"}
    return {"error": f"Unknown tool: {tool}"}

HANDLERS = {"activate": handle_activate, "deactivate": lambda p: {},
            "execute_tool": handle_execute_tool, "hook": lambda p: {"hookResult": {"decision": "allow"}},
            "enrich": lambda p: {"result": ""}}

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    handler = HANDLERS.get(req.get("method", ""))
    resp = handler(req.get("params", {})) if handler else {"error": "Unknown method"}
    print(json.dumps(resp), flush=True)
```

**Step 4**: Restart xbot. The `echo` tool is now available to the LLM.

### Go (native runtime)

See [examples/hello-world/](./examples/hello-world/) for a complete Go plugin example. Key steps:

1. Implement the `plugin.Plugin` interface (`Manifest`, `Activate`, `Deactivate`)
2. Register tools via `ctx.RegisterTool()`
3. Register with `pm.Register(myPlugin)` before discovery

---

## 2. Choosing a Plugin Type

| Factor | Native (Go) | gRPC (Any language) | WASM (Future) |
|--------|-------------|---------------------|---------------|
| **Language** | Go only | Any (Python, Node.js, Rust, etc.) | WASM-targeting languages |
| **Latency** | ~μs (zero-copy) | ~1-5ms (JSON serialization) | ~0.5-2ms |
| **Isolation** | Interface boundary | OS process isolation | Sandbox isolation |
| **Debugging** | Standard Go tools | stderr + manual protocol testing | Limited |
| **Event Bus** | ✅ Full access | ❌ Not available | ❌ Not available |
| **Hot Reload** | ✅ Supported | ✅ Supported | 🔮 Planned |
| **Status** | ✅ Phase 1 | ✅ Phase 1 | 🔮 Phase 2 |

### Decision Tree

```
Is your plugin written in Go?
├── Yes → Native runtime (recommended)
└── No
    ├── Do you need sandbox isolation?
    │   ├── Yes → WASM (not yet available)
    │   └── No → gRPC runtime
    └── Do you need Event Bus (plugin-to-plugin communication)?
        ├── Yes → Must use Native (Go)
        └── No → gRPC runtime
```

### When to Use gRPC

- You want to use Python, Node.js, Ruby, Rust, or any non-Go language
- You need process isolation (the plugin crash doesn't affect xbot)
- You're wrapping an existing service or CLI tool
- You want to leverage language-specific libraries (e.g., Python's ML ecosystem)

### When to Use Native

- You're writing Go code and want maximum performance
- You need the Event Bus for plugin-to-plugin communication
- You want the simplest debugging experience
- You're extending xbot's core functionality

---

## 3. Manifest Guide (plugin.json)

Every plugin must have a `plugin.json` in its root directory. This is the manifest that xbot reads during plugin discovery.

### Required Fields

```json
{
  "id": "com.example.my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "description": "What this plugin does",
  "runtime": "grpc"
}
```

| Field | Format | Description |
|-------|--------|-------------|
| `id` | `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` | Unique identifier (reverse DNS recommended) |
| `name` | Any string | Human-readable name |
| `version` | Semver (`MAJOR.MINOR.PATCH`) | Plugin version |
| `description` | Any string | Short summary |
| `runtime` | `native`, `grpc`, or `wasm` | Execution environment |

### Entry Point Configuration

For **gRPC** runtime:

```json
{
  "entry": "python3 main.py",
  "executable": "python3",
  "args": ["main.py"]
}
```

| Field | Description |
|-------|-------------|
| `entry` | Command string (space-split into command + args). Used as fallback if `executable` is empty. |
| `executable` | Command to run (recommended — no splitting, more secure). Takes precedence over `entry`. |
| `args` | Arguments passed to `executable`. Only used when `executable` is set. |

**Recommendation**: Use `executable` + `args` instead of `entry` for clarity and security.

For **native** runtime:

```json
{
  "entry": "Plugin"
}
```

The `entry` field is informational for native plugins — the Go plugin is registered via code.

### Activation Events

```json
{
  "activationEvents": ["onStart"]
}
```

| Event | Trigger | Use Case |
|-------|---------|----------|
| `onStart` | xbot startup | Always-active plugins (default if empty) |
| `onTool:<name>` | First call to the named tool | Lazy-loaded tools |
| `onHook:<event>` | First occurrence of a hook event | Event-driven plugins |
| `onCommand:<cmd>` | User types `/cmd` | Slash command plugins |

You can specify multiple activation events. The plugin activates on the **first** matching event.

### Contributions Declaration

```json
{
  "contributes": {
    "tools": [
      {
        "name": "my_tool",
        "description": "What it does",
        "inputSchema": { ... }
      }
    ],
    "hooks": [
      { "event": "PreToolUse", "matcher": "Shell*" }
    ],
    "contextEnrichers": [
      { "name": "my_enricher", "description": "What it enriches" }
    ],
    "commands": [
      { "name": "/deploy", "description": "Deploy to production" }
    ]
  }
}
```

### Dependencies

```json
{
  "dependencies": [
    { "id": "com.example.base-lib", "version": "^1.0.0" }
  ]
}
```

> **Note**: Dependencies are currently validated for format only — no version resolution or loading order is enforced yet.

### Full Example

```json
{
  "id": "com.example.code-reviewer",
  "name": "Code Reviewer",
  "version": "1.2.0",
  "description": "AI-powered code review",
  "author": "example.com",
  "homepage": "https://github.com/example/xbot-code-reviewer",
  "runtime": "grpc",
  "executable": "python3",
  "args": ["main.py"],
  "activationEvents": ["onTool:review_code", "onCommand:/review"],
  "permissions": ["tools.register", "hooks.subscribe", "storage.private", "network.outbound"],
  "dependencies": [
    { "id": "com.example.git-helper", "version": "^1.0.0" }
  ],
  "contributes": {
    "tools": [
      {
        "name": "review_code",
        "description": "Review code changes and suggest improvements",
        "inputSchema": {
          "type": "object",
          "properties": {
            "file_path": {"type": "string", "description": "Path to the file to review"}
          },
          "required": ["file_path"]
        }
      }
    ],
    "hooks": [
      { "event": "PostToolUse", "matcher": "Shell*git*" }
    ],
    "contextEnrichers": [
      { "name": "git_status", "description": "Inject current git status" }
    ],
    "commands": [
      { "name": "/review", "description": "Start code review" }
    ]
  }
}
```

---

## 4. Permissions

Plugins operate on a **least-privilege** model. You must declare every permission your plugin needs in `plugin.json`.

### Permission Reference

| Permission | Allows | Used By |
|-----------|--------|---------|
| `tools.register` | Register new tools for the LLM | `ctx.RegisterTool()` |
| `tools.call` | Call other registered tools | `ctx.CallTool()` |
| `hooks.subscribe` | Subscribe to lifecycle hooks | `ctx.OnPreToolUse()`, etc. |
| `context.enrich` | Inject content into system prompt | `ctx.EnrichContext()` |
| `storage.private` | Per-plugin isolated key-value storage | `ctx.Storage().Get/Set()` |
| `storage.shared` | Cross-plugin shared storage | `ctx.Storage().SharedGet/Set()` |
| `network.outbound` | Make outbound network requests | Plugin's own HTTP calls |
| `bus.plugin` | Enable plugin-to-plugin event bus | Required for bus access |
| `bus.read` | Read from message bus | `ctx.Subscribe()` |
| `bus.write` | Write to message bus | `ctx.Publish()` |

### Wildcard Permission

```json
{"permissions": ["*"]}
```

Grants all permissions. **Only use this for development or fully trusted plugins.**

### Permission Enforcement

Permissions are checked at the API boundary:

- Native plugins: `PluginContext` methods check permissions before execution
- gRPC plugins: The host enforces permissions — the plugin process cannot bypass them
- Missing permissions result in an error returned from the API call

### Minimum Permissions for Common Scenarios

| Scenario | Required Permissions |
|----------|---------------------|
| Register one tool | `tools.register` |
| Register a tool + hook | `tools.register`, `hooks.subscribe` |
| Full tool + hook + enricher | `tools.register`, `hooks.subscribe`, `context.enrich` |
| Tool with persistent storage | `tools.register`, `storage.private` |
| Plugin-to-plugin communication | `bus.plugin`, `bus.read`, `bus.write` |

---

## 5. Plugin Capabilities

### 5.1 Tools

Tools are the primary extension point — they add new capabilities that the LLM can invoke.

**When to use**: When you need the LLM to perform an action it can't do with built-in tools.

**How it works**:
1. Plugin declares tools in the `activate` response (gRPC) or via `ctx.RegisterTool()` (native)
2. xbot registers the tools with the LLM's function calling system
3. When the LLM decides to use the tool, xbot sends an `execute_tool` request

**Tool input**: Always a JSON string. The plugin must parse it internally.

**Tool output**: A string result. Return `{"error": "..."}` for errors (xbot marks it as `isError: true`).

### 5.2 Hooks

Hooks let you intercept and react to lifecycle events.

**When to use**:
- Security: Block dangerous operations (`PreToolUse` + `decision: "deny"`)
- Auditing: Log all tool usage (`PostToolUse`)
- Context injection: Add information before processing (`UserPromptSubmit`)

**Hook matcher patterns** (glob-style):
- `""` or `"*"` — match all tools
- `"Shell"` — exact match
- `"Shell*"` — prefix match (e.g., `Shell`, `ShellScript`)
- `"*git*"` — contains match (e.g., `GitStatus`, `RunGit`)

**Decision values**:
| Decision | Effect |
|----------|--------|
| `allow` | Let the operation proceed |
| `deny` | Block the operation (provide a reason in `message`) |
| `ask` | Prompt the user for confirmation |
| `defer` | Pass to the next handler |

When multiple hooks match, the **highest-priority decision wins**: `deny` > `ask` > `defer` > `allow`.

### 5.3 Context Enrichers

Enrichers inject dynamic content into the system prompt before each LLM call.

**When to use**:
- Inject current git status, project info, or environment variables
- Replace static SKILL.md files with dynamic content
- Provide real-time data to the LLM without explicit tool calls

**How it works**:
1. Plugin declares enrichers in the `activate` response
2. Before each LLM call, xbot invokes all registered enrichers
3. The returned string is injected as a section in the system prompt

**Performance note**: Enrichers are called on every LLM interaction. Keep them lightweight and fast.

### 5.4 Event Bus

The event bus provides pub/sub communication between plugins.

**When to use**: When multiple plugins need to coordinate or share state.

> **Important**: Event Bus is currently only available for **native (Go)** plugins. gRPC plugins cannot access the Event Bus in this version.

**Required permissions**: `bus.plugin` + `bus.read` (subscribe) + `bus.write` (publish).

---

## 6. gRPC Runtime Development

### Protocol Overview

gRPC plugins communicate via **NDJSON** (newline-delimited JSON) over stdin/stdout. See [PROTOCOL.md](./PROTOCOL.md) for the complete specification.

### Implementation Checklist

- [ ] Read JSON lines from stdin (one object per line)
- [ ] Parse the `method` field and dispatch to a handler
- [ ] For `activate`: Return tools, hooks, and enrichers declarations
- [ ] For `execute_tool`: Parse `input` (JSON string), execute logic, return `result` or `error`
- [ ] For `hook`: Return `hookResult` with a `decision`
- [ ] For `enrich`: Return enriched content as `result`
- [ ] For `deactivate`: Clean up and return empty response
- [ ] Write each response as a **single JSON line** to stdout
- [ ] **Flush stdout** after every response
- [ ] Use stderr for debug logging

### Language-Specific Tips

#### Python

```python
import sys, json

for line in sys.stdin:
    request = json.loads(line.strip())
    # ... handle request ...
    print(json.dumps(response), flush=True)  # IMPORTANT: flush!
```

#### Node.js

```javascript
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });

rl.on('line', (line) => {
    const request = JSON.parse(line);
    // ... handle request ...
    process.stdout.write(JSON.stringify(response) + '\n');  // explicit newline
});
```

### Key Constraints

| Constraint | Value |
|-----------|-------|
| Max message size | 1 MB per line |
| Call timeout | 30 seconds (process killed on timeout) |
| Concurrency | Strictly one request at a time |
| Shutdown | `SIGKILL` (no graceful shutdown signal) |
| Stderr | Passed through to xbot logs |

---

## 7. Native Runtime Development (Go)

### Implementing the Plugin Interface

```go
package myplugin

import "xbot/plugin"

type MyPlugin struct{}

func (p *MyPlugin) Manifest() plugin.PluginManifest {
    return plugin.PluginManifest{
        ID:      "com.example.my-plugin",
        Name:    "My Plugin",
        Version: "1.0.0",
        Runtime: plugin.RuntimeNative,
    }
}

func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Register tools, hooks, enrichers
    return nil
}

func (p *MyPlugin) Deactivate(ctx plugin.PluginContext) error {
    // Clean up
    return nil
}
```

### Using SimplePluginTool

The easiest way to register a tool:

```go
ctx.RegisterTool(&plugin.SimplePluginTool{
    Def: plugin.BuildToolDef("my_tool", "Does something useful",
        plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
    ),
    ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
        return plugin.NewToolResult("Done!"), nil
    },
})
```

### Using ToolCallContext (V2)

For session metadata (session ID, channel, user ID):

```go
ctx.RegisterTool(&plugin.SimplePluginTool{
    Def: plugin.BuildToolDef("my_tool", "Does something useful",
        plugin.ToolParamDef{Name: "input", Type: "string", Description: "Input param"},
    ),
    ExecV2Fn: func(ctx *plugin.ToolCallContext, input string) (*plugin.ToolResult, error) {
        log.Printf("Session: %s, Channel: %s", ctx.SessionID, ctx.Channel)
        return plugin.NewToolResult("Done!"), nil
    },
})
```

### Registration

```go
pm := plugin.NewPluginManager(...)
pm.Register(myPlugin)  // Register native plugin
pm.Discover(...)       // Discover gRPC plugins
pm.ActivateAll(ctx)    // Activate all onStart plugins
```

---

## 8. Storage

### Private Storage

Each plugin gets its own isolated key-value storage.

**Location**: `~/.xbot/plugins/<id>/data/storage.json`

**API** (native plugins only):
```go
storage := ctx.Storage()
storage.Set("key", "value")
val, _ := storage.Get("key")
storage.Delete("key")
keys := storage.Keys()
storage.Clear()
```

**Required permission**: `storage.private`

> **Note**: gRPC plugins cannot access storage through the protocol directly. Store state internally or use your own persistence mechanism.

### Shared Storage

Cross-plugin shared storage is planned. The permission (`storage.shared`) is defined but the API is not yet fully implemented.

---

## 9. Debugging

### Plugin Status

Check plugin status via xbot's plugin management:

```bash
# In xbot CLI
/plugin list          # List all plugins and their states
/plugin info <id>     # Show detailed plugin info
```

### gRPC Plugin Debugging

1. **Manual protocol test**: Pipe JSON directly into your plugin
   ```bash
   echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 main.py
   ```

2. **stderr logging**: Print to stderr for debug output
   ```python
   print(f"[debug] method={method}", file=sys.stderr)
   ```

3. **Common issues**:
   - EOF → plugin crashed, check stderr
   - Empty response → forgot to write stdout
   - Timeout → took >30s, optimize or split work
   - Invalid JSON → multi-line output, ensure single-line

### Native Plugin Debugging

1. Use `ctx.Logger()` for structured logging:
   ```go
   ctx.Logger().Info("Tool executed", "tool", "my_tool")
   ```

2. Use `pm.HealthCheck(ctx)` to verify plugin health

3. Use `pm.Metrics()` to check registration counts

### Hot Reload

Reload a plugin without restarting xbot:

```go
// Reload a single plugin
pm.Reload(ctx, "com.example.my-plugin")

// Reload all plugins
pm.ReloadAll(ctx)
```

> After reload, call `WireAll()` to re-register tools and hooks with xbot's subsystems.

---

## 10. Deployment

### Directory Structure

```
~/.xbot/plugins/
├── my-plugin/
│   ├── plugin.json       # Required: plugin manifest
│   ├── main.py           # gRPC: plugin entry point
│   └── (other files)     # Any supporting files
├── another-plugin/
│   ├── plugin.json
│   └── (compiled binary) # Native: Go plugin
└── ...
```

### Installation Methods

1. **Manual**: Copy plugin directory to `~/.xbot/plugins/`
2. **Future**: Plugin Marketplace with `xbot plugin install <name>`

### Version Management

- Use semver (`MAJOR.MINOR.PATCH`) in `plugin.json`
- Dependencies are declared but not yet enforced
- Hot reload picks up manifest changes automatically

---

## 11. Best Practices

### Design Principles

1. **Least privilege**: Only declare permissions you actually need
2. **Idempotent activation**: `Activate()` should be safe to call multiple times
3. **Fast deactivation**: Clean up quickly — the process may be killed without warning
4. **Defensive parsing**: Tool input may be malformed JSON — always handle errors
5. **Lightweight enrichers**: Called on every LLM interaction — keep them fast

### Error Handling

| Scenario | Do This |
|----------|---------|
| Tool encounters an error | Return `{"error": "description"}` — don't crash |
| Tool receives bad input | Return `{"error": "Invalid input: ..."}` — don't crash |
| Hook wants to block | Return `{"hookResult": {"decision": "deny", "message": "reason"}}` |
| Hook encounters an error | Return `{"hookResult": {"decision": "allow"}}` — don't crash |
| Enricher fails | Return `{"error": "..."}` — xbot logs it and continues |
| Unexpected exception | Log to stderr and return `{"error": "..."}` — don't crash |

### Performance

| Runtime | Typical overhead | Recommendation |
|---------|-----------------|----------------|
| Native | ~μs | Use for hot-path tools called frequently |
| gRPC | ~1-5ms | Acceptable for most tools; avoid in tight loops |
| Enrichers | ~1-5ms each | Keep light; called on every LLM interaction |

---

## 12. FAQ

### Q1: Why is it called "gRPC" if it uses JSON/stdio?

**A**: The runtime was named `grpc` with the intention of migrating to gRPC/protobuf in a future iteration. For Phase 1, JSON/stdio was chosen for simplicity and language-agnostic ease of implementation. The name is historical — the actual transport is NDJSON over pipes.

### Q2: Can gRPC plugins use the Event Bus?

**A**: Not currently. The Event Bus is an in-process mechanism. gRPC plugins run in external processes and cannot directly subscribe or publish. A future proxy mechanism may enable this.

### Q3: What happens if my plugin crashes?

**A**: For gRPC plugins: the process exits, and subsequent calls return an EOF error. Use `pm.Reload()` to restart the plugin. For native plugins: panics are recovered by xbot's error handling (varies by call site).

### Q4: Can I add tools after activation?

**A**: No. The `activate` response is a one-time declaration of all capabilities. To add new tools, modify your code and reload the plugin.

### Q5: Is `inputSchema` required for tools?

**A**: No, but it's strongly recommended. Without it, the LLM doesn't know what parameters the tool expects, which often leads to incorrect calls.

### Q6: Can I change the 30-second timeout?

**A**: The timeout is currently hardcoded. If your tool needs more time, consider breaking it into smaller operations or submitting a feature request.

### Q7: Do dependencies actually work?

**A**: Dependencies are validated for format only (correct ID and version format). No actual version resolution, installation, or loading-order enforcement exists yet.

### Q8: How do I persist plugin configuration?

**A**: Use `storage.private` (native plugins) to store key-value pairs. For gRPC plugins, implement your own file-based configuration in the plugin directory.

### Q9: Can I use third-party libraries in my plugin?

**A**: Yes. For gRPC plugins, install dependencies as usual (e.g., `pip install`, `npm install`). For native plugins, add Go imports normally. The plugin process has full access to its language's ecosystem.

### Q10: How do I test my gRPC plugin locally?

**A**: Pipe JSON requests directly into your plugin:
```bash
echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 main.py
echo '{"method":"execute_tool","params":{"toolName":"my_tool","input":"{}"}}' | python3 main.py
```
See [PROTOCOL.md](./PROTOCOL.md) for complete testing instructions.

---

## Document Navigation

| Document | Purpose |
|----------|---------|
| **GUIDE.md** (this file) | Tutorials, decision guidance, best practices |
| [README.md](./README.md) | API quick reference, code examples |
| [PROTOCOL.md](./PROTOCOL.md) | JSON/stdio protocol specification |
| [DESIGN.md](./DESIGN.md) | Architecture and design decisions |
| [examples/hello-world/](./examples/hello-world/) | Go native plugin example |
| [examples/grpc-python/](./examples/grpc-python/) | Python gRPC plugin example |
