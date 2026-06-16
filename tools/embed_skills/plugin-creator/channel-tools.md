# Channel-Scoped Tools

Channel plugins can declare tools that are automatically injected into agent
sessions **only for that channel**. This enables use cases like:

- **GitHub App plugin**: declares `get_pr_diff`, `post_review_comment` — agent
  only sees these tools when processing GitHub webhook events
- **Slack plugin**: declares `send_slack_modal`, `open_thread` — only visible
  in Slack sessions
- **Any integration plugin**: tools that use the channel's API credentials,
  without polluting other channels' tool lists

## How It Works

```
1. Channel process starts → sends "channel_tools" declaration
2. xbot registers tools via Registry.RegisterForChannel(name, bridge)
3. When agent processes a message from this channel:
   → sessionKey = "channel:chatID"
   → AsDefinitionsForSession auto-includes channel tools
   → LLM sees and can call them
4. LLM calls a channel tool → xbot sends "execute_tool" RPC → channel process executes
```

The tools are **always visible** for their channel (like core tools), no
`load_tools` activation needed. They are invisible to all other channels.

## Protocol

### Declaring Tools: `channel_tools` message

After receiving `channel_config`, send a `channel_tools` message on stdout:

```json
{
  "type": "channel_tools",
  "tools": [
    {
      "name": "get_pr_diff",
      "description": "Get the diff of a pull request from GitHub",
      "parameters": [
        {"name": "repo", "type": "string", "description": "Repository (owner/repo)", "required": true},
        {"name": "pr_number", "type": "integer", "description": "PR number", "required": true}
      ]
    },
    {
      "name": "post_review_comment",
      "description": "Post a code review comment on a pull request",
      "parameters": [
        {"name": "repo", "type": "string", "description": "Repository (owner/repo)", "required": true},
        {"name": "pr_number", "type": "integer", "description": "PR number", "required": true},
        {"name": "body", "type": "string", "description": "Review comment in markdown", "required": true},
        {"name": "event", "type": "string", "description": "Review type: APPROVE, REQUEST_CHANGES, COMMENT", "required": false}
      ]
    }
  ]
}
```

**Hot-update**: Sending a new `channel_tools` message **replaces** the entire
tool set. This is useful for dynamic tool discovery (e.g., different tools per
event type).

### Tool Execution: `execute_tool` RPC

When the LLM calls a channel tool, xbot sends an RPC request:

```json
{
  "type": "rpc",
  "id": "srv-1",
  "method": "execute_tool",
  "params": {
    "name": "get_pr_diff",
    "input": "{\"repo\":\"octocat/Hello-World\",\"pr_number\":42}"
  }
}
```

The channel process executes the tool and responds:

```json
{
  "id": "srv-1",
  "result": {
    "content": "diff --git a/main.go b/main.go\n...",
    "is_error": false
  }
}
```

**Error handling** — two levels:

```json
// Tool-level error (LLM sees it and can retry/adapt):
{"id":"srv-1","result":{"content":"GitHub API rate limit exceeded","is_error":true}}

// RPC-level error (tool infrastructure failure):
{"id":"srv-1","error":"tool not found: unknown_tool"}
```

## Parameter Types

Use standard JSON Schema types for parameters:

| Type | Example |
|------|---------|
| `string` | Text input |
| `integer` | Whole number |
| `boolean` | True/false |
| `array` | List (use `items` for element schema) |

Each parameter has: `name`, `type`, `description`, `required` (bool).

## Complete Example: GitHub App Auto-CR Plugin

```python
#!/usr/bin/env python3
"""GitHub App channel plugin with channel-scoped tools.

When someone @mentions the bot on a PR, this plugin:
1. Forwards the event to xbot as an inbound message
2. Declares tools the agent can use to interact with GitHub
3. Executes tool calls (fetch diff, post review) via GitHub API
"""
import sys, json, os
from http.server import HTTPServer, BaseHTTPRequestHandler

GITHUB_TOKEN = ""

def write_stdout(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def send_inbound(chat_id, content, sender_name):
    write_stdout({
        "id": f"inbound-{id(content)}",
        "method": "send_inbound",
        "params": {
            "channel": "github",
            "chat_id": chat_id,
            "content": content,
            "sender_id": chat_id,
            "sender_name": sender_name,
            "chat_type": "group",
        }
    })

def declare_tools():
    """Declare channel-scoped tools after receiving channel_config."""
    write_stdout({
        "type": "channel_tools",
        "tools": [
            {
                "name": "get_pr_diff",
                "description": "Get the diff of a pull request",
                "parameters": [
                    {"name": "repo", "type": "string", "description": "owner/repo", "required": True},
                    {"name": "pr_number", "type": "integer", "description": "PR number", "required": True},
                ]
            },
            {
                "name": "post_review_comment",
                "description": "Post a code review on a pull request",
                "parameters": [
                    {"name": "repo", "type": "string", "description": "owner/repo", "required": True},
                    {"name": "pr_number", "type": "integer", "description": "PR number", "required": True},
                    {"name": "body", "type": "string", "description": "Review content in markdown", "required": True},
                    {"name": "event", "type": "string", "description": "APPROVE, REQUEST_CHANGES, or COMMENT", "required": False},
                ]
            },
        ]
    })

def execute_tool(name, input_str):
    """Execute a tool call and return (content, is_error)."""
    import urllib.request
    params = json.loads(input_str)
    repo = params.get("repo", "")
    pr = params.get("pr_number", 0)

    if name == "get_pr_diff":
        url = f"https://api.github.com/repos/{repo}/pulls/{pr}"
        req = urllib.request.Request(url, headers={
            "Authorization": f"token {GITHUB_TOKEN}",
            "Accept": "application/vnd.github.v3.diff",
        })
        try:
            with urllib.request.urlopen(req) as resp:
                return resp.read().decode()[:50000], False  # truncate large diffs
        except Exception as e:
            return f"GitHub API error: {e}", True

    elif name == "post_review_comment":
        url = f"https://api.github.com/repos/{repo}/pulls/{pr}/reviews"
        body = json.dumps({
            "body": params.get("body", ""),
            "event": params.get("event", "COMMENT"),
        }).encode()
        req = urllib.request.Request(url, data=body, headers={
            "Authorization": f"token {GITHUB_TOKEN}",
            "Accept": "application/vnd.github.v3+json",
            "Content-Type": "application/json",
        })
        try:
            with urllib.request.urlopen(req) as resp:
                return "Review posted successfully", False
        except Exception as e:
            return f"Failed to post review: {e}", True

    return f"Unknown tool: {name}", True

# ---- Webhook handler ----

class WebhookHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode()
        event = json.loads(body)

        # Only handle PR comment events where bot is mentioned
        if event.get("action") == "created" and "comment" in event:
            comment_body = event["comment"].get("body", "")
            if "@code-reviewer" not in comment_body:
                self.send_response(200)
                self.end_headers()
                return

            repo = event["repository"]["full_name"]
            pr_number = event["issue"]["number"]
            sender = event["comment"]["user"]["login"]
            chat_id = f"{repo}-pr-{pr_number}"

            content = f"Please review PR #{pr_number} in {repo}. Comment from {sender}: {comment_body}"
            send_inbound(chat_id, content, sender)

        self.send_response(200)
        self.end_headers()

    def log_message(self, *a): pass

# ---- Main loop ----

for line in sys.stdin:
    msg = json.loads(line.strip())

    # Phase 1: Activation — declare channel provider
    if msg.get("method") == "activate":
        write_stdout({"result": "ok", "channel_provider": {
            "name": "github",
            "config_schema": [
                {"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "false"},
                {"key": "webhook_port", "label": "Webhook Port", "type": "number", "default_value": "9876"},
                {"key": "github_token", "label": "GitHub Token", "type": "password", "default_value": ""},
            ]
        }})

    # Phase 2: Channel config — init and declare tools
    elif msg.get("type") == "channel_config":
        config = json.loads(msg.get("metadata", {}).get("config", "{}"))
        GITHUB_TOKEN = config.get("github_token", os.environ.get("GITHUB_TOKEN", ""))
        port = int(config.get("webhook_port", "9876"))

        # Declare tools so agent can interact with GitHub
        declare_tools()

        # Start webhook server
        import threading
        server = HTTPServer(("0.0.0.0", port), WebhookHandler)
        threading.Thread(target=server.serve_forever, daemon=True).start()

    # Phase 3: Tool execution request from xbot
    elif msg.get("method") == "execute_tool":
        tool_name = msg.get("params", {}).get("name", "")
        tool_input = msg.get("params", {}).get("input", "{}")
        content, is_error = execute_tool(tool_name, tool_input)
        write_stdout({
            "id": msg["id"],
            "result": {"content": content, "is_error": is_error}
        })
```

## plugin.json for Channel + Tools

```json
{
  "id": "my.github-reviewer",
  "name": "GitHub Code Reviewer",
  "version": "1.0.0",
  "description": "Auto code review via GitHub App",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "permissions": ["channels.register"],
  "contributes": {
    "channelProvider": {
      "name": "github",
      "config_schema": [
        {"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "false"},
        {"key": "webhook_port", "label": "Webhook Port", "type": "number", "default_value": "9876"},
        {"key": "github_token", "label": "GitHub Token", "type": "password", "default_value": ""}
      ]
    }
  }
}
```

Note: the `channel_tools` protocol message is **not** declared in plugin.json.
It is sent dynamically at runtime by the channel process after receiving
`channel_config`. This allows the tool set to adapt to runtime conditions.

## Design Notes

- **Channel tools are always visible** for their channel (like core tools).
  They do NOT require `load_tools` activation.
- **Tool name conflicts**: if a channel tool has the same name as a global
  tool, the channel version takes priority within that channel's sessions.
- **Lifecycle**: channel tools are automatically cleaned up when the channel
  process stops (`ChannelPluginTransport.Stop()` calls
  `Registry.UnregisterChannelTools`).
- **Hot-update**: send a new `channel_tools` message at any time to replace
  the entire tool set. Old tools are unregistered before new ones are added.
