---
title: "Quick Start: Go Plugin"
weight: 3
---

Build a native Go plugin with two tools, a hook, and a context enricher. This recipe is based on the real `hello-world` example at `plugin/examples/hello-world/hello.go`.

Native plugins run **in-process**: they import `xbot/plugin`, implement the `Plugin` interface, and are compiled into the xbot binary. Use them when you need tight integration and no process overhead.

## Step 1: Implement the plugin

```go
package helloworld

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"xbot/plugin"
)

// HelloWorldPlugin implements plugin.Plugin.
type HelloWorldPlugin struct {
	callCount atomic.Int64
	startTime time.Time
}

// Manifest returns the plugin metadata.
func (p *HelloWorldPlugin) Manifest() plugin.PluginManifest {
	return plugin.PluginManifest{
		ID:               "xbot.hello-world",
		Name:             "Hello World",
		Version:          "1.0.0",
		Description:      "A simple example plugin",
		Runtime:          plugin.RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register", "hooks.subscribe", "context.enrich", "storage.private"},
	}
}

// Activate initializes the plugin and registers all capabilities.
func (p *HelloWorldPlugin) Activate(pctx plugin.PluginContext) error {
	p.startTime = time.Now()

	// 1. Register a tool with a typed parameter
	if err := pctx.RegisterTool(&plugin.SimplePluginTool{
		Def: plugin.BuildToolDef("hello", "Greet someone by name.",
			plugin.ToolParamDef{Name: "name", Type: "string", Description: "The person to greet"},
		),
		ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
			name, err := plugin.ParseToolInputString(input, "name")
			if err != nil {
				name = "World"
			}
			p.callCount.Add(1)
			return plugin.NewToolResult(fmt.Sprintf("Hello, %s! 👋 Welcome to xbot plugins.", name)), nil
		},
	}); err != nil {
		return fmt.Errorf("register hello tool: %w", err)
	}

	// 2. Register a PostToolUse hook that persists a counter to storage
	pluginCtx := pctx // capture for the closure
	if err := pctx.OnPostToolUse("", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
		storage := pluginCtx.Storage()
		count, _ := storage.Get("tool_call_count")
		// ... increment and storage.Set(...)
		return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
	}); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}

	// 3. Register a context enricher that injects status into the system prompt
	if err := pctx.EnrichContext("hello_status", func(ctx context.Context) (string, error) {
		return fmt.Sprintf("Hello World plugin active (tool calls served: %d)", p.callCount.Load()), nil
	}); err != nil {
		return fmt.Errorf("register enricher: %w", err)
	}
	return nil
}

func (p *HelloWorldPlugin) Deactivate(ctx plugin.PluginContext) error {
	ctx.Logger().Info("Goodbye from Hello World plugin!")
	return nil
}
```

## Step 2: Register it with the runtime

Native plugins are registered on the `NativeRuntime` — usually from `init()` in the plugin's package:

```go
import "xbot/plugin"

func init() {
	plugin.NativeRuntimeRegistry.RegisterPlugin(&HelloWorldPlugin{})
}
```

The plugin directory still needs a `plugin.json` so discovery can find and validate it (`plugin/manifest.go:LoadManifest`):

```json
{
  "id": "xbot.hello-world",
  "name": "Hello World",
  "version": "1.0.1",
  "runtime": "native",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich", "storage.private"],
  "contributes": {
    "tools": [
      { "name": "hello", "description": "Greet someone by name." },
      { "name": "ping", "description": "Ping-pong connectivity test." }
    ],
    "hooks": [
      { "event": "PostToolUse", "matcher": "" }
    ],
    "contextEnrichers": [
      { "name": "hello_status", "description": "Shows Hello World plugin status" }
    ]
  }
}
```

## Step 3: Build and restart

`plugin.NativeRuntime.Create` (`plugin/runtime.go:38`) returns the pre-registered instance when the manifest ID matches. Nothing else to do — restart xbot and ask the agent to call the `hello` tool.

## How it works

- **`Plugin` interface** (`plugin/plugin.go:26`) — three methods: `Manifest()`, `Activate(ctx)`, `Deactivate(ctx)`. `Activate` must be idempotent; it receives the `PluginContext` through which all capability registration happens.
- **`BuildToolDef(name, desc, params...)`** builds a `ToolDef` with JSON-Schema generation (`ToJSONSchema`).
- **`ParseToolInputString(input, "name")`** extracts a parameter from the raw tool input JSON.
- **`OnPostToolUse("", handler)`** — the matcher `""` means "all tools". Use `"Shell*"` for wildcard matching.
- **`Storage()`** gives access to the plugin's private key-value store (see [Storage](../storage/)).
- **`EnrichContext`** injects dynamic content into the system prompt each turn — the enricher closure is called by the agent loop.

## SDK shortcuts

`plugin/sdk.go` provides fluent helpers that shrink the boilerplate:

```go
m := plugin.QuickManifest("xbot.demo", "Demo", "0.1.0", "A demo",
	plugin.WithPermissions(plugin.PermToolsRegister),
	plugin.WithTools(plugin.ToolContribution{Name: "demo_tool", Description: "..."}),
)

tool := plugin.ToolFromFunc("double", "Doubles a number",
	func(ctx context.Context, input string) (string, error) { return input + input, nil })

pctx.RegisterTool(tool)
```

See [Go Plugins](../go-plugins/) for the full guide.
