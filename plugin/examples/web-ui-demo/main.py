#!/usr/bin/env python3
"""Web UI Demo channel plugin — demonstrates the web_ui protocol.

Protocol (JSON lines over stdio):
  xbot → plugin:  {"type":"channel_config","metadata":{"config":"{...}"}}  (startup)
  xbot → plugin:  {"id":"srv-N","method":"web_ui_action","params":{...}}   (interaction)
  plugin → xbot:  {"type":"web_ui","ui":[...]}                              (component declarations, hot-update)
  plugin → xbot:  {"id":"srv-N","result":{...}}                             (RPC response)

On startup this plugin declares three web components:
  1. ci-monitor   — sparkline (build durations) in the right sidebar
  2. build-table  — table of recent builds (right sidebar)
  3. deploy-badge — status badge in the title bar

Handling web_ui_action: clicking the table's "retry" cell triggers a
web_ui_action with action="retry"; the plugin responds and pushes a fresh
web_ui declaration (simulating an updated component state).
"""
import json
import sys
import threading
import time

channel_name = "webui-demo"

lock = threading.Lock()
rpc_id = 0
builds = [
    {"id": "#128", "status": "success", "by": "alice", "dur": "2m12s"},
    {"id": "#127", "status": "failed", "by": "bob", "dur": "1m48s"},
    {"id": "#126", "status": "success", "by": "carol", "dur": "3m01s"},
    {"id": "#125", "status": "running", "by": "dave", "dur": "—"},
]


def write_stdout(obj):
    line = json.dumps(obj, ensure_ascii=False)
    with lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def send_web_ui():
    """Declare web UI components (hot-update replaces the previous set)."""
    write_stdout({
        "type": "web_ui",
        "ui": [
            {
                "widget_id": "ci-monitor",
                "title": "CI Duration Trend",
                "slot": "right_sidebar",
                "refresh": "10s",
                "component": {
                    "type": "sparkline",
                    "props": {"data": [120, 95, 132, 88, 150, 110, 128], "color": "#22c55e"},
                },
            },
            {
                "widget_id": "build-table",
                "title": "Recent Builds",
                "slot": "right_sidebar",
                "component": {
                    "type": "table",
                    "props": {
                        "columns": ["id", "status", "by", "dur", "action"],
                        "rows": [
                            {
                                "id": b["id"],
                                "status": {"text": "✓" if b["status"] == "success" else ("✗" if b["status"] == "failed" else "⏳"), "tone": b["status"]},
                                "by": b["by"],
                                "dur": b["dur"],
                                "action": "retry" if b["status"] == "failed" else "",
                            }
                            for b in builds
                        ],
                    },
                },
            },
            {
                "widget_id": "deploy-badge",
                "slot": "title_bar_right",
                "component": {
                    "type": "badge",
                    "props": {"text": "✓ 构建通过", "tone": "success", "pulse": False},
                },
            },
        ],
    })


def handle_web_ui_action(params):
    """Handle a web_ui_action from the web frontend."""
    widget_id = params.get("widgetId", "")
    action = params.get("action", "")
    data = params.get("data", "")
    if widget_id == "build-table" and action == "retry":
        # Simulate re-triggering a failed build and pushing an updated state.
        for b in builds:
            if b["id"] == "#127":
                b["status"] = "running"
        send_web_ui()
        return {"result": "retry triggered for #127"}
    if widget_id == "deploy-badge" and action == "toggle":
        return {"result": "deploy badge clicked"}
    return {"result": f"handled {action} on {widget_id} ({data})"}


def handle_xbot_rpc(req_id, method, params):
    """Handle an RPC request from xbot (JSON-RPC over stdin)."""
    if method == "web_ui_action":
        result = handle_web_ui_action(params or {})
        write_stdout({"id": req_id, "result": result})
    elif method == "execute_tool":
        write_stdout({"id": req_id, "result": {"content": "demo tool executed", "is_error": False}})
    else:
        write_stdout({"id": req_id, "error": f"Unknown method: {method}"})


def handle_xbot_event(msg):
    """Handle a WSMessage event from xbot."""
    if msg.get("type") == "channel_config":
        # Startup: declare web UI components once the channel is live.
        time.sleep(0.2)
        send_web_ui()


def main():
    print(f"[{channel_name}] web UI demo plugin starting...", file=sys.stderr)
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        if "id" in msg and "method" in msg:
            handle_xbot_rpc(msg["id"], msg["method"], msg.get("params"))
        elif "type" in msg:
            handle_xbot_event(msg)
        else:
            # Plugin → xbot RPC response / unknown — ignore.
            pass
    print(f"[{channel_name}] stdin closed, shutting down", file=sys.stderr)


if __name__ == "__main__":
    main()
