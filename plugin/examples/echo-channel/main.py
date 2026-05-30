#!/usr/bin/env python3
"""
xbot Echo Channel Plugin — JSON-RPC over stdio

Demonstrates the new gRPC transport protocol where the plugin acts as a
full RPC client. The plugin:

1. Receives WSMessage events from xbot via stdin (progress, replies, etc.)
2. Sends JSON-RPC requests to xbot via stdout (send_inbound, get_history, etc.)
3. Runs an HTTP echo server that forwards messages to/from xbot
4. Stores conversation history in memory and serves it via GET /history

Protocol format (same as WebSocket):
  - xbot → plugin (event push):  {"type":"progress","progress":{...}}
  - xbot → plugin (RPC request): {"id":"1","method":"channel_send","params":{...}}
  - xbot → plugin (RPC response):{"id":"1","result":"ok"}
  - plugin → xbot (RPC request): {"id":"1","method":"send_inbound","params":{...}}
  - plugin → xbot (RPC response):{"id":"1","result":{...}}

Endpoints:
  POST /message          — send message to xbot
  POST /message?chat_id= — send message with custom chat_id
  GET  /message?q=hello  — send message via query param
  GET  /history          — list conversation history (all chats)
  GET  /history?chat_id= — list history for specific chat
  GET  /health           — health check
"""

import sys
import json
import threading
import time
from collections import defaultdict
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import parse_qs, urlparse

# ---------------------------------------------------------------------------
# Global state
# ---------------------------------------------------------------------------

config = {}
rpc_id = 0
lock = threading.Lock()
channel_name = "echo"

# Conversation history: chat_id → list of {role, content, time, ...}
history = defaultdict(list)
history_max = 200  # max messages per chat

# ---------------------------------------------------------------------------
# stdio JSON-RPC helpers
# ---------------------------------------------------------------------------

def write_stdout(obj):
    """Write a JSON object to stdout (to xbot). Thread-safe."""
    line = json.dumps(obj, ensure_ascii=False)
    with lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def next_rpc_id():
    """Generate a unique RPC request ID."""
    global rpc_id
    rpc_id += 1
    return f"plugin-{rpc_id}"


def send_rpc_request(method, params=None):
    """Send an RPC request to xbot."""
    req = {"id": next_rpc_id(), "method": method}
    if params:
        req["params"] = params
    write_stdout(req)


def send_rpc_response(req_id, result=None, error=None):
    """Send an RPC response back to xbot."""
    resp = {"id": req_id}
    if error:
        resp["error"] = error
    else:
        resp["result"] = result if result is not None else "ok"
    write_stdout(resp)


# ---------------------------------------------------------------------------
# Conversation history
# ---------------------------------------------------------------------------

def add_to_history(chat_id, role, content, **extra):
    """Record a message in the conversation history."""
    entry = {
        "role": role,
        "content": content,
        "time": time.strftime("%Y-%m-%d %H:%M:%S"),
    }
    entry.update(extra)
    with lock:
        h = history[chat_id]
        h.append(entry)
        # Trim old messages if exceeding max
        if len(h) > history_max:
            history[chat_id] = h[-history_max:]


def get_history(chat_id=None):
    """Get conversation history. If chat_id is None, return all chats."""
    with lock:
        if chat_id:
            return list(history.get(chat_id, []))
        return {k: list(v) for k, v in history.items()}


# ---------------------------------------------------------------------------
# Inbound: push user messages to xbot
# ---------------------------------------------------------------------------

def send_inbound_message(chat_id, content, sender_id="http_user", sender_name="HTTP User"):
    """Send a user message to xbot via the send_inbound RPC method."""
    # Record it locally
    add_to_history(chat_id, "user", content,
                   sender_id=sender_id, sender_name=sender_name)
    # Send to xbot
    send_rpc_request("send_inbound", {
        "channel": channel_name,
        "chat_id": chat_id,
        "content": content,
        "sender_id": sender_id,
        "sender_name": sender_name,
        "chat_type": "p2p",
    })


# ---------------------------------------------------------------------------
# Handle old-style plugin protocol (activation) + new-style events
# ---------------------------------------------------------------------------

def handle_activate(params):
    """Declare channel provider capability."""
    return {
        "channel_provider": {
            "name": channel_name,
            "config_schema": config.get("config_schema", []),
        }
    }


HANDLERS = {"activate": handle_activate}


def handle_plugin_request(request):
    """Handle an old-style plugin request from xbot (method, no id)."""
    method = request.get("method", "")
    params = request.get("params", {})
    handler = HANDLERS.get(method)
    if handler:
        try:
            response = handler(params)
        except Exception as e:
            response = {"error": str(e)}
    else:
        response = {"error": f"Unknown method: {method}"}
    write_stdout(response)


# ---------------------------------------------------------------------------
# Handle new-style JSON-RPC messages
# ---------------------------------------------------------------------------

def handle_incoming(raw_line):
    """Route an incoming JSON line from xbot."""
    try:
        msg = json.loads(raw_line)
    except json.JSONDecodeError:
        return

    msg_id = msg.get("id", "")
    msg_type = msg.get("type", "")
    msg_method = msg.get("method", "")

    # 1. RPC request from xbot (has id + method) → handle and respond
    if msg_id and msg_method:
        handle_xbot_rpc(msg_id, msg_method, msg.get("params", {}))
        return

    # 2. RPC response from xbot (has id, no method)
    if msg_id and not msg_method:
        return

    # 3. Old-style plugin request (has method, no id)
    if msg_method and not msg_id:
        handle_plugin_request(msg)
        return

    # 4. Event push from xbot (has type)
    if msg_type:
        handle_xbot_event(msg)


def handle_xbot_rpc(req_id, method, params):
    """Handle an RPC request from xbot."""
    if method == "channel_send":
        content = params.get("content", "")
        chat_id = params.get("chat_id", "")
        if content:
            add_to_history(chat_id, "assistant", content)
        send_rpc_response(req_id, result="ok")
    else:
        send_rpc_response(req_id, error=f"Unknown method: {method}")


def handle_xbot_event(msg):
    """Handle a WSMessage event from xbot."""
    msg_type = msg.get("type", "")
    content = msg.get("content", "")
    progress = msg.get("progress", {})
    chat_id = msg.get("chat_id", "")
    meta = msg.get("metadata", {})

    if msg_type == "channel_config":
        if "config" in (meta or {}):
            try:
                config.update(json.loads(meta["config"]))
            except json.JSONDecodeError:
                pass
        start_http_server()

    elif msg_type == "text":
        # Only store final agent replies (marked by transport).
        # Status/error/progress text messages are NOT stored.
        is_final = (meta or {}).get("is_final", "") == "true"
        if is_final and content:
            add_to_history(chat_id, "assistant", content)


# ---------------------------------------------------------------------------
# HTTP server
# ---------------------------------------------------------------------------

http_server = None


def _json_response(handler, data, status=200):
    """Send a JSON response."""
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Access-Control-Allow-Origin", "*")
    handler.end_headers()
    handler.wfile.write(json.dumps(data, ensure_ascii=False, indent=2).encode("utf-8"))


class EchoHandler(BaseHTTPRequestHandler):
    """HTTP handler for the echo channel."""

    def do_GET(self):
        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)

        # ── /message?q=hello ──────────────────────────────────
        if parsed.path == "/message":
            content = params.get("q", [""])[0]
            chat_id = params.get("chat_id", ["http_default"])[0]
            if content:
                send_inbound_message(chat_id, content)
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"Message sent!\n")
            else:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Missing 'q' parameter\n")

        # ── /history — list conversation history ──────────────────
        elif parsed.path == "/history":
            chat_id = params.get("chat_id", [None])[0]
            result = get_history(chat_id)
            _json_response(self, result)

        # ── /health ───────────────────────────────────────────
        elif parsed.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            self.wfile.write(b"ok\n")

        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)

        if parsed.path == "/message":
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length).decode("utf-8", errors="replace")
            chat_id = params.get("chat_id", ["http_default"])[0]
            if body.strip():
                send_inbound_message(chat_id, body.strip())
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"Message sent!\n")
            else:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Empty body\n")
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass


def start_http_server():
    """Start the HTTP echo server in a background thread."""
    global http_server
    port = int(config.get("port", "9876"))
    try:
        http_server = HTTPServer(("0.0.0.0", port), EchoHandler)
        thread = threading.Thread(target=http_server.serve_forever, daemon=True)
        thread.start()
        print(f"[echo] HTTP server started on port {port}", file=sys.stderr)
    except Exception as e:
        print(f"[echo] Failed to start HTTP server: {e}", file=sys.stderr)


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------

def main():
    print("[echo] Echo channel plugin starting...", file=sys.stderr)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        handle_incoming(line)
    print("[echo] stdin closed, shutting down", file=sys.stderr)
    if http_server:
        http_server.shutdown()


if __name__ == "__main__":
    main()
