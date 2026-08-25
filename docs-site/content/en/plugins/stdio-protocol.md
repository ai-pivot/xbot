---
title: "Stdio Runtime Protocol"
weight: 13
---

Stdio plugins communicate with xbot via JSON-over-stdin/stdout. This protocol supports any programming language.

## Protocol Overview

The stdio runtime uses a bidirectional JSON line protocol:

- **xbot → plugin**: Requests (JSON objects with `method` field)
- **plugin → xbot**: Responses (JSON objects with `result` or `error`) or inbound messages (JSON objects with `method` field)

```
xbot                    Plugin Process
 │                           │
 │ ── activate request ────→ │
 │ ←── activate response ── │
 │                           │
 │ ── execute_tool req ────→ │
 │ ←── tool result ───────── │
 │                           │
 │ ←── channel_inbound ───── │ (async push)
 │                           │
 │ ── hook event ──────────→ │
 │ ←── hook result ───────── │
 │                           │
 │ ── deactivate ──────────→ │
 │ ←── response ──────────── │
```

## Message Types

### Request (xbot → plugin)

```json
{
  "method": "activate",
  "params": {
    "pluginId": "my-plugin",
    "config": { "key": "value" }
  }
}
```

### Response (plugin → xbot)

```json
{
  "result": "success",
  "tools": [...],
  "hooks": [...],
  "enrichers": [...],
  "channel_provider": {...}
}
```

Or error:

```json
{
  "error": "something went wrong"
}
```

### Inbound (plugin → xbot, async)

```json
{
  "method": "channel_inbound",
  "params": {
    "message": "user input"
  }
}
```

## Methods

### `activate`

Sent when the plugin is activated. The plugin should register its capabilities in the response.

**Request:**
```json
{
  "method": "activate",
  "params": {
    "pluginId": "my-plugin",
    "config": { "setting1": "value1" }
  }
}
```

**Response:**
```json
{
  "tools": [
    {
      "name": "my-tool",
      "description": "Does something useful",
      "parameters": [
        {"name": "input", "type": "string", "description": "Input text", "required": true}
      ]
    }
  ],
  "hooks": [
    {"event": "PreToolUse", "matcher": "Shell"}
  ],
  "enrichers": [
    {"name": "context-enricher"}
  ],
  "channel_provider": {
    "name": "my-channel",
    "config_schema": [...]
  }
}
```

### `deactivate`

Sent when the plugin is being deactivated. The plugin should clean up resources.

**Request:**
```json
{
  "method": "deactivate"
}
```

**Response:**
```json
{
  "result": "ok"
}
```

### `execute_tool`

Sent when the LLM calls a tool registered by the plugin.

**Request:**
```json
{
  "method": "execute_tool",
  "params": {
    "toolName": "my-tool",
    "input": "user input"
  }
}
```

**Response:**
```json
{
  "result": "Tool output text"
}
```

Or error:
```json
{
  "error": "Tool execution failed: ..."
}
```

### `hook`

Sent when a lifecycle hook fires.

**Request:**
```json
{
  "method": "hook",
  "params": {
    "event": "PreToolUse",
    "toolName": "Shell",
    "toolInput": "ls -la",
    "sessionId": "session-123",
    "channel": "cli",
    "chatId": "chat-456"
  }
}
```

**Response:**
```json
{
  "hook_result": {
    "decision": "allow",
    "message": ""
  }
}
```

Hook decisions: `allow`, `deny`, `defer`, `ask`.

### `enrich`

Sent when a context enricher is invoked.

**Request:**
```json
{
  "method": "enrich",
  "params": {
    "enricherName": "context-enricher"
  }
}
```

**Response:**
```json
{
  "result": "Additional context to inject"
}
```

### `config_changed`

Sent when the plugin's configuration changes (hot reload).

**Request:**
```json
{
  "method": "config_changed",
  "params": {
    "config": { "setting1": "new-value" }
  }
}
```

**Response:**
```json
{
  "result": "ok"
}
```

## Channel Plugin Protocol

Channel plugins use additional inbound messages:

### `channel_tools`

Declares tools for a specific channel:

```json
{
  "method": "channel_tools",
  "params": {
    "tools": [...]
  }
}
```

### `channel_prompt`

Declares channel-specific system prompt parts:

```json
{
  "method": "channel_prompt",
  "params": {
    "system_parts": {
      "05_channel_xxx": "Channel-specific context"
    }
  }
}
```

### `web_ui`

Declares web UI components:

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

## Error Handling

- **Process crash**: xbot detects stdout closure and marks the plugin as errored
- **Timeout**: Plugin calls have a 30-second timeout (configurable via manifest `timeout`)
- **Malformed JSON**: Lines that fail to parse are logged and skipped
- **Auto-retry**: If enabled, xbot retries activation with exponential backoff

## Python Example

```python
#!/usr/bin/env python3
import json
import sys

def handle_request(req):
    method = req.get("method")
    params = req.get("params", {})
    
    if method == "activate":
        return {
            "tools": [{
                "name": "greet",
                "description": "Greet someone",
                "parameters": [
                    {"name": "name", "type": "string", "description": "Name to greet", "required": True}
                ]
            }],
            "hooks": [
                {"event": "PostToolUse", "matcher": "Shell"}
            ]
        }
    elif method == "execute_tool":
        tool = params.get("toolName")
        if tool == "greet":
            return {"result": f"Hello, {params.get('input', 'World')}!"}
        return {"error": f"unknown tool: {tool}"}
    elif method == "hook":
        event = params.get("event")
        if event == "PostToolUse":
            # Log tool usage
            return {"hook_result": {"decision": "allow"}}
        return {"hook_result": {"decision": "allow"}}
    elif method == "deactivate":
        return {"result": "ok"}
    
    return {"error": f"unknown method: {method}"}

for line in sys.stdin:
    try:
        req = json.loads(line)
        resp = handle_request(req)
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    except Exception as e:
        sys.stdout.write(json.dumps({"error": str(e)}) + "\n")
        sys.stdout.flush()
```

## Node.js Example

```javascript
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });

rl.on('line', (line) => {
    const req = JSON.parse(line);
    const method = req.method;
    const params = req.params || {};
    
    let resp;
    if (method === 'activate') {
        resp = {
            tools: [{
                name: 'timestamp',
                description: 'Get current timestamp',
                parameters: []
            }]
        };
    } else if (method === 'execute_tool') {
        if (params.toolName === 'timestamp') {
            resp = { result: new Date().toISOString() };
        } else {
            resp = { error: `unknown tool: ${params.toolName}` };
        }
    } else if (method === 'deactivate') {
        resp = { result: 'ok' };
    } else {
        resp = { error: `unknown method: ${method}` };
    }
    
    process.stdout.write(JSON.stringify(resp) + '\n');
});
```

## Protocol Types (Go)

```go
type PluginRequest struct {
    Method string         `json:"method"`
    Params map[string]any `json:"params,omitempty"`
}

type PluginResponse struct {
    Result          string              `json:"result,omitempty"`
    Error           string              `json:"error,omitempty"`
    Tools           []ToolDef           `json:"tools,omitempty"`
    Hooks           []hookReg           `json:"hooks,omitempty"`
    HookResult      *HookResult         `json:"hook_result,omitempty"`
    Enrichers       []enricherReg       `json:"enrichers,omitempty"`
    ChannelProvider *ChannelProviderDecl `json:"channel_provider,omitempty"`
}

type PluginInbound struct {
    Method string         `json:"method"`
    Params map[string]any `json:"params,omitempty"`
}
```

## See Also

- [Channel Plugins](./channel-plugins/) — Full channel adapter development
- [Script Runtime](./script-runtime/) — Simpler script-based plugins
- [Architecture](./architecture/) — How stdio runtime fits in the system
- [Cookbook: Stdio Plugin](./cookbook/stdio-plugin/) — Step-by-step guide
