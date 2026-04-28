# Python Plugin Example

A minimal example demonstrating how to write a xbot plugin using the JSON/stdio protocol (`grpc` runtime).

## What It Demonstrates

| Feature | Implementation |
|---------|---------------|
| **Tool registration** | `python_greet` (with input parameter) and `python_time` (no parameters) |
| **Hook handling** | `PostToolUse` hook for `python_*` tools |
| **Context enrichment** | `python_env` enricher injecting runtime info |
| **Error handling** | Unknown tool name returns `{"error": "..."}` |
| **Debug logging** | Hook handler logs to stderr |

## Prerequisites

- Python 3.7+ (uses only standard library)
- xbot with plugin system enabled

## Quick Start

### 1. Install the plugin

```bash
# Copy the example to xbot's plugin directory
cp -r plugin/examples/grpc-python/ ~/.xbot/plugins/python-hello/
```

### 2. Restart xbot

The plugin activates on `onStart`, so its tools are immediately available.

### 3. Use the tools

In xbot, ask the LLM to use the tools:

```
> Use the python_greet tool to greet "Bob"
> What time is it? Use the python_time tool.
```

## File Structure

```
grpc-python/
├── plugin.json   # Plugin manifest (runtime: grpc)
├── main.py       # Plugin implementation (NDJSON loop)
└── README.md     # This file
```

## How It Works

1. xbot starts the plugin by running `python3 main.py` (as specified in `plugin.json` `entry` field)
2. xbot sends an `activate` request via stdin — the plugin responds with its tools, hooks, and enrichers
3. When the LLM calls `python_greet` or `python_time`, xbot sends an `execute_tool` request
4. When a `python_*` tool completes, xbot sends a `hook` request (PostToolUse)
5. On shutdown, xbot sends `deactivate` and kills the process

See [PROTOCOL.md](../../PROTOCOL.md) for the full protocol specification.

## Manual Testing

Test the protocol directly from the command line:

```bash
# Test activate
echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 main.py

# Test tool execution
echo '{"method":"execute_tool","params":{"toolName":"python_greet","input":"{\"name\":\"Alice\"}"}}' | python3 main.py

# Test time tool
echo '{"method":"execute_tool","params":{"toolName":"python_time","input":"{}"}}' | python3 main.py

# Test hook
echo '{"method":"hook","params":{"event":"PostToolUse","toolName":"python_greet","toolInput":"{}","sessionId":"s1","channel":"cli","chatId":"c1"}}' | python3 main.py

# Test enricher
echo '{"method":"enrich","params":{"enricherName":"python_env"}}' | python3 main.py

# Test deactivate
echo '{"method":"deactivate"}' | python3 main.py
```

## Debugging

- **stderr output**: Anything printed to stderr appears in xbot's log output. The example already includes debug logging in the hook handler.
- **Common pitfall**: Forgetting to flush stdout after writing. The example uses `print(json.dumps(...), flush=True)` to ensure each response is sent immediately.
- **Timeout**: Each call has a 30-second timeout. If your tool takes longer, the process will be killed.

## Extending This Example

To add a new tool:

1. Add the tool definition to `handle_activate()` → `tools` array
2. Add a new `elif` branch in `handle_execute_tool()`
3. Optionally declare it in `plugin.json` → `contributes.tools`

To add a new hook:

1. Add the hook registration to `handle_activate()` → `hooks` array
2. Add handling logic in `handle_hook()`
3. Optionally declare it in `plugin.json` → `contributes.hooks`
