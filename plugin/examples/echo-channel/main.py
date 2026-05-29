#!/usr/bin/env python3
"""
xbot Echo Channel Plugin — JSON-RPC over stdio

Demonstrates the new gRPC transport protocol where the plugin acts as a
full RPC client. The plugin:

1. Receives WSMessage events from xbot via stdin (progress, replies, etc.)
2. Sends JSON-RPC requests to xbot via stdout (send_inbound, get_history, etc.)
3. Runs an HTTP echo server that forwards messages to/from xbot

Protocol format (same as WebSocket):
  - xbot → plugin (event push):  {"type":"progress","progress":{...}}
  - xbot → plugin (RPC request): {"id":"1","method":"channel_send","params":{...}}
  - xbot → plugin (RPC response):{"id":"1","result":"ok"}
  - plugin → xbot (RPC request): {"id":"1","method":"send_inbound","params":{...}}
  - plugin → xbot (RPC response):{"id":"1","result":{...}}

Usage:
  1. Install: cp -r echo-channel/ ~/.xbot/plugins/echo-channel/
  2. Configure: Add "echo" to config.json channels with {"enabled":"true","port":"9876"}
  3. Restart xbot
  4. Test: curl -X POST http://localhost:9876/message -d 'Hello xbot!'
"""

import sys
import json
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import parse_qs, urlparse
import time

# ---------------------------------------------------------------------------
# Global state
# ---------------------------------------------------------------------------

config = {}
rpc_id = 0
lock = threading.Lock()
channel_name = "echo"

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
    """Send an RPC request to xbot and wait for the response.

    NOTE: In a real implementation you'd track pending requests and
    match responses by ID in the read loop. This example sends fire-and-forget
    for simplicity (send_inbound doesn't need a response).
    """
    req = {
        "id": next_rpc_id(),
        "method": method,
    }
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
# Inbound: push user messages to xbot
# ---------------------------------------------------------------------------

def send_inbound_message(chat_id, content, sender_id="http_user", sender_name="HTTP User"):
    """Send a user message to xbot via the send_inbound RPC method."""
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


HANDLERS = {
    "activate": handle_activate,
}


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
    msg_result = msg.get("result", None)
    msg_error = msg.get("error", "")

    # 1. RPC request from xbot (has id + method) → handle and respond
    if msg_id and msg_method:
        handle_xbot_rpc(msg_id, msg_method, msg.get("params", {}))
        return

    # 2. RPC response from xbot (has id, no method) → deliver to pending call
    if msg_id and not msg_method:
        # In a real implementation, match by ID and deliver to waiting goroutine
        # For this example, we just log it
        if msg_error:
            print(f"[echo] RPC error for {msg_id}: {msg_error}", file=sys.stderr)
        return

    # 3. Old-style plugin request (has method, no id) → legacy protocol
    if msg_method and not msg_id:
        handle_plugin_request(msg)
        return

    # 4. Event push from xbot (has type) → handle event
    if msg_type:
        handle_xbot_event(msg)


def handle_xbot_rpc(req_id, method, params):
    """Handle an RPC request from xbot."""
    if method == "channel_send":
        # xbot wants to send a message to the HTTP client
        # In a real implementation, forward to connected HTTP clients
        content = params.get("content", "")
        chat_id = params.get("chat_id", "")
        print(f"[echo] Agent reply for {chat_id}: {content[:100]}", file=sys.stderr)
        send_rpc_response(req_id, result="ok")
    else:
        send_rpc_response(req_id, error=f"Unknown method: {method}")


def handle_xbot_event(msg):
    """Handle a WSMessage event from xbot."""
    msg_type = msg.get("type", "")
    content = msg.get("content", "")
    progress = msg.get("progress", {})
    chat_id = msg.get("chat_id", "")

    if msg_type == "channel_config":
        # Initial configuration from xbot
        meta = msg.get("metadata", {})
        if "config" in meta:
            try:
                parsed = json.loads(meta["config"])
                config.update(parsed)
            except json.JSONDecodeError:
                pass
        print(f"[echo] Received config: {config}", file=sys.stderr)
        # Start HTTP server with the configured port
        start_http_server()
        return

    if msg_type == "progress_structured":
        # Progress event — log it
        phase = progress.get("phase", "")
        message = progress.get("message", "")
        if message:
            print(f"[echo] Progress [{chat_id}]: {phase} - {message[:80]}", file=sys.stderr)
        return

    if msg_type == "stream_content":
        # Streaming content — forward to HTTP clients
        print(f"[echo] Stream [{chat_id}]: {content[:80]}", file=sys.stderr)
        return

    if msg_type == "text":
        # Final text message from agent
        print(f"[echo] Reply [{chat_id}]: {content[:100]}", file=sys.stderr)
        return

    if msg_type == "session":
        # Session state change
        session = msg.get("session", {})
        state = session.get("state", "")
        print(f"[echo] Session [{chat_id}]: {state}", file=sys.stderr)
        return

    # Ignore unknown event types
    print(f"[echo] Event [{msg_type}]: {str(msg)[:100]}", file=sys.stderr)


# ---------------------------------------------------------------------------
# HTTP echo server
# ---------------------------------------------------------------------------

http_server = None


class EchoHandler(BaseHTTPRequestHandler):
    """Simple HTTP handler that echoes messages through xbot."""

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/message":
            # GET /message?q=hello
            params = parse_qs(parsed.query)
            content = params.get("q", [""])[0]
            if content:
                send_inbound_message("http_default", content)
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"Message sent to xbot!\n")
            else:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Missing 'q' parameter\n")
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
        if parsed.path == "/message":
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length).decode("utf-8", errors="replace")
            if body:
                send_inbound_message("http_default", body)
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(b"Message sent to xbot!\n")
            else:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Empty body\n")
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        """Suppress default HTTP logging."""
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
    """
    Main loop: read JSON lines from stdin (from xbot), route them.

    This handles both:
    - Old-style plugin protocol (activate, etc.) — for initial activation
    - New-style WS JSON-RPC protocol — for channel communication
    """
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
