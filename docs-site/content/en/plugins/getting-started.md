---
title: "Getting Started with Plugins"
weight: 1
---

Create your first xbot plugin in 5 minutes. This guide walks through the three simplest plugin types.

## Prerequisites

- xbot installed and running
- Basic familiarity with the terminal
- For Go plugins: Go 1.22+

## Plugin Directory Structure

All plugins live in `~/.xbot/plugins/<plugin-id>/`. The minimal structure is:

```
~/.xbot/plugins/my-plugin/
├── plugin.json    # Required: plugin manifest
└── main.sh        # Entry point (varies by runtime)
```

## Type 1: Script Plugin (Easiest)

Script plugins run external scripts (bash, Python, Node, anything). Perfect for widgets and simple tools.

### Step 1: Create the manifest

```json
{
  "id": "hello-widget",
  "name": "Hello Widget",
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

### Step 2: Create the script

```bash
#!/bin/bash
# main.sh — runs on activation and on widget refresh
echo "Hello, xbot!"
```

### Step 3: Install and activate

```bash
mkdir -p ~/.xbot/plugins/hello-widget
# Copy plugin.json and main.sh into the directory
# Restart xbot or run: /plugin reload-all
```

The greeting appears in the CLI status bar. Script plugins automatically refresh on hooks and directory changes.

## Type 2: Go Native Plugin

Native Go plugins compile into the xbot binary. Best for tools, hooks, and complex logic.

### Step 1: Implement the Plugin interface

```go
package main

import (
    "context"
    "fmt"
    "xbot/plugin"
)

type HelloPlugin struct {
    manifest plugin.PluginManifest
}

func (p *HelloPlugin) Manifest() plugin.PluginManifest {
    return p.manifest
}

func (p *HelloPlugin) Activate(ctx plugin.PluginContext) error {
    // Register a tool
    tool := plugin.ToolFromFunc("hello", "Say hello", func(ctx context.Context, input string) (string, error) {
        return "Hello, World!", nil
    })
    return ctx.RegisterTool(tool)
}

func (p *HelloPlugin) Deactivate(ctx plugin.PluginContext) error {
    return nil
}
```

### Step 2: Register in xbot

Native plugins are registered via `PluginManager.Register()` during xbot initialization. See the [Go Native Plugin Guide](../cookbook/go-native-plugin/) for details.

## Type 3: Stdio Plugin (Any Language)

Stdio plugins communicate via JSON-RPC over stdin/stdout. Use any language.

### Step 1: Create the manifest

```json
{
  "id": "python-hello",
  "name": "Python Hello",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register"]
}
```

### Step 2: Implement the protocol

```python
#!/usr/bin/env python3
import json
import sys

def handle_request(req):
    if req["method"] == "activate":
        return {"result": {"tools": [{"name": "hello", "description": "Say hello"}]}}
    elif req["method"] == "execute_tool":
        return {"result": {"output": "Hello from Python!"}}
    return {"error": "unknown method"}

for line in sys.stdin:
    req = json.loads(line)
    resp = handle_request(req)
    sys.stdout.write(json.dumps(resp) + "\n")
    sys.stdout.flush()
```

See the [Stdio Runtime Protocol](../stdio-runtime/) for the complete JSON-RPC specification.

## Next Steps

- [Architecture](../architecture/) — Understand how plugins work under the hood
- [Plugin Manifest](../manifest/) — Complete manifest specification
- [PluginContext API](../plugin-context/) — The plugin's gateway to xbot
- [Cookbook](../cookbook/) — Step-by-step development guides
