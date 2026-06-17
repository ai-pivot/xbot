---
title: "Hooks（生命周期钩子）"
weight: 60
---

# Hooks（生命周期钩子）

Hooks 让你在 xbot 执行动作的「前一刻」或「后一刻」自动运行脚本。比如：每次 git commit 之前自动跑 lint、AI 回答完后自动发个通知、文件编辑后自动格式化。

## 最简单的用法：让 AI 帮你配

不需要手写 JSON。直接告诉 xbot 你想要什么：

> 「帮我设一个 hook，每次 git commit 之前自动跑 make lint」

> 「配一个 hook，AI 报错的时候给我发桌面通知」

> 「每次编辑 Go 文件后自动跑 gofmt」

AI 会帮你创建 `hooks.json` 配置文件，然后自动重载生效。

### 重载 Hooks

> ⚠️ 修改 hooks 配置后需要重载才能生效。AI 配完后会自动重载，你也可以说「重载 hooks 配置」。

## Hooks 配置文件

配置文件是 JSON 格式，放在这些位置（后面的覆盖前面的）：

| 文件 | 作用 |
|------|------|
| `~/.xbot/hooks.json` | 用户级，所有项目生效 |
| `<项目>/.xbot/hooks.json` | 项目级，可提交到 git 共享给团队 |
| `<项目>/.xbot/hooks.local.json` | 本地覆盖，不提交到 git |

### 一个完整的例子

**场景**：每次 `git commit` 之前自动跑 lint，不通过就阻止提交。

`~/.xbot/hooks.json`:
```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [{
          "type": "command",
          "command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make lint || exit 2",
          "if": "Shell(*git commit*)",
          "timeout": 120
        }]
      }
    ]
  }
}
```

**关键点**：
- `enable_command_hooks: true` — **必须写**，不写的话命令类型的 hook 不会执行
- `exit 2` = 阻止操作，`exit 0` = 放行
- `if` 是过滤条件，只匹配包含 `git commit` 的 Shell 调用

## 什么时候可以挂 Hook

xbot 有 17 个「挂载点」，分两类：

### 能阻止操作的（Blocking）

这些 hook 可以说「不行，不许这么做」：

| 事件 | 时机 | 用途 |
|------|------|------|
| `UserPromptSubmit` | 用户消息发送前 | 审查用户输入 |
| `PreToolUse` | 工具执行前 | **最常用**——提交前 lint、写文件前检查 |
| `PostToolBatch` | 一批工具执行完后 | 批量检查 |
| `SubAgentStop` | 子 Agent 完成后 | 审查子 Agent 结果 |
| `AgentStop` | AI 回复完成后 | 最终审查 |
| `PreCompact` | 上下文压缩前 | 控制压缩行为 |

### 只是通知的（Non-blocking）

这些 hook 只能「听说」发生了什么，不能阻止：

| 事件 | 时机 | 用途 |
|------|------|------|
| `SessionStart` | 会话开始 | 初始化 |
| `SessionEnd` | 会话结束 | 清理 |
| `PostToolUse` | 工具执行成功后 | 日志、通知 |
| `PostToolUseFailure` | 工具执行失败后 | 错误通知 |
| `AgentError` | AI 调用失败 | 错误告警 |
| `PostCompact` | 压缩完成后 | 日志 |
| `CronFired` | 定时任务触发 | |
| `WebhookReceived` | 收到外部 Webhook | |

## Hook 的三种类型

### command（最常用）

运行一个 shell 命令。事件数据通过 stdin 传入（JSON 格式）。

- 退出码 `0` = 放行
- 退出码 `2` = **阻止**操作
- 其他退出码 = 放行（错误会被记录但不阻止）

```json
{
  "type": "command",
  "command": "my-script.sh",
  "timeout": 30,
  "async": false
}
```

`async: true` 表示后台运行，不阻塞 AI——适合发通知、记日志。

### http

向一个 URL 发 POST 请求。事件数据作为请求体。

```json
{
  "type": "http",
  "url": "http://localhost:3000/notify",
  "timeout": 10
}
```

### mcp_tool

调用一个 MCP 工具。支持 `${...}` 变量插值。

```json
{
  "type": "mcp_tool",
  "server": "security",
  "tool": "scan_file",
  "input": { "path": "${tool_input.path}" }
}
```

## matcher 和 if 的区别

- **`matcher`**：匹配工具名。`"Shell"` 只匹配 Shell 工具，`""` 匹配所有
- **`if`**：更精细的过滤，匹配整个工具输入的文本

例如 `if: "Shell(*git commit*)"` 会检查这次 Shell 调用的参数里是否包含 `git commit`。

> ⚠️ `if` 匹配的是**整个输入文本**，不是 key=value。`Shell(*git commit*)` 能工作，但 `Shell(command=*git*)` 不行。

## 脚本能拿到什么

command 类型的 hook，环境变量自动可用：

| 变量 | 内容 |
|------|------|
| `$XBOT_PROJECT_DIR` | 当前项目根目录 |
| `$XBOT_SESSION_ID` | 当前会话 ID |
| `$XBOT_HOME` | `~/.xbot` 目录 |

事件数据还会通过 stdin 以 JSON 格式传入。

## 参考手册

### 完整配置格式

```jsonc
{
  // 必须设为 true，command 类型 hook 才会执行
  "enable_command_hooks": true,

  "hooks": {
    "事件名": [
      {
        "matcher": "工具名或通配符",  // "" 匹配所有
        "hooks": [
          {
            "type": "command",        // command | http | mcp_tool
            "command": "script.sh",   // command 类型：要运行的命令
            "url": "http://...",      // http 类型：目标 URL
            "if": "Shell(*pattern*)", // 可选：精细过滤
            "timeout": 30,            // 超时秒数（最长 60s）
            "async": false            // true = 后台运行，不阻塞
          }
        ]
      }
    ]
  }
}
```

### 决定结果的优先级

当多个 hook 同时触发时，按这个优先级决定最终结果：

**阻止 > 弃权 > 询问 > 放行** (`deny > defer > ask > allow`)

也就是说，只要有一个 hook 说「阻止」，即使其他 hook 说「放行」，操作也会被阻止。

### 限制

- 每个事件最多 10 个 handler
- 单个 handler 超时最长 60 秒
- command hook 必须显式设置 `enable_command_hooks: true`

## 更多例子

### AI 出错时发桌面通知（不阻塞）

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "AgentError": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "notify-send 'xbot 出错了' 'LLM 调用失败，请检查'",
        "async": true
      }]
    }]
  }
}
```

### 编辑 Go 文件后自动格式化

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PostToolUse": [{
      "matcher": "FileReplace",
      "hooks": [{
        "type": "command",
        "command": "file=$(echo $XBOT_TOOL_INPUT | jq -r '.path'); [[ \"$file\" == *.go ]] && gofmt -w \"$file\"",
        "async": true
      }]
    }]
  }
}
```

## Hook 创意

你可以创建的实用 hooks：

- **Go 自动格式化** — 每次文件编辑后运行 `gofmt`
- **Git diff 预览** — 每次文件编辑后显示 diff
- **Slack 通知** — Agent 出错时 POST 到 webhook
- **成本追踪** — 每次 LLM 调用后记录 token 用量到文件
- **自动提交** — 文件操作成功后 `git add`
- **安全扫描** — 允许 Shell 命令前运行 `gosec`

让 Agent："设置一个 hook 来[你的需求]。"

## 参见
- [插件](/zh-cn/features/plugins/) — 插件系统概览
- [权限控制](/zh-cn/guides/permission-control/) — 沙箱和权限
- [配置参考](/zh-cn/configuration/) — enable_command_hooks 设置
