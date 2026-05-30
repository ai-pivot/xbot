# xbot Plugin Protocol (JSON/stdio)

> **Protocol Version**: v1
> **Runtime Name**: `stdio` (aliases: `grpc` for backward compatibility)
> **Status**: Phase 1 — stable, subject to minor additions in future iterations

## 1. Overview

The `stdio` runtime (also accepted as `grpc` for backward compatibility) communicates with xbot via **newline-delimited JSON (NDJSON)** over stdin/stdout. **Language-agnostic: any language that can read/write JSON and stdin/stdout works

**Design goals**:
- Language-agnostic: any language that can read/write JSON and stdin/stdout works
- Easy to debug: messages are human-readable JSON
- Synchronous request-response: one request in flight at a time

**Relationship to other docs**:
- [README.md](./README.md) — Quick reference for the Go native plugin API
- [DESIGN.md](./DESIGN.md) — Architecture and design decisions
- [GUIDE.md](./GUIDE.md) — Developer guide (tutorials, decision guidance)

---

## 2. Transport Layer

### 2.1 Communication Channels

| Channel | Direction | Purpose |
|---------|-----------|---------|
| **stdin** | xbot → Plugin | JSON request lines |
| **stdout** | Plugin → xbot | JSON response lines |
| **stderr** | Plugin → xbot | Debug output (passed through to host stderr) |

### 2.2 Message Format

All messages are **single-line JSON objects** terminated by `\n` (NDJSON):

```
{"method":"activate","params":{"pluginId":"com.example.my-plugin"}}\n
{"result":"...","tools":[...]}\n
```

**Constraints**:
- Maximum line size: **1 MB** (1,048,576 bytes)
- Character encoding: **UTF-8**
- Each line is a complete JSON object — no partial writes
- Plugin **must flush stdout** after every response line (important for Python, Node.js)

### 2.3 Process Lifecycle

xbot spawns the plugin process as follows:

1. **Start**: `exec.Command(executable, args...)` or `exec.Command(parts[0], parts[1:]...)` from `entry` field
   - Working directory: plugin's install directory
   - `executable` field takes precedence over `entry` if both are set
2. **Communicate**: Send/receive NDJSON lines over stdin/stdout
3. **Stop**: On deactivation, timeout, or error → `SIGKILL` the process (no graceful shutdown)

---

## 3. Message Formats

### 3.1 Request

Every request from xbot to the plugin follows this structure:

```json
{
  "method": "<method_name>",
  "params": { ... }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `method` | string | Yes | The method to invoke |
| `params` | object | No | Method-specific parameters (omitted when empty) |

### 3.2 Response

Every response from the plugin to xbot follows this structure:

```json
{
  "result": "...",
  "error": "",
  "tools": [],
  "hooks": [],
  "hookResult": null,
  "enrichers": []
}
```

| Field | Type | Applies to | Description |
|-------|------|------------|-------------|
| `result` | string | `execute_tool`, `enrich` | Success result content |
| `error` | string | All methods | Error message (non-empty = error) |
| `tools` | ToolDef[] | `activate` | Tool declarations |
| `hooks` | hookReg[] | `activate` | Hook subscriptions |
| `hookResult` | HookResult? | `hook` | Hook decision result |
| `enrichers` | enricherReg[] | `activate` | Enricher registrations |

**Note**: Fields not applicable to the current method are omitted (zero-value JSON omit).

### 3.3 Auxiliary Types

#### ToolDef

```json
{
  "name": "tool_name",
  "description": "What this tool does",
  "parameters": [
    {
      "name": "param_name",
      "type": "string",
      "description": "Parameter description",
      "required": true
    }
  ],
  "inputSchema": {
    "type": "object",
    "properties": {
      "param_name": {
        "type": "string",
        "description": "Parameter description"
      }
    },
    "required": ["param_name"]
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Tool identifier (unique within plugin) |
| `description` | string | Yes | Human-readable description for LLM |
| `parameters` | ToolParam[] | No | Parameter definitions for LLM function calling |
| `inputSchema` | object | No | JSON Schema for tool input (recommended) |

#### ToolParam

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Parameter name |
| `type` | string | Yes | JSON Schema type: `string`, `number`, `boolean`, `array`, `object` |
| `description` | string | Yes | Parameter description for LLM |
| `required` | boolean | Yes | Whether this parameter is required |
| `items` | object | No | Schema for array items (only when type is `array`) |

#### hookReg

```json
{
  "event": "PreToolUse",
  "matcher": "Shell*"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `event` | string | Yes | Hook event name (see §5.2 for valid events) |
| `matcher` | string | No | Tool name glob pattern (`""` = match all) |

#### enricherReg

```json
{
  "name": "my_enricher"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique enricher name within the plugin |

#### HookResult

```json
{
  "decision": "allow",
  "message": "Optional explanation",
  "data": {}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `decision` | string | Yes | One of: `allow`, `deny`, `ask`, `defer` |
| `message` | string | No | Explanation (required for `deny` and `ask`) |
| `data` | object | No | Additional structured data |

---

## 4. Method Specifications

### 4.1 `activate`

Called once after the plugin process starts. The plugin declares all its capabilities in the response.

**Request**:
```json
{
  "method": "activate",
  "params": {
    "pluginId": "com.example.my-plugin"
  }
}
```

| Param | Type | Description |
|-------|------|-------------|
| `pluginId` | string | The plugin's unique ID from the manifest |

**Response (success)**:
```json
{
  "tools": [
    {
      "name": "greet",
      "description": "Greet someone by name",
      "parameters": [
        {"name": "name", "type": "string", "description": "Person to greet", "required": true}
      ],
      "inputSchema": {
        "type": "object",
        "properties": {"name": {"type": "string", "description": "Person to greet"}},
        "required": ["name"]
      }
    }
  ],
  "hooks": [
    {"event": "PostToolUse", "matcher": ""}
  ],
  "enrichers": [
    {"name": "status"}
  ]
}
```

**Response (error)**:
```json
{"error": "Failed to initialize: database connection refused"}
```

**Behavior**:
- On error (`error` non-empty), xbot kills the plugin process and marks the plugin as `error` state
- On success, all tools, hooks, and enrichers are registered with xbot
- This is a **one-time declaration** — capabilities cannot be added after activation

### 4.2 `deactivate`

Called before the plugin process is killed. The plugin should clean up resources.

**Request**:
```json
{"method": "deactivate"}
```

**Response**: Any response (ignored by xbot). After the response is received, the process is killed.

### 4.3 `execute_tool`

Called when the LLM invokes one of the plugin's registered tools.

**Request**:
```json
{
  "method": "execute_tool",
  "params": {
    "toolName": "greet",
    "input": "{\"name\":\"Alice\"}"
  }
}
```

| Param | Type | Description |
|-------|------|-------------|
| `toolName` | string | Name of the tool to execute |
| `input` | string | JSON string of tool input (must be parsed by the plugin) |

**Response (success)**:
```json
{"result": "Hello, Alice! 👋"}
```

**Response (error)**:
```json
{"error": "Unknown tool: greet"}
```

When `error` is non-empty, xbot wraps it as a `ToolResult` with `isError: true`.

### 4.4 `hook`

Called when a lifecycle event matches one of the plugin's registered hooks.

**Request**:
```json
{
  "method": "hook",
  "params": {
    "event": "PostToolUse",
    "toolName": "Shell",
    "toolInput": "{\"command\":\"ls -la\"}",
    "sessionId": "sess_abc123",
    "channel": "cli",
    "chatId": "chat_xyz"
  }
}
```

| Param | Type | Description |
|-------|------|-------------|
| `event` | string | The hook event name |
| `toolName` | string | Name of the tool being executed (empty for non-tool events) |
| `toolInput` | string | Raw JSON input to the tool (empty for non-tool events) |
| `sessionId` | string | Current session identifier |
| `channel` | string | Message channel (e.g., `cli`, `feishu`, `web`) |
| `chatId` | string | Chat/conversation ID |

> **Known Limitation**: The current implementation does not pass `userId` or `extra` fields from `HookPayload` to remote plugins. These will be added in a future iteration.

**Response (allow)**:
```json
{
  "hookResult": {
    "decision": "allow"
  }
}
```

**Response (deny)**:
```json
{
  "hookResult": {
    "decision": "deny",
    "message": "This operation is blocked by policy"
  }
}
```

**Response (ask user)**:
```json
{
  "hookResult": {
    "decision": "ask",
    "message": "This tool will modify files. Allow?"
  }
}
```

**Default behavior**: If `hookResult` is `null` or missing, xbot treats it as `decision: "allow"`.

### 4.5 `enrich`

Called to collect dynamic content for injection into the system prompt.

**Request**:
```json
{
  "method": "enrich",
  "params": {
    "enricherName": "status"
  }
}
```

| Param | Type | Description |
|-------|------|-------------|
| `enricherName` | string | Name of the enricher to invoke |

**Response (success)**:
```json
{"result": "Plugin status: active, uptime 2h30m, 42 tool calls served"}
```

The `result` string is injected into the system prompt as:
```
## <enricherName> (<pluginId>)
<result>
```

**Response (error)**:
```json
{"error": "Failed to query status"}
```

---

## 5. Hook Events

### 5.1 Valid Event Names

| Event | Trigger | Can Deny? |
|-------|---------|-----------|
| `PreToolUse` | Before a tool executes | ✅ Yes |
| `PostToolUse` | After a tool succeeds | ❌ No |
| `PostToolUseFailure` | After a tool fails | ❌ No |
| `UserPromptSubmit` | User sends a message | ❌ No |
| `AgentStop` | Agent finishes processing | ❌ No |
| `SessionStart` | New session created | ❌ No |
| `SessionEnd` | Session ended | ❌ No |
| `SubAgentStart` | SubAgent spawned | ❌ No |
| `SubAgentStop` | SubAgent completed | ❌ No |
| `PreCompact` | Before context compression | ❌ No |
| `PostCompact` | After context compression | ❌ No |
| `CronFired` | Scheduled task triggered | ❌ No |
| `WebhookReceived` | External webhook received | ❌ No |

### 5.2 Matcher Patterns

The `matcher` field in hook registrations supports glob-style patterns:

| Pattern | Meaning | Example |
|---------|---------|---------|
| `""` or `"*"` | Match all tools | `""` |
| `"Shell"` | Exact match | `"Shell"` |
| `"Shell*"` | Prefix match | Matches `Shell`, `ShellScript` |
| `"*git*"` | Contains match | Matches `GitStatus`, `RunGit` |
| `"*Deploy"` | Suffix match | Matches `Deploy`, `K8sDeploy` |

### 5.3 Decision Aggregation

When multiple hooks match the same event, decisions are aggregated by priority:

| Decision | Priority | Behavior |
|----------|----------|----------|
| `deny` | 4 (highest) | Blocks the operation, all lower decisions overridden |
| `ask` | 3 | Prompts user for confirmation |
| `defer` | 2 | Defers to the next handler |
| `allow` | 1 (lowest) | Permits the operation |

The highest-priority decision wins. If no hooks are registered, the default is `defer`.

---

## 6. Timeout and Error Handling

### 6.1 Call Timeout

- **Default timeout**: 30 seconds per call
- **Timeout behavior**: The plugin process is **killed** (`SIGKILL`) — no graceful shutdown
- **Context cancellation**: Also kills the process immediately

### 6.2 Concurrency Model

The protocol is **strictly synchronous** — one request in flight at a time. xbot acquires a mutex before sending a request and holds it until the response is received (or timeout). The plugin does not need to handle concurrent requests.

### 6.3 Error Scenarios

| Scenario | Error Message | Behavior |
|----------|---------------|----------|
| Plugin exits unexpectedly | `plugin process exited (EOF)` | Plugin enters `error` state |
| Plugin sends empty line | `empty response from plugin` | Plugin enters `error` state |
| Plugin sends invalid JSON | JSON parse error | Plugin enters `error` state |
| Call times out (30s) | `plugin call timeout (30s)` | Process killed |
| Context cancelled | `context canceled` | Process killed |

---

## 7. Lifecycle Sequence

```
xbot (host)                              Plugin process
    |                                          |
    |---- spawn process ---------------------->|
    |                                          |
    |---- {"method":"activate", ...} --------->|  (stdin)
    |<--- {"tools":[...], "hooks":[...]} ------|  (stdout)
    |                                          |
    |                                          |  ... idle ...
    |                                          |
    |---- {"method":"execute_tool", ...} ------>|  (stdin)
    |<--- {"result":"Hello, Alice!"} ----------|  (stdout)
    |                                          |
    |---- {"method":"hook", ...} ------------->|  (stdin)
    |<--- {"hookResult":{"decision":"allow"}} -|  (stdout)
    |                                          |
    |---- {"method":"enrich", ...} ------------>|  (stdin)
    |<--- {"result":"Status: active"} ---------|  (stdout)
    |                                          |
    |---- {"method":"deactivate"} ------------>|  (stdin)
    |<--- {} ----------------------------------|  (stdout)
    |                                          |
    |---- SIGKILL ---------------------------->|
```

**Stderr** is available for debug output at any time — it is passed through to the host process.

---

## 8. Security Considerations

1. **Process isolation**: stdio plugins run as separate OS processes, providing memory isolation from xbot
2. **Same user privileges**: The plugin process runs with the same OS user as xbot — no sandboxing
3. **Permission enforcement**: Permissions declared in `plugin.json` are enforced by the host's `PermissionChecker` — the plugin cannot bypass them
4. **No sensitive data in transit**: The protocol does not transmit passwords, tokens, or API keys
5. **Use `executable` over `entry`**: For security, prefer specifying `executable` (absolute path) over `entry` (shell-split string) in the manifest

---

## 9. Debugging Tips

### Manual Protocol Testing

Test your plugin's protocol handling by piping JSON directly:

```bash
# Test activate
echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 main.py

# Test tool execution
echo '{"method":"execute_tool","params":{"toolName":"greet","input":"{\"name\":\"Alice\"}"}}' | python3 main.py

# Test hook
echo '{"method":"hook","params":{"event":"PostToolUse","toolName":"greet","toolInput":"{}","sessionId":"s1","channel":"cli","chatId":"c1"}}' | python3 main.py
```

### Common Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| xbot reports "plugin process exited (EOF)" | Plugin crashed or exited early | Check stderr output; test manually |
| xbot reports "empty response from plugin" | Plugin wrote nothing to stdout | Ensure `print(json.dumps(...))` + `flush()` |
| Tool calls hang until timeout | Plugin forgot to flush stdout | Call `sys.stdout.flush()` after every `print()` |
| "Invalid JSON" errors | Plugin wrote multi-line or malformed JSON | Ensure single-line JSON output |
| Plugin killed after 30s | Tool execution too slow | Optimize or break into smaller operations |

### Using stderr for Debugging

Any output written to stderr appears in xbot's log output:

```python
import sys
print(f"[debug] Processing method: {method}", file=sys.stderr)
```

---

## Appendix A: Complete Message Examples

### A.1 Activate Flow

**Request**:
```json
{"method":"activate","params":{"pluginId":"com.example.python-hello"}}
```

**Response**:
```json
{
  "tools": [
    {
      "name": "python_greet",
      "description": "Greet someone by name",
      "parameters": [
        {"name": "name", "type": "string", "description": "Person to greet", "required": true}
      ],
      "inputSchema": {
        "type": "object",
        "properties": {"name": {"type": "string", "description": "Person to greet"}},
        "required": ["name"]
      }
    },
    {
      "name": "python_time",
      "description": "Get current server time",
      "parameters": [],
      "inputSchema": {"type": "object", "properties": {}}
    }
  ],
  "hooks": [
    {"event": "PostToolUse", "matcher": "python_*"}
  ],
  "enrichers": [
    {"name": "python_env"}
  ]
}
```

### A.2 Tool Execution (success)

**Request**:
```json
{"method":"execute_tool","params":{"toolName":"python_greet","input":"{\"name\":\"Alice\"}"}}
```

**Response**:
```json
{"result":"{\"english\":\"Hello, Alice!\",\"chinese\":\"你好，Alice！\",\"japanese\":\"こんにちは、Alice！\"}"}
```

### A.3 Tool Execution (error)

**Request**:
```json
{"method":"execute_tool","params":{"toolName":"unknown_tool","input":"{}"}}
```

**Response**:
```json
{"error":"Unknown tool: unknown_tool"}
```

### A.4 Hook (deny)

**Request**:
```json
{"method":"hook","params":{"event":"PreToolUse","toolName":"Shell","toolInput":"{\"command\":\"format disk\"}","sessionId":"s1","channel":"cli","chatId":"c1"}}
```

**Response**:
```json
{"hookResult":{"decision":"deny","message":"Dangerous operation blocked by policy"}}
```

### A.5 Enrich

**Request**:
```json
{"method":"enrich","params":{"enricherName":"python_env"}}
```

**Response**:
```json
{"result":"Python 3.11.5 on Linux x86_64"}
```

## Appendix B: HookEvent Reference

All valid `event` values for hook registration:

| Event | Description | Typical Use |
|-------|-------------|-------------|
| `PreToolUse` | Before a tool executes | Security checks, input validation |
| `PostToolUse` | After a tool succeeds | Logging, auditing |
| `PostToolUseFailure` | After a tool fails | Error tracking |
| `UserPromptSubmit` | User sends a message | Prompt modification |
| `AgentStop` | Agent loop terminates | Session cleanup |
| `SessionStart` | New session created | Session initialization |
| `SessionEnd` | Session concludes | Session cleanup |
| `SubAgentStart` | SubAgent is spawned | Tracing |
| `SubAgentStop` | SubAgent completes | Tracing |
| `PreCompact` | Before context compression | Pre-compaction hooks |
| `PostCompact` | After context compression | Post-compaction hooks |
| `CronFired` | Scheduled task triggers | Automation |
| `WebhookReceived` | External webhook arrives | Integration |
