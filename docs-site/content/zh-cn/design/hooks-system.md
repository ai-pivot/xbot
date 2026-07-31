---
title: "Hooks System Design"
weight: 50
---

# xbot Hooks 系统设计

> 覆盖 17 种生命周期事件，支持 command/http/mcp_tool/callback 四种执行器，用户可通过 JSON 配置无代码扩展 agent 行为。

## 架构

```
                    ┌─────────────────────┐
                    │    Hook Manager      │
                    │ (config load·dispatch│
                    │  match·decision agg) │
                    └─────────┬───────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
     ┌────────▼──────┐ ┌─────▼──────┐ ┌──────▼───────┐
     │ Matcher Engine │ │  Executors │ │ Config Layers│
     │                │ │            │ │              │
     │ tool name      │ │ command    │ │ user         │
     │ event glob     │ │ http       │ │ project      │
     │ regex          │ │ callback   │ │ local        │
     │ if-condition   │ │ mcp_tool   │ │              │
     └────────────────┘ └────────────┘ └──────────────┘
```

## 生命周期事件（17 种）

| 事件 | 触发时机 | 可阻塞 |
|------|----------|--------|
| `SessionStart` | 会话启动/恢复 | ❌ |
| `SessionEnd` | 会话结束 | ❌ |
| `UserPromptSubmit` | 用户消息提交后、LLM 处理前 | ✅ |
| `PreToolUse` | 工具执行前 | ✅ |
| `PostToolUse` | 工具执行成功后 | ❌ |
| `PostToolUseFailure` | 工具执行失败后 | ❌ |
| `PostToolBatch` | 一轮工具调用全部完成后 | ✅ |
| `PermissionRequest` | 权限审批时 | ✅ |
| `PermissionDenied` | 权限被拒时 | ❌ |
| `SubAgentStart` | SubAgent 创建时 | ❌ |
| `SubAgentStop` | SubAgent 完成时 | ✅ |
| `AgentStop` | Agent 完成响应 | ✅ |
| `AgentError` | API 调用失败 | ❌ |
| `PreCompact` | 上下文压缩前 | ✅ |
| `PostCompact` | 压缩完成后 | ❌ |
| `CronFired` | 定时任务触发 | ❌ |
| `WebhookReceived` | 外部 webhook 到达 | ❌ |

## Handler 类型

### command — Shell 命令（最常用）

```json
{
  "type": "command",
  "command": ".xbot/hooks/lint.sh",
  "timeout": 30,
  "async": false
}
```

- 事件载荷通过 **stdin（JSON）** 传入
- 退出码 0 = 成功，退出码 2 = 阻塞（deny）
- `async: true` 不阻塞 agent，适合日志/通知类 hook

### http — HTTP POST

```json
{
  "type": "http",
  "url": "http://localhost:3000/api/build-trigger",
  "timeout": 10
}
```

- 事件载荷作为 POST body（`Content-Type: application/json`）
- 2xx = 成功，非 2xx = 非阻塞错误

### callback — Go 内置

```go
type CallbackHook struct {
    Name string
    Fn   func(ctx context.Context, event Event) (*Result, error)
}
```

Logging、Timing、Approval 等内置 hook 使用此类型。

### mcp_tool — MCP 工具调用

```json
{
  "type": "mcp_tool",
  "server": "security",
  "tool": "scan_file",
  "input": { "path": "${tool_input.path}" }
}
```

`${tool_input.path}` 从事件载荷自动插值。

## 决策控制

| 决策 | 含义 |
|------|------|
| `allow` | 允许继续 |
| `deny` | 阻止操作（最高优先级） |
| `ask` | 向用户询问 |
| `defer` | 交由下个 handler 决定 |

优先级：**deny > defer > ask > allow**。低优先级 deny 不可被高优先级 allow 覆盖。

## 配置

三层合并：`~/.xbot/hooks.json` → `<project>/.xbot/hooks.json` → `<project>/.xbot/hooks.local.json`，后者覆盖前者。

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [{
          "type": "command",
          "command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make -C \"$XBOT_PROJECT_DIR\" lint || exit 2",
          "if": "Shell(*git commit*)",
          "timeout": 120
        }]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [{
          "type": "command",
          "command": ".xbot/hooks/context-loader.sh"
        }]
      }
    ]
  }
}
```

### 关键规则

- **`enable_command_hooks` 默认禁用** — 必须显式设为 `true`
- **最多 10 handler 每事件**，总超时 60s
- 修改配置后需**重启 CLI** 生效
- 项目级 `.xbot/hooks.json` 可提交 git，本地 `.xbot/hooks.local.json` 不入版本控制

### 常用环境变量

| 变量 | 值 |
|------|------|
| `$XBOT_HOME` | `~/.xbot` |
| `$XBOT_PROJECT_DIR` | 当前项目根目录 |
| `$XBOT_SESSION_ID` | 当前 session ID |

## 安全

| 风险 | 缓解 |
|------|------|
| 恶意 shell 命令 | command 类型默认禁用 |
| 死循环 | 每事件最多 10 handler，总超时 60s |
| 信息泄露 | 事件载荷自动脱敏 api_key/secret |
| SSRF | HTTP handler 禁止内网地址 |
| 覆盖安全 | 低层级 deny 不可被高层级 allow 覆盖 |

## 集成点

```
agent/agent.go
  Run() 启动 → Manager.Emit(SessionStart)
  Run() 结束 → Manager.Emit(SessionEnd)

agent/engine_run.go
  Run() 入口 → Manager.Emit(UserPromptSubmit)
  Run() 结束 → Manager.Emit(AgentStop)

agent/engine.go
  executeTool() 前 → Manager.Emit(PreToolUse)
  executeTool() 后 → Manager.Emit(PostToolUse)
  批量工具完成      → Manager.Emit(PostToolBatch)

agent/context_manager.go
  maybeCompress() 前 → Manager.Emit(PreCompact)
  maybeCompress() 后 → Manager.Emit(PostCompact)

agent/subagent.go
  创建 → Manager.Emit(SubAgentStart)
  完成 → Manager.Emit(SubAgentStop)
```

## 对标 Claude Code

| 特性 | Claude Code | xbot |
|------|-------------|------|
| 事件数 | 30+ | 17（覆盖核心场景） |
| Handler 类型 | command/http/mcp_tool | command/http/callback/mcp_tool |
| 匹配器 | 正则 + if 条件 | 一致 |
| 决策控制 | allow/deny/ask/defer | 一致 |
| 配置层级 | user/project/local | user/project/local |
| 入站 hook | 无 | WebhookReceived + CronFired（独有） |
