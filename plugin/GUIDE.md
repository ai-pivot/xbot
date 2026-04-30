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
- [8. SDK Helpers](#8-sdk-helpers)
- [9. Middleware System](#9-middleware-system)
- [10. Plugin Configuration](#10-plugin-configuration)
- [11. Rate Limiting & Quota](#11-rate-limiting--quota)
- [12. Audit Trail](#12-audit-trail)
- [13. Storage](#13-storage)
- [14. Debugging](#14-debugging)
- [15. Deployment](#15-deployment)
- [16. Best Practices](#16-best-practices)
- [17. FAQ](#17-faq)

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
| `tools.call` | Call other registered tools | *(reserved for future use)* |
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

## 8. SDK Helpers

The `sdk.go` package provides convenience functions that reduce boilerplate for common plugin tasks. These helpers are only available for **native (Go)** plugins.

### Quick Tool Creation

#### ToolFromFunc — Simplest possible tool

`ToolFromFunc` creates a tool from a function that receives a plain string and returns a plain string. No struct definitions, no `ToolResult` wrapping — just the logic.

```go
// One function = one tool
tool := plugin.ToolFromFunc("greet", "Greet a user",
    func(ctx context.Context, input string) (string, error) {
        return "Hello, " + input, nil
    },
)
ctx.RegisterTool(tool)
```

This is equivalent to creating a `SimplePluginTool` manually, but with less code.

#### ToolFromJSONFunc — Structured input/output

When your tool needs typed parameters and returns structured data, use `ToolFromJSONFunc`. It accepts `json.RawMessage` as input and auto-marshals the return value to JSON.

```go
searchTool := plugin.ToolFromJSONFunc("search", "Search items",
    []plugin.ToolParamDef{
        {Name: "query", Type: "string", Description: "Search query", Required: true},
        {Name: "limit", Type: "number", Description: "Max results", Required: false},
    },
    func(ctx context.Context, input json.RawMessage) (any, error) {
        var params struct {
            Query string `json:"query"`
            Limit int    `json:"limit"`
        }
        if err := json.Unmarshal(input, &params); err != nil {
            return nil, err
        }
        // ... perform search ...
        return map[string]any{
            "results": []string{"item1", "item2"},
            "count":   2,
        }, nil
    },
)
ctx.RegisterTool(searchTool)
```

**What happens internally**: `ToolFromJSONFunc` calls `BuildToolDef` to construct the parameter schema from `ToolParamDef` slice, wraps the function to parse raw JSON input, and auto-marshals the return value.

### Quick Manifest Builder

`QuickManifest` creates a valid `PluginManifest` using a fluent option pattern. Instead of constructing the struct field by field, you compose it from declarative options:

```go
manifest := plugin.QuickManifest(
    "com.example.my-plugin",
    "My Plugin",
    "1.0.0",
    "A plugin built with SDK helpers",
    plugin.WithPermissions("tools.register", "storage.private"),
    plugin.WithActivationEvents("onStart"),
    plugin.WithTools(plugin.ToolContribution{
        Name: "my_tool", Description: "Does something",
    }),
    plugin.WithHooks(plugin.HookContribution{
        Event: "PostToolUse", Matcher: "*",
    }),
    plugin.WithEnrichers(plugin.EnricherContribution{
        Name: "project_info", Description: "Current project context",
    }),
    plugin.WithRuntime(plugin.RuntimeNative),
)
```

**Available options**:

| Option | Description |
|--------|-------------|
| `WithPermissions(perms...)` | Add permission strings |
| `WithActivationEvents(events...)` | Set activation events (replaces default `onStart`) |
| `WithRuntime(rt)` | Set the runtime type |
| `WithTools(tools...)` | Add tool contributions |
| `WithHooks(hooks...)` | Add hook contributions |
| `WithEnrichers(enrichers...)` | Add context enricher contributions |

### Hook Factory Functions

One-line hooks for common patterns — no need to write the full `HookHandler` closure:

```go
// Deny — always block with a message
ctx.OnPreToolUse("Shell*rm*", plugin.DenyHook("Dangerous rm commands are blocked"))

// Log — record the event and allow
ctx.OnPreToolUse("*", plugin.LogHook(logger, "Tool call observed"))

// Allow — always allow (useful as a no-op placeholder)
ctx.OnPostToolUse("*", plugin.AllowHook())
```

**When to use**:
- `DenyHook`: Security policies — block dangerous operations
- `LogHook`: Audit logging — record every matching event
- `AllowHook`: Default passthrough — useful in conditional hook chains

### Enricher Factory Functions

One-line context enrichers for static or file-based content injection:

```go
// StaticEnricher — always returns the same string
ctx.EnrichContext("rules", plugin.StaticEnricher("Always use tabs for indentation"))

// FileEnricher — reads content from a file on every call
ctx.EnrichContext("project-info", plugin.FileEnricher("./PROJECT.md"))
```

**Note**: `FileEnricher` reads from disk on every LLM interaction. For files that change infrequently, consider caching the content in your plugin and using `StaticEnricher` with the cached value.

### MustActivate

For plugins that must succeed at startup (fail-fast behavior):

```go
func init() {
    ctx := /* obtain PluginContext */
    plugin.MustActivate(myPlugin, ctx)  // panics if activation fails
}
```

Use this in `init()` functions or during app bootstrap where a missing plugin should halt the entire process.

---

## 9. Middleware System

### Concept

The middleware chain follows the classic **onion model** (same pattern as Gin/Chi HTTP frameworks). Each middleware wraps the next, forming nested layers:

```
Request → Logging → Recovery → Timeout → Retry → [Tool Execute]
                                                     ← Retry ← Timeout ← Recovery ← Logging ← Response
```

Each middleware receives `(ctx, toolName, input, next)` and **must** call `next()` to continue the chain. Not calling `next()` short-circuits execution — useful for early rejection.

### Creating a Middleware Chain

```go
chain := plugin.NewMiddlewareChain(
    plugin.LoggingMiddleware(logger),           // Log tool call details
    plugin.RecoveryMiddleware(logger),          // Recover from panics
    plugin.TimeoutMiddleware(10 * time.Second), // Enforce timeout
    plugin.RetryMiddleware(3),                  // Retry on error
)

// Execute a tool call through the chain
result, err := chain.Execute(ctx, "my_tool", input,
    func(ctx context.Context, toolName, input string) (*plugin.ToolResult, error) {
        // Final handler — the actual tool execution
        return myTool.Execute(ctx, input)
    },
)
```

You can also append middleware after construction:

```go
chain.Use(myCustomMiddleware)
```

### Built-in Middleware

#### LoggingMiddleware

Logs tool call start, completion, and failure. Pure observer — does not modify results.

```go
plugin.LoggingMiddleware(logger)
```

Logs: tool name, input length, duration, and error (if any).

#### RecoveryMiddleware

Recovers from panics inside tool execution and converts them to error `ToolResult`. Includes the stack trace in the log.

```go
plugin.RecoveryMiddleware(logger)
```

Prevents a panicking tool from crashing the entire process.

#### TimeoutMiddleware

Enforces a maximum execution duration using `context.WithTimeout`. Returns an error `ToolResult` if the deadline is exceeded.

```go
plugin.TimeoutMiddleware(10 * time.Second)
```

Passing `0` or negative creates a no-op middleware.

#### RetryMiddleware

Retries tool execution on Go `error` (not `ToolResult.IsError`). Uses fixed 100ms backoff between attempts.

```go
plugin.RetryMiddleware(3)  // 1 initial + 3 retries = 4 total attempts
```

Stops retrying if the context is cancelled. `maxRetries <= 0` creates a no-op middleware.

### Custom Middleware

Write your own middleware by implementing the `PluginMiddleware` function signature:

```go
func MetricsMiddleware(metrics *MetricsCollector) plugin.PluginMiddleware {
    return func(ctx context.Context, toolName, input string, next plugin.PluginMiddlewareNext) (*plugin.ToolResult, error) {
        start := time.Now()
        result, err := next(ctx, toolName, input)  // call the next middleware
        duration := time.Since(start)

        metrics.Record(toolName, duration, err)
        return result, err
    }
}

// Register during activation
ctx.UseMiddleware(MetricsMiddleware(myMetrics))
```

**Key rules**:
- Always call `next()` unless you intend to short-circuit
- Return the same `(result, err)` from `next()` unless you need to transform them
- Middleware registered via `ctx.UseMiddleware()` applies to **all** tool executions from this plugin

### Execution Order

Middlewares execute in **registration order** — the first registered is the outermost layer. For `[Logging, Recovery, Timeout, Retry]`:

```
1. Logging.before   → logs "tool call started"
2. Recovery.before  → sets up panic recovery
3. Timeout.before   → starts countdown
4. Retry.before     → begins attempt loop
5. [Tool Execute]   → actual tool logic
6. Retry.after      → retries if error
7. Timeout.after    → checks deadline
8. Recovery.after   → cleans up
9. Logging.after    → logs "tool call completed"
```

### Integration with PluginToolBridge

The middleware chain is wired automatically when plugins are integrated via `PluginToolBridge`. Rate limiting and quota checks run **before** the middleware chain (host-level enforcement that cannot be bypassed by middleware):

```
Rate Limit Check → Quota Check → Middleware Chain → Tool Execute
```

---

## 10. Plugin Configuration

The plugin configuration system allows plugins to declare user-configurable settings with defaults, and users to override those settings without modifying plugin code.

### Declaring Configuration Schema

Add a `configuration` section to your `plugin.json` under `contributes`:

```json
{
  "contributes": {
    "configuration": {
      "title": "My Plugin Settings",
      "properties": {
        "api_endpoint": {
          "type": "string",
          "default": "https://api.example.com",
          "description": "API endpoint URL"
        },
        "max_retries": {
          "type": "number",
          "default": 3,
          "description": "Maximum retry attempts"
        },
        "debug_mode": {
          "type": "boolean",
          "default": false,
          "description": "Enable debug logging"
        }
      }
    }
  }
}
```

**Supported types**: `"string"`, `"number"`, `"boolean"`.

Each property has:
| Field | Required | Description |
|-------|----------|-------------|
| `type` | Yes | JSON Schema type |
| `default` | No | Default value when no user config exists |
| `description` | Yes | Human-readable explanation |

### Reading Configuration in Code

In native plugins, use `Config()` on the `PluginContext`. It merges manifest defaults with user overrides:

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    config, err := ctx.Config()
    if err != nil {
        return fmt.Errorf("load config: %w", err)
    }

    endpoint, _ := config["api_endpoint"].(string)       // "https://api.example.com"
    retries, _ := config["max_retries"].(float64)        // 3.0 (JSON numbers → float64)
    debug, _ := config["debug_mode"].(bool)              // false

    // Use config values...
    return nil
}
```

**Type assertion note**: JSON unmarshaling produces `float64` for numbers, `string` for strings, and `bool` for booleans. Always type-assert accordingly.

### Writing Configuration

Use `SetConfig` to persist individual configuration values at runtime:

```go
err := ctx.SetConfig("debug_mode", true)
if err != nil {
    return err
}
```

`SetConfig` performs an atomic load-modify-save operation protected by a write lock, preventing concurrent updates from overwriting each other.

### Configuration File Location

User configuration is stored at:

```
~/.xbot/plugins/<id>/config.json
```

Users can manually edit this file. Example content:

```json
{
  "api_endpoint": "https://custom-api.example.com",
  "debug_mode": true
}
```

**Properties**:
- Independent of the plugin installation directory
- Atomic writes (temp file + rename) to prevent corruption
- In-memory cache with automatic invalidation — `Config()` always reads fresh data
- Missing file → only manifest defaults are returned

### How Defaults Work

When `Config()` is called:
1. Manifest defaults are extracted via `GetDefaultConfig()`
2. User config from `config.json` is loaded
3. User values **override** defaults for matching keys
4. The merged result is returned

This means plugins always have sensible defaults even when no user config file exists.

---

## 11. Rate Limiting & Quota

xbot provides host-level enforcement of rate limits and daily quotas for plugin tool calls. These checks run **before** any middleware, making them impossible to bypass.

### Rate Limiting (Sliding Window)

`PluginRateLimiter` enforces per-plugin rate limits using a sliding window counter:

```go
// Create a rate limiter: allow 100 calls per minute for "com.example.api-plugin"
rl := plugin.NewPluginRateLimiter(map[string]plugin.RateLimit{
    "com.example.api-plugin": {MaxCalls: 100, Window: time.Minute},
})

// Check if a call is allowed
if !rl.Allow("com.example.api-plugin") {
    // Rate limit exceeded
}

// Query remaining calls
remaining := rl.Remaining("com.example.api-plugin")  // -1 if no limit configured

// Dynamically update limits
rl.SetRateLimit("com.example.api-plugin", plugin.RateLimit{MaxCalls: 200, Window: time.Minute})

// Reset counters
rl.Reset("com.example.api-plugin")
```

**Behavior**:
- Plugins without configured limits are **unlimited** (`Allow()` always returns `true`)
- Uses a true sliding window (not fixed window) for smoother enforcement
- Thread-safe (mutex-protected)

### Daily Quotas

`PluginQuotaManager` enforces daily resource limits:

```go
// Create a quota manager
qm := plugin.NewPluginQuotaManager(map[string]plugin.PluginQuota{
    "com.example.api-plugin": {
        MaxToolCallsPerDay: 1000,
        MaxStorageMB:       50,
    },
})

// Check tool call budget
allowed, remaining := qm.CheckToolCall("com.example.api-plugin")
if !allowed {
    // Daily quota exceeded
}

// Check storage quota
ok, usedBytes := qm.CheckStorage("com.example.api-plugin")
if !ok {
    // Storage quota exceeded
}

// Query current usage
toolCalls, storageBytes := qm.GetQuotaUsage("com.example.api-plugin")
```

**Quota features**:
| Feature | Description |
|---------|-------------|
| `MaxToolCallsPerDay` | Maximum tool executions per UTC day (lazy reset) |
| `MaxStorageMB` | Maximum storage size in MB (checked against actual key values) |
| Daily reset | Counters reset automatically at UTC midnight |
| Dynamic config | `SetQuota()` updates limits at runtime |

### Integration with PluginToolBridge

Rate limiting and quotas are enforced at the bridge level via `NewPluginToolBridgeWithLimits`:

```go
bridge := plugin.NewPluginToolBridgeWithLimits(adapter, pluginID, rateLimiter, quotaManager)
```

When a tool call comes through the bridge:
1. **Rate limit check** — if exceeded, returns error immediately
2. **Quota check** — if daily budget exhausted, returns error immediately
3. **Middleware chain** — runs only if both checks pass
4. **Tool execution** — runs last

This ensures host-level resource protection is always enforced regardless of plugin middleware configuration.

---

## 12. Audit Trail

The `AuditLogger` provides append-only JSONL audit logging for plugin operations. It automatically records key lifecycle events.

### Creating an Audit Logger

```go
al, err := plugin.NewAuditLogger("/path/to/audit.jsonl")
if err != nil {
    return err
}
defer al.Close()
```

The parent directory is created automatically if it doesn't exist. The file is opened with `O_APPEND|O_CREATE|O_WRONLY` for atomic appends.

### Recording Events

```go
al.Log(plugin.AuditEntry{
    PluginID: "com.example.my-plugin",
    Action:   plugin.AuditActivate,
    Details:  map[string]any{"version": "1.0.0"},
})

al.Log(plugin.AuditEntry{
    PluginID: "com.example.my-plugin",
    Action:   plugin.AuditDeactivate,
    Error:    "context cancelled",
})
```

**Audit action constants**:

| Constant | Value | When to use |
|----------|-------|-------------|
| `AuditActivate` | `"activate"` | Plugin activated |
| `AuditDeactivate` | `"deactivate"` | Plugin deactivated |
| `AuditInstall` | `"install"` | Plugin installed |
| `AuditUninstall` | `"uninstall"` | Plugin uninstalled |
| `AuditReload` | `"reload"` | Plugin reloaded |
| `AuditDisable` | `"disable"` | Plugin disabled |

**Note**: If `Timestamp` is zero, it is automatically set to `time.Now()`. Write errors are silently ignored — audit logging must not block the caller.

### Querying the Audit Log

```go
// All entries for a specific plugin
entries := al.Query(plugin.AuditFilter{
    PluginID: "com.example.my-plugin",
})

// Entries in a time range
entries := al.Query(plugin.AuditFilter{
    From: time.Now().Add(-24 * time.Hour),
    To:   time.Now(),
})

// Combined filter
entries := al.Query(plugin.AuditFilter{
    PluginID: "com.example.my-plugin",
    From:     startTime,
    To:       endTime,
})
```

Results are sorted by `Timestamp` ascending. Zero-value filter fields mean "no filter on that field".

### Clearing the Log

```go
al.Clear()  // Truncates the file; safe for concurrent use with Log
```

### Log Format

Each line is a JSON object (JSONL format):

```jsonl
{"timestamp":"2025-01-15T10:30:00Z","plugin_id":"com.example.my-plugin","action":"activate","details":{"version":"1.0.0"}}
{"timestamp":"2025-01-15T10:35:00Z","plugin_id":"com.example.my-plugin","action":"deactivate","error":"context cancelled"}
```

---

## 13. Storage

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

**Helper methods** for typed access:
```go
// Store JSON objects
ctx.StorageJSON("config", map[string]any{"theme": "dark"})

// Retrieve typed values
count, ok := ctx.StorageInt("call_count")
enabled, ok := ctx.StorageBool("enabled")

// Retrieve JSON into a struct
var config MyConfig
err := ctx.StorageGetJSON("config", &config)
```

**Required permission**: `storage.private`

> **Note**: gRPC plugins cannot access storage through the protocol directly. Store state internally or use your own persistence mechanism.

### Shared Storage

Cross-plugin shared storage is planned. The permission (`storage.shared`) is defined but the API is not yet fully implemented.

---

## 14. Debugging

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

## 15. Deployment

### Directory Structure

```
~/.xbot/plugins/
├── my-plugin/
│   ├── plugin.json       # Required: plugin manifest
│   ├── main.py           # gRPC: plugin entry point
│   ├── config.json       # Optional: user configuration overrides
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

## 16. Best Practices

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

### Middleware Tips

1. **Order matters**: Register `RecoveryMiddleware` early (outer layer) so it catches panics from all inner middleware
2. **Keep it fast**: Middleware wraps every tool call — avoid heavy computation
3. **Always call next()**: Unless you intentionally short-circuit, always delegate to the next handler
4. **Use built-in middleware first**: `LoggingMiddleware`, `RecoveryMiddleware`, `TimeoutMiddleware`, and `RetryMiddleware` cover most needs

---

## 17. FAQ

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

**A**: Use the configuration system (Section 10). Declare defaults in `plugin.json` under `contributes.configuration`, and use `ctx.Config()` / `ctx.SetConfig()` in native plugins. For gRPC plugins, implement your own file-based configuration in the plugin directory.

### Q9: Can I use third-party libraries in my plugin?

**A**: Yes. For gRPC plugins, install dependencies as usual (e.g., `pip install`, `npm install`). For native plugins, add Go imports normally. The plugin process has full access to its language's ecosystem.

### Q10: How do I test my gRPC plugin locally?

**A**: Pipe JSON requests directly into your plugin:
```bash
echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 main.py
echo '{"method":"execute_tool","params":{"toolName":"my_tool","input":"{}"}}' | python3 main.py
```
See [PROTOCOL.md](./PROTOCOL.md) for complete testing instructions.

### Q11: When should I use SDK helpers vs. manual construction?

**A**: Use SDK helpers (`ToolFromFunc`, `QuickManifest`, `DenyHook`, etc.) for simple cases — they reduce boilerplate and are easier to read. Switch to manual construction when you need `ExecV2Fn` (for `ToolCallContext`), custom middleware integration, or fine-grained control over the `ToolDef`.

### Q12: Can middleware bypass rate limits?

**A**: No. Rate limiting and quota checks run at the `PluginToolBridge` level, **before** the middleware chain. This is by design — host-level resource limits cannot be overridden by plugin middleware.

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
