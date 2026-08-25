---
title: "Stdio 插件"
weight: 8
---

Stdio 插件是通过 stdin/stdout 传输 NDJSON 的外部进程。运行时声明为 `"stdio"`（向后兼容 `"grpc"`），实现分为 `plugin/runtime.go`（进程管理）+ `plugin/protocol/protocol.go`（线上类型）。示例：`plugin/examples/grpc-python/main.py`（Python）、`plugins/xbot-git-fancy/main.go`（Go，生产级）。

## 进程生命周期

`stdioPlugin.Activate`（`plugin/runtime.go:114`）通过 `startPluginProcess(entry, executable, args, dir)` 拉起进程。清单字段：

| 字段 | 含义 |
|---|---|
| `entry` | 启动命令（如 `"python3 main.py"`） |
| `executable` | 覆盖 `entry`（安全：防止 shell 解释） |
| `args` | 追加给可执行文件的参数 |
| `timeout` | Go duration 字符串；`DefaultPluginTimeout = 30s`；超时即杀进程 |

单个 `readLoop` goroutine 解复用 stdout（`StdioPluginProcess`，`plugin/runtime.go:70`）：

- 带 `"method"` 无 `"id"` 的行 → **入站推送消息** → `InboundHandler`；
- 带 `"result"`/`"error"` 的行 → 对挂起 `Call()` 请求的响应。

## 线上协议（Request/Response）

`protocol.Request`（xbot → 插件）：

```json
{"method":"activate","params":{"pluginId":"com.example.plugin"}}
{"method":"execute_tool","params":{"toolName":"t","input":"{\"k\":\"v\"}"}}
{"method":"hook","params":{"event":"PostToolUse","toolName":"t","toolInput":"...","sessionId":"...","channel":"web","chatId":"..."}}
{"method":"enrich","params":{"enricherName":"env"}}
{"method":"web_plugin_rpc","params":{"method":"git.status","params":{...}}}
{"method":"web_ui_action","params":{"widgetId":"w","action":"click","data":"{}","chatId":"..."}}
{"method":"deactivate","params":{}}
```

`protocol.Response`（插件 → xbot）——只填充与方法相关的字段：

```json
{"tools":[{"name":"t","description":"...","parameters":[{"name":"k","type":"string","required":true}],"inputSchema":{...}}]}
{"hooks":[{"event":"PostToolUse","matcher":"t*"}]}
{"enrichers":[{"name":"env"}]}
{"result":"<工具输出字符串>"}
{"error":"<错误信息>"}
{"hook_result":{"decision":"allow"}}
{"channel_provider":{"name":"echo","config_schema":[...]}}
```

`protocol.HookResult.Decision` 取值：`allow`、`deny`、`ask`、`defer`；`Message` 承载说明。

## Go 处理器 API

`protocol.Handler`（`plugin/protocol/protocol.go:200`）是填空式 API：

```go
h := &protocol.Handler{
	Activate: func(req *protocol.ActivateParams) (*protocol.ActivateResult, error) { ... },
	ExecuteTool: func(p *protocol.ExecuteToolParams) (*protocol.ExecuteToolResult, error) { ... },
	Hook: func(p *protocol.HookParams) (*protocol.HookResult, error) { ... },
	Enrich: func(p *protocol.EnrichParams) (*protocol.EnrichResult, error) { ... },
	WebUIAction: func(p *protocol.WebUIActionParams) (*protocol.WebUIActionResult, error) { ... },
	WebPluginRPC: func(p *protocol.WebPluginRPCParams) (*protocol.WebPluginRPCResult, error) { ... },
	Deactivate: func(p *protocol.DeactivateParams) (*protocol.DeactivateResult, error) { ... },
}
protocol.Run(h) // 逐行读 stdin、分发、写 stdout
```

`Run` 处理循环、错误序列化与 flush。单行上限 1MB（`maxLineSize`，`protocol.go:442`）。

## WebPluginRPC —— 前端↔后端桥

`WebPluginRPCParams` 携带 `Method`（如 `"git.status"`）+ 任意 JSON 参数，由前端经 `ctx.rpc.call('pluginId.method', ...)` 路由而来。内置 `git-fancy` 后端是参考实现（`plugins/xbot-git-fancy/main.go handleWebPluginRPC`）：

```go
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) *protocol.WebPluginRPCResult {
	switch p.Method {
	case "git.status":
		return rpcOK(gitStatus(cwd))
	case "git.log":
		var params struct{ Limit *int `json:"limit"` }
		json.Unmarshal(p.Params, &params)
		return rpcOK(gitLog(cwd, limit, 0))
	default:
		return rpcErr("unknown method: " + p.Method)
	}
}
```

前端再通过 `BackendRPC` 的声明合并为这些方法定型（`web/src/plugin-api/rpc.ts`）——见 [Web 插件](../web-plugins/)。

## Python 参考实现（仅标准库）

`grpc-python/main.py` 的分发表模式：

```python
HANDLERS = {
    "activate": handle_activate,
    "deactivate": handle_deactivate,
    "execute_tool": handle_execute_tool,
    "hook": handle_hook,
    "enrich": handle_enrich,
}

def main():
    for line in sys.stdin:
        request = json.loads(line.strip())
        handler = HANDLERS.get(request.get("method", ""))
        response = handler(request.get("params", {})) if handler else {"error": "Unknown method"}
        print(json.dumps(response), flush=True)   # flush 是强制的
```

## 避坑清单

1. **永远 flush stdout。** 缓冲管道会让 xbot 的挂起 `Call()` 停滞直到超时。
2. **日志写 stderr**，绝不写 stdout——stdout 承载协议。
3. **`execute_tool.input` 是字符串。** 始终 `json.loads` 解析；容忍 `"{}"`/空串。
4. **响应字段名是 snake_case**（`hook_result`、`channel_provider`、`inputSchema`）——协议早于 JSON camelCase 约定。
5. **未知方法用 `{"error": ...}` 回应**，不要沉默——沉默看起来像进程挂起。
6. **超时会杀进程**：`manager.call()`（`plugin/manager.go`）杀掉进程并标记未运行，防止 stdout 读取阻塞造成的 goroutine 泄漏。
