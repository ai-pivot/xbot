---
title: "Stdio Plugins"
weight: 8
---

Stdio plugins are external processes speaking NDJSON over stdin/stdout. The runtime is declared as `"stdio"` (or `"grpc"` for backward compat) and implemented in `plugin/runtime.go` (process management) + `plugin/protocol/protocol.go` (wire types). Examples: `plugin/examples/grpc-python/main.py` (Python), `plugins/xbot-git-fancy/main.go` (Go, production).

## Process lifecycle

`stdioPlugin.Activate` (`plugin/runtime.go:114`) spawns the process via `startPluginProcess(entry, executable, args, dir)`. The manifest fields:

| Field | Meaning |
|---|---|
| `entry` | Command to spawn (e.g. `"python3 main.py"`) |
| `executable` | Overrides `entry` (security: prevents shell interpretation) |
| `args` | Extra args appended to the executable |
| `timeout` | Go duration string; `DefaultPluginTimeout = 30s`; operations exceeding it kill the process |

A single `readLoop` goroutine demultiplexes stdout (`StdioPluginProcess`, `plugin/runtime.go:70`):

- lines with `"method"` and no `"id"` → **inbound push messages** → `InboundHandler`;
- lines with `"result"`/`"error"` → responses to pending `Call()` requests.

## Wire protocol (Request/Response)

`protocol.Request` (xbot → plugin):

```json
{"method":"activate","params":{"pluginId":"com.example.plugin"}}
{"method":"execute_tool","params":{"toolName":"t","input":"{\"k\":\"v\"}"}}
{"method":"hook","params":{"event":"PostToolUse","toolName":"t","toolInput":"...","sessionId":"...","channel":"web","chatId":"..."}}
{"method":"enrich","params":{"enricherName":"env"}}
{"method":"web_plugin_rpc","params":{"method":"git.status","params":{...}}}
{"method":"web_ui_action","params":{"widgetId":"w","action":"click","data":"{}","chatId":"..."}}
{"method":"deactivate","params":{}}
```

`protocol.Response` (plugin → xbot) — populate only the relevant fields:

```json
{"tools":[{"name":"t","description":"...","parameters":[{"name":"k","type":"string","required":true}],"inputSchema":{...}}]}
{"hooks":[{"event":"PostToolUse","matcher":"t*"}]}
{"enrichers":[{"name":"env"}]}
{"result":"<tool output string>"}
{"error":"<error message>"}
{"hook_result":{"decision":"allow"}}
{"channel_provider":{"name":"echo","config_schema":[...]}}
```

`protocol.HookResult.Decision` is one of `allow`, `deny`, `ask`, `defer`; `Message` carries the explanation.

## The Go handler API

`protocol.Handler` (`plugin/protocol/protocol.go:200`) is the fill-in-the-blanks API:

```go
h := &protocol.Handler{
	Activate: func(req *protocol.ActivateParams) (*protocol.ActivateResult, error) { ... },
	ExecuteTool: func(p *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) { ... },
	Hook: func(p *protocol.HookParams) (*protocol.HookResult, error) { ... },
	Enrich: func(p *protocol.EnrichParams) (*protocol.EnrichResult, error) { ... },
	WebUIAction: func(p *protocol.WebUIActionParams) (*protocol.WebUIActionResult, error) { ... },
	WebPluginRPC: func(p *protocol.WebPluginRPCParams) (*protocol.WebPluginRPCResult, error) { ... },
	Deactivate: func(p *protocol.DeactivateParams) (*protocol.DeactivateResult, error) { ... },
}
protocol.Run(h) // reads stdin line-by-line, dispatches, writes stdout
```

`Run` handles the loop, error marshalling, and flushing. Max line size is 1MB (`maxLineSize`, `protocol.go:442`).

## WebPluginRPC — the frontend↔backend bridge

`WebPluginRPCParams` carries `Method` (e.g. `"git.status"`) + arbitrary JSON params, routed from the frontend via `ctx.rpc.call('pluginId.method', ...)`. The built-in `git-fancy` backend is the reference (`plugins/xbot-git-fancy/main.go handleWebPluginRPC`):

```go
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) *protocol.WebPluginRPCResult {
	switch p.Method {
	case "git.status":
		return rpcOK(gitStatus(cwd))
	case "git.log":
		var params struct{ Limit *int `json:"limit"` }
		json.Unmarshal(p.Params, &params)
		return rpcOK(gitLog(cwd, limit, 0))
	default:
		return rpcErr("unknown method: " + p.Method)
	}
}
```

The frontend then types these methods via declaration merging in `BackendRPC` (`web/src/plugin-api/rpc.ts`) — see [Web Plugins](../web-plugins/).

## Python reference (stdlib only)

The dispatch-table pattern from `grpc-python/main.py`:

```python
HANDLERS = {
    "activate": handle_activate,
    "deactivate": handle_deactivate,
    "execute_tool": handle_execute_tool,
    "hook": handle_hook,
    "enrich": handle_enrich,
}

def main():
    for line in sys.stdin:
        request = json.loads(line.strip())
        handler = HANDLERS.get(request.get("method", ""))
        response = handler(request.get("params", {})) if handler else {"error": "Unknown method"}
        print(json.dumps(response), flush=True)   # flush is mandatory
```

## Gotchas

1. **Always flush stdout.** A buffered pipe stalls the pending `Call()` in xbot until timeout.
2. **Log to stderr**, never stdout — stdout carries the protocol.
3. **`execute_tool.input` is a string.** Always `json.loads` it; tolerate `"{}"`/empty.
4. **Response field names are snake_case** (`hook_result`, `channel_provider`, `inputSchema`) — the protocol predates the JSON camelCase convention.
5. **Handle unknown methods with `{"error": ...}`**, not silence — silence looks like a hung process.
6. **Timeout kills the process**: `manager.call()` (`plugin/manager.go`) kills and marks not-running to prevent goroutine leaks on blocked stdout reads.
