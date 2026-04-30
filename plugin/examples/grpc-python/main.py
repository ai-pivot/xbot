#!/usr/bin/env python3
"""
xbot Python Plugin Example — JSON/stdio Protocol Implementation

Demonstrates: tool registration, hook handling, context enrichment.
Uses only the Python standard library (sys, json, datetime).

Protocol: NDJSON (newline-delimited JSON) over stdin/stdout.
See ../../PROTOCOL.md for the full protocol specification.
"""

import sys
import json
from datetime import datetime


def handle_activate(params):
    """Declare all plugin capabilities: tools, hooks, and enrichers."""
    return {
        "tools": [
            {
                "name": "python_greet",
                "description": "Greet someone by name. Returns greetings in multiple languages.",
                "parameters": [
                    {
                        "name": "name",
                        "type": "string",
                        "description": "The person to greet",
                        "required": True,
                    }
                ],
                "inputSchema": {
                    "type": "object",
                    "properties": {
                        "name": {"type": "string", "description": "The person to greet"}
                    },
                    "required": ["name"],
                },
            },
            {
                "name": "python_time",
                "description": "Get current server time with timezone info.",
                "parameters": [],
                "inputSchema": {"type": "object", "properties": {}},
            },
        ],
        "hooks": [{"event": "PostToolUse", "matcher": "python_*"}],
        "enrichers": [{"name": "python_env"}],
    }


def handle_deactivate(_params):
    """Clean up resources. Response is ignored by xbot."""
    print("[python-hello] Deactivating", file=sys.stderr)
    return {}


def handle_execute_tool(params):
    """Dispatch tool execution by name."""
    tool_name = params.get("toolName", "")
    input_str = params.get("input", "{}")

    try:
        input_data = json.loads(input_str) if input_str else {}
    except json.JSONDecodeError as e:
        return {"error": f"Invalid JSON input: {e}"}

    if tool_name == "python_greet":
        name = input_data.get("name", "World")
        greetings = {
            "english": f"Hello, {name}!",
            "chinese": f"你好，{name}！",
            "japanese": f"こんにちは、{name}！",
            "french": f"Bonjour, {name}!",
            "spanish": f"¡Hola, {name}!",
            "timestamp": datetime.now().isoformat(),
        }
        return {"result": json.dumps(greetings, ensure_ascii=False)}

    elif tool_name == "python_time":
        now = datetime.now()
        return {
            "result": json.dumps(
                {
                    "iso": now.isoformat(),
                    "date": now.strftime("%Y-%m-%d"),
                    "time": now.strftime("%H:%M:%S"),
                    "weekday": now.strftime("%A"),
                }
            )
        }

    else:
        return {"error": f"Unknown tool: {tool_name}"}


def handle_hook(params):
    """Handle lifecycle hook events."""
    event = params.get("event", "")
    tool_name = params.get("toolName", "")

    # Log to stderr for debugging (appears in xbot's log output)
    print(
        f"[python-hello] Hook event={event} tool={tool_name}",
        file=sys.stderr,
    )

    if event == "PostToolUse" and tool_name.startswith("python_"):
        return {
            "hookResult": {
                "decision": "allow",
                "message": f"Python tool {tool_name} completed",
            }
        }

    # Default: allow
    return {"hookResult": {"decision": "allow"}}


def handle_enrich(params):
    """Return dynamic content for system prompt injection."""
    import platform

    enricher_name = params.get("enricherName", "")

    if enricher_name == "python_env":
        info = {
            "python_version": platform.python_version(),
            "platform": platform.system(),
            "architecture": platform.machine(),
            "plugin": "python-hello v1.0.0",
        }
        return {"result": json.dumps(info)}
    else:
        return {"error": f"Unknown enricher: {enricher_name}"}


# Method dispatch table — maps method names to handler functions.
HANDLERS = {
    "activate": handle_activate,
    "deactivate": handle_deactivate,
    "execute_tool": handle_execute_tool,
    "hook": handle_hook,
    "enrich": handle_enrich,
}


def main():
    """
    Main loop: read JSON lines from stdin, dispatch to handler, write JSON lines to stdout.

    Protocol: NDJSON (one JSON object per line, terminated by newline).
    IMPORTANT: stdout must be flushed after every response.
    """
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        # Parse the incoming request
        try:
            request = json.loads(line)
        except json.JSONDecodeError as e:
            response = {"error": f"Invalid JSON: {e}"}
            print(json.dumps(response), flush=True)
            continue

        method = request.get("method", "")
        params = request.get("params", {})

        # Dispatch to the appropriate handler
        handler = HANDLERS.get(method)
        if handler is None:
            response = {"error": f"Unknown method: {method}"}
        else:
            try:
                response = handler(params)
            except Exception as e:
                print(f"[python-hello] Error in {method}: {e}", file=sys.stderr)
                response = {"error": str(e)}

        # Write response as single-line JSON and flush immediately
        print(json.dumps(response), flush=True)


if __name__ == "__main__":
    main()
