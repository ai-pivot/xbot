---
title: "Hook 事件"
weight: 5
---

生命周期 hook 事件与 `HookPayload` 字段参考（`plugin/plugin.go`）。

## HookEvent 常量

```go
type HookEvent string
```

| 常量 | 字符串值 | 触发时机 |
|------|---------|---------|
| `HookPreToolUse` | `"PreToolUse"` | 工具执行之前。 |
| `HookPostToolUse` | `"PostToolUse"` | 工具执行成功之后。 |
| `HookPostToolUseError` | `"PostToolUseFailure"` | 工具执行失败时。 |
| `HookUserPromptSubmit` | `"UserPromptSubmit"` | 用户提交提示词时。 |
| `HookAgentStop` | `"AgentStop"` | agent 循环终止时。 |
| `HookSessionStart` | `"SessionStart"` | 新会话开始时。 |
| `HookSessionEnd` | `"SessionEnd"` | 会话结束时。 |
| `HookSubAgentStart` | `"SubAgentStart"` | 子代理启动之前。 |
| `HookSubAgentStop` | `"SubAgentStop"` | 子代理完成之后。 |
| `HookPreCompact` | `"PreCompact"` | 消息历史压缩之前。 |
| `HookPostCompact` | `"PostCompact"` | 消息历史压缩之后。 |
| `HookCronFired` | `"CronFired"` | 定时任务触发时。 |
| `HookWebhookReceived` | `"WebhookReceived"` | 收到入站 webhook 时。 |

`IsValidHookEvent(name)` 用于验证 manifest `contributes.hooks` 条目与 `onHook:` 激活事件的事件名。

## HookPayload

```go
type HookPayload struct {
    Event         HookEvent      `json:"event"`
    ToolName      string         `json:"tool_name,omitempty"`
    ToolInput     string         `json:"tool_input,omitempty"`
    ToolOutput    string         `json:"tool_output,omitempty"`     // 工具执行结果（仅 PostToolUse）
    ToolElapsedMs int64          `json:"tool_elapsed_ms,omitempty"` // 工具执行耗时（毫秒）
    SessionID     string         `json:"session_id,omitempty"`
    Channel       string         `json:"channel,omitempty"`
    ChatID        string         `json:"chat_id,omitempty"`
    UserID        string         `json:"user_id,omitempty"`
    TenantID      int64          `json:"tenant_id,omitempty"`
    Extra         map[string]any `json:"extra,omitempty"`
}
```

### 重要说明

- `ToolOutput` 在 `HookPayload` 中**截断为 8KB**——不要依赖它获取完整文件内容。需要完整输出的插件应使用专用工具结果通道。
- `Extra` 携带引擎注入的会话上下文（模型名、最大上下文、token 用量）——见[环境变量](environment-variables/)。
- 字段可用性取决于事件：`ToolName`/`ToolInput`/`ToolElapsedMs` 仅工具事件携带；`ToolOutput` 仅 `PostToolUse` 携带。

## HookHandler、HookResult、HookDecision

```go
type HookHandler func(ctx context.Context, payload *HookPayload) (*HookResult, error)

type HookResult struct {
    Decision HookDecision   `json:"decision"`
    Message  string         `json:"message,omitempty"` // deny/ask 的解释
    Data     map[string]any `json:"data,omitempty"`
}
```

| 决策 | 字符串值 | 含义 |
|------|---------|------|
| `DecisionAllow` | `"allow"` | 允许操作继续。 |
| `DecisionDeny` | `"deny"` | 阻止操作，可附带原因。 |
| `DecisionAsk` | `"ask"` | 继续前提示用户确认。 |
| `DecisionDefer` | `"defer"` | 将决策推迟给链中的下一个 handler。 |

**决策优先级：`deny > defer > ask > allow`。** 低优先级层的 deny 不能被高优先级层的 allow 覆盖。

## SDK Hook 助手

来自 `plugin/sdk.go`：

```go
func DenyHook(msg string) HookHandler      // 始终以给定消息拒绝
func AllowHook() HookHandler               // 始终允许
func LogHook(logger Logger, msg string) HookHandler // 记录事件并允许
```

## Handler 限制

- 每个事件最多 10 个 handler。
- 单次事件分发总超时 60s。
- 超额 handler 静默截断并记录警告日志。
