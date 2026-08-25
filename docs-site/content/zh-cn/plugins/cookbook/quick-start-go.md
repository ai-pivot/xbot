---
title: "快速上手：Go 插件"
weight: 3
---

构建一个带两个工具、一个 Hook 和一个上下文注入器的原生 Go 插件。本篇基于真实示例 `plugin/examples/hello-world/hello.go`。

原生插件**进程内运行**：它们 `import "xbot/plugin"`，实现 `Plugin` 接口，编译进 xbot 二进制。需要紧密集成、零进程开销时使用。

## 第一步：实现插件

```go
package helloworld

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"xbot/plugin"
)

// HelloWorldPlugin 实现 plugin.Plugin。
type HelloWorldPlugin struct {
	callCount atomic.Int64
	startTime time.Time
}

// Manifest 返回插件元数据。
func (p *HelloWorldPlugin) Manifest() plugin.PluginManifest {
	return plugin.PluginManifest{
		ID:               "xbot.hello-world",
		Name:             "Hello World",
		Version:          "1.0.0",
		Description:      "A simple example plugin",
		Runtime:          plugin.RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Permissions:      []string{"tools.register", "hooks.subscribe", "context.enrich", "storage.private"},
	}
}

// Activate 初始化插件并注册全部能力。
func (p *HelloWorldPlugin) Activate(pctx plugin.PluginContext) error {
	p.startTime = time.Now()

	// 1. 注册一个带类型化参数的工具
	if err := pctx.RegisterTool(&plugin.SimplePluginTool{
		Def: plugin.BuildToolDef("hello", "Greet someone by name.",
			plugin.ToolParamDef{Name: "name", Type: "string", Description: "The person to greet"},
		),
		ExecFn: func(ctx context.Context, input string) (*plugin.ToolResult, error) {
			name, err := plugin.ParseToolInputString(input, "name")
			if err != nil {
				name = "World"
			}
			p.callCount.Add(1)
			return plugin.NewToolResult(fmt.Sprintf("Hello, %s! 👋 Welcome to xbot plugins.", name)), nil
		},
	}); err != nil {
		return fmt.Errorf("register hello tool: %w", err)
	}

	// 2. 注册 PostToolUse Hook，把计数器持久化到存储
	pluginCtx := pctx // 捕获供闭包使用
	if err := pctx.OnPostToolUse("", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
		storage := pluginCtx.Storage()
		count, _ := storage.Get("tool_call_count")
		// …… 自增并 storage.Set(...)
		return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
	}); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}

	// 3. 注册上下文注入器，把状态注入系统提示词
	if err := pctx.EnrichContext("hello_status", func(ctx context.Context) (string, error) {
		return fmt.Sprintf("Hello World plugin active (tool calls served: %d)", p.callCount.Load()), nil
	}); err != nil {
		return fmt.Errorf("register enricher: %w", err)
	}
	return nil
}

func (p *HelloWorldPlugin) Deactivate(ctx plugin.PluginContext) error {
	ctx.Logger().Info("Goodbye from Hello World plugin!")
	return nil
}
```

## 第二步：注册到运行时

原生插件注册在 `NativeRuntime` 上——通常在插件包的 `init()` 里：

```go
import "xbot/plugin"

func init() {
	plugin.NativeRuntimeRegistry.RegisterPlugin(&HelloWorldPlugin{})
}
```

插件目录仍需 `plugin.json` 以便发现与校验（`plugin/manifest.go:LoadManifest`）：

```json
{
  "id": "xbot.hello-world",
  "name": "Hello World",
  "version": "1.0.1",
  "runtime": "native",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich", "storage.private"],
  "contributes": {
    "tools": [
      { "name": "hello", "description": "Greet someone by name." },
      { "name": "ping", "description": "Ping-pong connectivity test." }
    ],
    "hooks": [
      { "event": "PostToolUse", "matcher": "" }
    ],
    "contextEnrichers": [
      { "name": "hello_status", "description": "Shows Hello World plugin status" }
    ]
  }
}
```

## 第三步：构建并重启

`plugin.NativeRuntime.Create`（`plugin/runtime.go:38`）在清单 ID 匹配时返回预注册的实例。没有其他步骤——重启 xbot，让 agent 调用 `hello` 工具即可。

## 工作原理

- **`Plugin` 接口**（`plugin/plugin.go:26`）——三个方法：`Manifest()`、`Activate(ctx)`、`Deactivate(ctx)`。`Activate` 必须幂等；它接收 `PluginContext`，所有能力注册都经由它完成。
- **`BuildToolDef(name, desc, params...)`** 构建带 JSON Schema 自动生成的 `ToolDef`（`ToJSONSchema`）。
- **`ParseToolInputString(input, "name")`** 从工具原始 JSON 输入中提取参数。
- **`OnPostToolUse("", handler)`** —— matcher `""` 表示"所有工具"。通配符用 `"Shell*"`。
- **`Storage()`** 访问插件私有键值存储（见 [存储系统](../storage/)）。
- **`EnrichContext`** 每轮把动态内容注入系统提示词——注入器闭包由 agent 循环调用。

## SDK 快捷方式

`plugin/sdk.go` 提供流式助手，大幅削减样板代码：

```go
m := plugin.QuickManifest("xbot.demo", "Demo", "0.1.0", "A demo",
	plugin.WithPermissions(plugin.PermToolsRegister),
	plugin.WithTools(plugin.ToolContribution{Name: "demo_tool", Description: "..."}),
)

tool := plugin.ToolFromFunc("double", "Doubles a number",
	func(ctx context.Context, input string) (string, error) { return input + input, nil })

pctx.RegisterTool(tool)
```

完整指南见 [Go 插件](../go-plugins/)。
