---
title: "Quick Start: Stdio Plugin"
weight: 4
---

Build a plugin in **any language** that communicates with xbot over newline-delimited JSON (NDJSON) on stdin/stdout. This recipe is based on the real `grpc-python` example at `plugin/examples/grpc-python/main.py` — Python standard library only, no dependencies.

The stdio runtime (`runtime: "stdio"`, `"grpc"` accepted for backward compat) spawns your process and speaks a request/response protocol defined in `plugin/protocol/protocol.go`. xbot sends one JSON object per line; you respond with one JSON object per line, flushing after every response.

## Step 1: Implement the protocol in Python

```python
#!/usr/bin/env python3
import sys, json
from datetime import datetime

def handle_activate(params):
    """Declare capabilities: tools, hooks, enrichers."""
    return {
        "tools": [
            {
                "name": "python_greet",
                "description": "Greet someone by name.",
                "parameters": [
                    {"name": "name", "type": "string", "description": "The person to greet", "required": True}
                ],
                "inputSchema": {
                    "type": "object",
                    "properties": {"name": {"type": "string", "description": "The person to greet"}},
                    "required": ["name"],
                },
            },
            {"name": "python_time", "description": "Get current server time.", "parameters": []},
        ],
        "hooks": [{"event": "PostToolUse", "matcher": "python_*"}],
        "enrichers": [{"name": "python_env"}],
    }

def handle_execute_tool(params):
    tool_name = params.get("toolName", "")
    input_data = json.loads(params.get("input", "{}") or "{}")
    if tool_name == "python_greet":
        return {"result": json.dumps({"english": f"Hello, {input_data.get('name', 'World')}!"})}
    if tool_name == "python_time":
        return {"result": json.dumps({"iso": datetime.now().isoformat()})}
    return {"error": f"Unknown tool: {tool_name}"}

def handle_hook(params):
    return {"hookResult": {"decision": "allow"}}

def handle_enrich(params):
    if params.get("enricherName") == "python_env":
        import platform
        return {"result": json.dumps({"python_version": platform.python_version()})}
    return {"error": "Unknown enricher"}

HANDLERS = {
    "activate": handle_activate,
    "deactivate": lambda _p: {},
    "execute_tool": handle_execute_tool,
    "hook": handle_hook,
    "enrich": handle_enrich,
}

def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        request = json.loads(line)
        handler = HANDLERS.get(request.get("method", ""))
        if handler is None:
            response = {"error": f"Unknown method: {request.get('method')}"}
        else:
            try:
                response = handler(request.get("params", {}))
            except Exception as e:
                response = {"error": str(e)}
        # Write response as single-line JSON and flush immediately
        print(json.dumps(response), flush=True)

if __name__ == "__main__":
    main()
```

## Step 2: Write the manifest

```json
{
  "id": "com.example.python-hello",
  "name": "Python Hello",
  "version": "1.0.0",
  "description": "Example Python plugin demonstrating the JSON/stdio protocol",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich"],
  "contributes": {
    "tools": [
      { "name": "python_greet", "description": "Greet someone by name." },
      { "name": "python_time", "description": "Get current server time." }
    ],
    "hooks": [ { "event": "PostToolUse", "matcher": "python_*" } ],
    "contextEnrichers": [ { "name": "python_env", "description": "Inject Python runtime environment info" } ]
  }
}
```

## Step 3: Restart and test

Restart xbot. The agent can now call `python_greet` and `python_time`; every `python_*` tool completion fires your hook; the `python_env` enricher is injected into the system prompt.

## The wire protocol

Requests from xbot (`plugin/protocol/protocol.go:43`):

```json
{"method":"activate","params":{"pluginId":"com.example.python-hello"}}
{"method":"execute_tool","params":{"toolName":"python_greet","input":"{\"name\":\"Bob\"}"}}
{"method":"hook","params":{"event":"PostToolUse","toolName":"python_greet","toolInput":"..."}}
{"method":"enrich","params":{"enricherName":"python_env"}}
{"method":"deactivate","params":{}}
```

Your responses populate only the fields relevant to the method (`protocol.Response`):

```json
{"tools":[{"name":"python_greet","description":"...","parameters":[...]}]}
{"result":"{\"english\":\"Hello, Bob!\"}"}
{"hook_result":{"decision":"allow"}}
{"enrichers":[{"name":"python_env"}]}
```

## Rules of the road

1. **One JSON object per line, flush after every response.** Buffered output deadlocks the caller.
2. **Log to stderr** — it is captured into xbot's log; stdout is protocol-only.
3. **`execute_tool` params.input is a JSON string**, not an object. Parse it yourself.
4. **Timeout**: operations are bounded by the manifest `timeout` field (Go duration string, e.g. `"30s"`; default `DefaultPluginTimeout = 30s`, `plugin/plugin.go:723`). Exceeding it kills the process.

## Prefer Go? Use the canonical structs

`plugin/protocol` exports typed structs so Go stdio plugins don't hand-roll JSON:

```go
h := &protocol.Handler{
	Activate: func(req *protocol.ActivateParams) (*protocol.ActivateResult, error) {
		return &protocol.ActivateResult{Tools: []protocol.ToolDef{{Name: "greet", Description: "Greet someone"}}}, nil
	},
	ExecuteTool: func(params *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) {
		return &protocol.ExecuteToolResult{Result: `{"msg":"Hello!"}`}, nil
	},
}
protocol.Run(h) // reads stdin, dispatches, writes stdout
```

The built-in `xbot.git-fancy` backend uses exactly this pattern (`plugins/xbot-git-fancy/main.go`).

Next: [Stdio Plugins](../stdio-plugins/) for the full protocol reference.
