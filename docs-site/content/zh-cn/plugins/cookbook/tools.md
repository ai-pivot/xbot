---
title: "工具注册"
weight: 14
---

工具是 agent 循环中可调用的函数。插件经 `PluginContext.RegisterTool`（Go）、`activate` 响应（stdio）或 `channel_tools`（渠道插件）注册工具。核心类型：`PluginTool`、`ToolDef`、`ToolResult`（`plugin/plugin.go`）。

## Go 方式

### 方式一：SimplePluginTool + BuildToolDef

`plugin/examples/hello-world/hello.go`：

```go
pctx.RegisterTool(&plugin.SimplePluginTool{
	Def: plugin.BuildToolDef("hello", "Greet someone by name. Returns a friendly greeting message.",
		plugin.ToolParamDef{Name: "name", Type: "string", Description: "The person to greet"},
	),
	ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
		name, err := plugin.ParseToolInputString(input, "name")
		if err != nil {
			name = "World"
		}
		return plugin.NewToolResult(fmt.Sprintf("Hello, %s! 👋", name)), nil
	},
})
```

`BuildToolDef(name, desc, params...)` 自动生成 JSON Schema（`ToJSONSchema`/`buildParameters`）。agent 在工具列表中看到 schema；由 LLM 决定何时调用。

### 方式二：SDK 函数适配器

```go
pctx.RegisterTool(plugin.ToolFromFunc("double", "Doubles a number",
	func(ctx context.Context, input string) (string, error) {
		return input + input, nil
	}))

pctx.RegisterTool(plugin.ToolFromJSONFunc("weather", "Get weather",
	[]plugin.ToolParamDef{{Name: "city", Type: "string"}},
	func(ctx context.Context, input json.RawMessage) (any, error) {
		var req struct{ City string `json:"city"` }
		json.Unmarshal(input, &req)
		return map[string]any{"temp": 21, "city": req.City}, nil // 自动序列化
	}))
```

### 方式三：PluginToolV2（完整上下文）

```go
type PluginToolV2 interface {
	PluginTool
	ExecuteV2(ctx context.Context, tc ToolCallContext) (*ToolResult, error)
}
```

`PluginToolBridge` 自动探测 V2 并传入 `ToolCallContext`（会话元数据、tenant、沙箱访问）。否则回退到 V1 `Execute(ctx, input)`。

## ToolResult —— 结构化输出

```go
// 简单
plugin.NewToolResult("done")
plugin.NewToolError("file not found")   // IsError() = true → 错误渲染

// 带元数据的构建器
plugin.NewResultBuilder().
	Content("Server Info\nstatus: running").
	Metadata("kind", "server-info").
	Build()

// 确定性格式化
plugin.FormatToolResult("Server Info", map[string]string{
	"status": "running", "version": "2.0.1",
})
// → "Server Info\nstatus: running\nversion: 2.0.1"  （键排序）

plugin.FormatListResult([]string{"alpha", "beta"})
// → "1. alpha\n2. beta"     （空 → "(no items)"）
```

`Metadata` 贯穿到渲染器——例如 Edit 工具把 unified diff 存在 `Metadata["diff"]` 中供精美渲染。

## stdio 方式

`activate` 响应声明工具；`execute_tool` 执行：

```json
{ "tools": [ {
    "name": "python_greet",
    "description": "Greet someone by name.",
    "parameters": [ {"name": "name", "type": "string", "description": "The person to greet", "required": true} ],
    "inputSchema": { "type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"] }
} ] }
```

```json
{ "result": "{\"english\": \"Hello, Bob!\"}" }
{ "error": "Unknown tool: xyz" }
```

`protocol.ExecuteToolParams.Input` 是原始 JSON 字符串——自行解析（`plugin/examples/grpc-python/main.py handle_execute_tool`）。

## 渠道方式

渠道插件在 `channel_config` 之后发送 `channel_tools` type 消息（见 [Channel 插件](../channel-plugins/)）。工具可声明 `channels: ["web"]` 与 `ui` 块（`tools.UIDecl`——mode/param/libs/surface），让前端对结果做特殊渲染（GenUI 面板等）。

## 命名与可见性

- 工具名用 snake_case 并注意冲突——全局命名空间与内置工具共享。名为 `read` 的插件工具会遮蔽内置工具。存疑时加插件域前缀（`git_status`）。
- 渠道限定工具（`RegisterForChannel`）只在该渠道的会话中出现——`AsDefinitionsForSession` 合并 `channelTools[channel]` + tenant 工具 + 全局工具。
- 清单 `contributes.tools[]` 声明是**文档 + 发现**；真正的注册发生在 `Activate`。

## 超时与失败

- 工具执行受清单 `timeout` 约束（默认 30s）。stdio 调用超时**杀掉进程**并标记未运行（防止 goroutine 泄漏）。
- 预期失败用 `plugin.NewToolError(msg)` 返回——agent 看到错误结果并能适应。返回 Go error 同样标记工具失败；可恢复条件优先用结构化错误结果。
