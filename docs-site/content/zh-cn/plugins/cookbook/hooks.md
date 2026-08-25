---
title: "Hook 钩子"
weight: 13
---

Hook 让插件观察并否决 agent 的生命周期：工具使用前后、提示词提交、会话边界、上下文压缩、定时任务触发与 webhook。类型定义在 `plugin/plugin.go`（HookEvent/HookDecision/HookResult/HookPayload）；分发桥在 `plugin/adapter_hook.go`。

## 完整事件目录

`plugin/plugin.go:623`：

| 事件 | 触发时机 |
|---|---|
| `PreToolUse` | 工具执行前（可否决） |
| `PostToolUse` | 工具成功后 |
| `PostToolUseFailure` | 工具失败后 |
| `UserPromptSubmit` | 用户提交提示词时 |
| `AgentStop` | agent 循环终止时 |
| `SessionStart` / `SessionEnd` | 会话生命周期 |
| `SubAgentStart` / `SubAgentStop` | 子代理启动 / 完成 |
| `PreCompact` / `PostCompact` | 历史压缩前后 |
| `CronFired` | 定时任务触发 |
| `WebhookReceived` | 入站 webhook 到达 |

## 订阅

```go
// matcher "" = 所有工具；"Shell*" = 前缀通配
ctx.OnPreToolUse("Shell*", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
	if strings.Contains(payload.ToolInput, "rm -rf /") {
		return &plugin.HookResult{Decision: plugin.DecisionDeny,
			Message: "destructive command blocked"}, nil
	}
	return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
})

ctx.OnPostToolUse("Shell*", handler)   // 成功后观察
ctx.OnEvent(plugin.HookAgentStop, "", handler)  // 按名字订阅任意事件
ctx.OnAllToolUse(handler)              // 所有工具的 pre+post
ctx.OnError(handler)                   // 工具失败
```

清单中的声明（驱动激活与发现）：

```json
"contributes": { "hooks": [ { "event": "PostToolUse", "matcher": "Shell*" } ] }
```

## 载荷

`HookPayload`（`plugin/plugin.go:674`）：

```go
type HookPayload struct {
	Event         HookEvent      `json:"event"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     string         `json:"tool_input,omitempty"`
	ToolOutput    string         `json:"tool_output,omitempty"`     // 仅 PostToolUse
	ToolElapsedMs int64          `json:"tool_elapsed_ms,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	Channel       string         `json:"channel,omitempty"`
	ChatID        string         `json:"chat_id,omitempty"`
	UserID        string         `json:"user_id,omitempty"`
	TenantID      int64          `json:"tenant_id,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}
```

⚠️ **`ToolOutput` 截断到 8KB**——不要依赖它获取完整文件内容。`Extra` 携带会话上下文（`model`、`max_context`、`prompt_tokens`、`comp_tokens`）与事件特定数据（如 git-pr-status 的 `detectBranch` 示例读取 shell 输出）。

## 决策

`HookDecision`（`plugin/plugin.go:655`）：`allow`、`deny`、`ask`、`defer`。桥接器中的优先级（`adapter_hook.go decisionWeight`）：**`deny > defer > ask > allow`**——低优先级层的 deny 不能被高优先级层的 allow 覆盖。

```go
type HookResult struct {
	Decision HookDecision   `json:"decision"`
	Message  string         `json:"message,omitempty"` // deny/ask 的说明
	Data     map[string]any `json:"data,omitempty"`
}
```

返回 `(nil, nil)` 表示弃权（观察型处理器等效于 defer/allow）。

## SDK 快捷方式

`plugin/sdk.go`：

```go
handler := plugin.AllowHook()              // 永远允许
handler := plugin.DenyHook("not allowed")  // 永远拒绝并带消息
handler := plugin.LogHook(logger, "event") // 记录 + 允许
```

## Stdio 插件的 Hook

`hook` 方法接收 `{event, toolName, toolInput, sessionId, channel, chatId}`，响应 `{"hook_result":{"decision":"allow"}}`——见 `plugin/examples/grpc-python/main.py handle_hook` 与 `protocol.HookParams`/`protocol.HookResult`。

## Script 插件的 Hook

Script 插件经 `contributes.ui[].triggers`（`"PostToolUse:Shell*"`）订阅——运行时重跑脚本，把载荷注入 `XBOT_HOOK_EVENT`/`XBOT_TOOL_NAME`/`XBOT_TOOL_INPUT`/`XBOT_TOOL_OUTPUT` 环境变量。Script 触发器设计为**全局**（会话无关，`registerGlobalHook`，`plugin/script_runtime.go:608`）。

## toolHint 区域与同步执行

`"sync": true` 的 `ui` 槽位（`toolHint` 区域）在 Hook 调用内**内联**执行脚本。`GetHintContent()` 在 Hook 返回后立即返回剥离前缀的输出——这就是 `file-diff` 把实时 diff 附加到 `ToolProgress.ToolHints` 的机制：

1. `PostToolUse:FileReplace*` 触发。
2. `subscribeTrigger` 的 `triggerFn` 发现 `syncWidgets` 非空 → 同步执行 `file-diff.sh`。
3. 输出剥离 `md|`/`diff|` 前缀 → 存为 `hintContent`。
4. 引擎调用 `GetToolHints()`（**消费并清空**提示）→ 附加到工具结果。

## 避坑

- **匹配器语义**：`""` 匹配所有工具；`"Shell*"` 是前缀通配（`matchToolName`）。精确匹配所需——裸 `PostToolUse` + `""` 会在每个会话的每个工具上触发。
- **会话隔离**：原生插件经 `OnEvent` 注册的 Hook 由桥接器按会话隔离，除非注册为全局（`OnGlobalEvent`）。Script 触发器永远全局。
- **并发**：Hook 处理器运行在 agent goroutine 上——用互斥锁保护共享状态（见 `GitPRPlugin.mu`）。
- **`GetToolHints()` 消费即清空**——第二次读取返回空；过期提示不会附加到下一个工具。
- 每个事件最多 10 个处理器、总超时 60s；超额处理器静默截断并记录警告日志（`agent/hooks/manager.go`）。
