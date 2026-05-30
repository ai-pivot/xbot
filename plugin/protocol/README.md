# xbot Plugin Protocol (Go SDK)

Go SDK for writing [xbot](https://github.com/ai-pivot/xbot) plugins using the stdio runtime.

## Install

```bash
go get github.com/ai-pivot/xbot/plugin/protocol
```

## Quick Start

```go
package main

import (
    "github.com/ai-pivot/xbot/plugin/protocol"
)

func main() {
    h := &protocol.Handler{
        Activate: func(p *protocol.ActivateParams) (*protocol.ActivateResult, error) {
            return &protocol.ActivateResult{
                Tools: []protocol.ToolDef{
                    {
                        Name:        "greet",
                        Description: "Greet someone by name",
                        InputSchema: []byte(`{
                            "type": "object",
                            "properties": {"name": {"type": "string"}},
                            "required": ["name"]
                        }`),
                    },
                },
            }, nil
        },
        ExecuteTool: func(p *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) {
            // Parse input JSON and return result JSON
            return &protocol.ExecuteToolResult{Result: `{"message": "Hello!"}`}, nil
        },
    }
    protocol.Run(h)
}
```

## plugin.json

```json
{
  "id": "my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "my-plugin"
}
```

The `runtime` field accepts `"stdio"` (recommended) or `"grpc"` (backward compat alias).
The `entry` field is the executable name (built with `go build -o my-plugin .`).

## Handler Callbacks

| Callback | Required | When |
|----------|----------|------|
| `Activate` | Yes | Plugin process starts. Return tools/hooks/enrichers. |
| `ExecuteTool` | If you register tools | LLM invokes one of your tools. |
| `Hook` | If you register hooks | Lifecycle event fires. |
| `Enrich` | If you register enrichers | System prompt context collection. |
| `Deactivate` | No | Before process is killed. |

## Types

- `ToolDef` — tool definition (name, description, JSON Schema input)
- `HookReg` — hook registration (`event` + `matcher`)
- `HookResult` — hook response (`allow`/`deny`/`ask`/`defer`)
- `EnricherReg` — context enricher registration
- `ChannelProviderDecl` — channel plugin declaration

## Helpers

```go
// Default allow
protocol.HookResultAllow

// Deny with message
protocol.HookResultDeny("not allowed")
```
