---
title: "快速上手：Stdio 插件"
weight: 4
---

用**任意语言**构建插件，通过 stdin/stdout 上的换行分隔 JSON（NDJSON）与 xbot 通信。本篇基于真实示例 `plugin/examples/grpc-python/main.py`——只用 Python 标准库，零依赖。

stdio 运行时（`runtime: "stdio"`，向后兼容 `"grpc"`）拉起你的进程，使用 `plugin/protocol/protocol.go` 定义的请求/响应协议：xbot 每行发送一个 JSON 对象，你每行回一个 JSON 对象，每次响应后必须 flush。

## 第一步：用 Python 实现协议

```python
#!/usr/bin/env python3
import sys, json
from datetime import datetime

def handle_activate(params):
    """声明能力：工具、Hook、注入器。"""
    return {
        "tools": [
            {
                "name": "python_greet",
                "description": "Greet someone by name.",
                "parameters": [
                    {"name": "name", "type": "string", "description": "The person to greet", "required": True}
                ],
                "inputSchema": {
                    "type": "object",
                    "properties": {"name": {"type": "string", "description": "The person to greet"}},
                    "required": ["name"],
                },
            },
            {"name": "python_time", "description": "Get current server time.", "parameters": []},
        ],
        "hooks": [{"event": "PostToolUse", "matcher": "python_*"}],
        "enrichers": [{"name": "python_env"}],
    }

def handle_execute_tool(params):
    tool_name = params.get("toolName", "")
    input_data = json.loads(params.get("input", "{}") or "{}")
    if tool_name == "python_greet":
        return {"result": json.dumps({"english": f"Hello, {input_data.get('name', 'World')}!"})}
    if tool_name == "python_time":
        return {"result": json.dumps({"iso": datetime.now().isoformat()})}
    return {"error": f"Unknown tool: {tool_name}"}

def handle_hook(params):
    return {"hookResult": {"decision": "allow"}}

def handle_enrich(params):
    if params.get("enricherName") == "python_env":
        import platform
        return {"result": json.dumps({"python_version": platform.python_version()})}
    return {"error": "Unknown enricher"}

HANDLERS = {
    "activate": handle_activate,
    "deactivate": lambda _p: {},
    "execute_tool": handle_execute_tool,
    "hook": handle_hook,
    "enrich": handle_enrich,
}

def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        request = json.loads(line)
        handler = HANDLERS.get(request.get("method", ""))
        if handler is None:
            response = {"error": f"Unknown method: {request.get('method')}"}
        else:
            try:
                response = handler(request.get("params", {}))
            except Exception as e:
                response = {"error": str(e)}
        # 响应写成单行 JSON 并立即 flush
        print(json.dumps(response), flush=True)

if __name__ == "__main__":
    main()
```

## 第二步：编写清单

```json
{
  "id": "com.example.python-hello",
  "name": "Python Hello",
  "version": "1.0.0",
  "description": "Example Python plugin demonstrating the JSON/stdio protocol",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich"],
  "contributes": {
    "tools": [
      { "name": "python_greet", "description": "Greet someone by name." },
      { "name": "python_time", "description": "Get current server time." }
    ],
    "hooks": [ { "event": "PostToolUse", "matcher": "python_*" } ],
    "contextEnrichers": [ { "name": "python_env", "description": "Inject Python runtime environment info" } ]
  }
}
```

## 第三步：重启并测试

重启 xbot。agent 现在可以调用 `python_greet` 与 `python_time`；每次 `python_*` 工具完成都会触发你的 Hook；`python_env` 注入器被注入系统提示词。

## 线上协议

来自 xbot 的请求（`plugin/protocol/protocol.go:43`）：

```json
{"method":"activate","params":{"pluginId":"com.example.python-hello"}}
{"method":"execute_tool","params":{"toolName":"python_greet","input":"{\"name\":\"Bob\"}"}}
{"method":"hook","params":{"event":"PostToolUse","toolName":"python_greet","toolInput":"..."}}
{"method":"enrich","params":{"enricherName":"python_env"}}
{"method":"deactivate","params":{}}
```

你的响应只填充与方法相关的字段（`protocol.Response`）：

```json
{"tools":[{"name":"python_greet","description":"...","parameters":[...]}]}
{"result":"{\"english\":\"Hello, Bob!\"}"}
{"hook_result":{"decision":"allow"}}
{"enrichers":[{"name":"python_env"}]}
```

## 路上须知

1. **每行一个 JSON 对象，每次响应后 flush。** 缓冲输出会让调用方死锁直至超时。
2. **日志写 stderr** —— 会被捕获进 xbot 日志；stdout 仅承载协议。
3. **`execute_tool` 的 `params.input` 是 JSON 字符串**，不是对象。自行解析。
4. **超时**：操作受清单 `timeout` 字段约束（Go duration 字符串，如 `"30s"`；默认 `DefaultPluginTimeout = 30s`，`plugin/plugin.go:723`）。超时即杀进程。

## 偏爱 Go？使用规范结构体

`plugin/protocol` 导出类型化结构体，Go stdio 插件无需手写 JSON：

```go
h := &protocol.Handler{
	Activate: func(req *protocol.ActivateParams) (*protocol.ActivateResult, error) {
		return &protocol.ActivateResult{Tools: []protocol.ToolDef{{Name: "greet", Description: "Greet someone"}}}, nil
	},
	ExecuteTool: func(params *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) {
		return &protocol.ExecuteToolResult{Result: `{"msg":"Hello!"}`}, nil
	},
}
protocol.Run(h) // 读 stdin、分发、写 stdout
```

内置的 `xbot.git-fancy` 后端正是这个模式（`plugins/xbot-git-fancy/main.go`）。

下一篇：[Stdio 插件](../stdio-plugins/) 完整协议参考。
