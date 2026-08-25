---
title: "脚本运行时"
weight: 12
---

脚本插件运行外部脚本（bash、Python、Node，任何可执行程序）。它们无需编写 Go 代码即可创建 widget、hook 和命令，是最简单的方式。

## 概述

脚本插件在清单中使用 `runtime: "script"`。`entry` 字段指定要执行的命令。脚本的 stdout 成为 widget 内容或命令响应。

## 清单

```json
{
  "id": "my-script-plugin",
  "name": "My Script Plugin",
  "version": "1.0.0",
  "runtime": "script",
  "entry": "bash main.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute"],
  "contributes": {
    "ui": [
      {
        "id": "status",
        "slot": "statusBarRight",
        "priority": 50,
        "description": "Show status info",
        "refreshInterval": "10s",
        "triggers": ["PostToolUse:Shell"]
      }
    ]
  }
}
```

### 平台特定入口

```json
{
  "entry": "bash main.sh",
  "entry_windows": "powershell main.ps1",
  "entry_darwin": "bash main.sh",
  "entry_linux": "bash main.sh"
}
```

平台特定入口在匹配的操作系统上优先于通用 `entry`。

## Widget 刷新

Widget 由三种触发方式刷新：

1. **周期刷新**：`refreshInterval` 字段（如 `"10s"`、`"1m"`）。默认：30 秒。
2. **Hook 触发**：`triggers` 字段（如 `["PostToolUse:Shell"]`）。hook 匹配时立即触发。
3. **目录变化**：会话工作目录变化时，脚本会针对新目录重新执行。

### 触发事件

| 触发条件 | 描述 |
|---------|------|
| `PreToolUse:<matcher>` | 工具执行前（matcher = 工具名模式） |
| `PostToolUse:<matcher>` | 工具成功执行后 |
| `PostToolUseFailure:<matcher>` | 工具执行失败后 |
| `UserPromptSubmit` | 用户发送消息时 |
| `AgentStop` | Agent 停止时 |
| `SessionStart` | 会话开始时 |
| `SessionEnd` | 会话结束时 |
| `SubAgentStart` | SubAgent 启动时 |
| `SubAgentStop` | SubAgent 停止时 |
| `PreCompact` | 上下文压缩前 |
| `PostCompact` | 上下文压缩后 |
| `CronFired` | 定时任务触发时 |
| `WebhookReceived` | 收到 webhook 时 |

### 同步模式

在 UI 贡献上设置 `"sync": true`，hook 触发时脚本将同步执行。输出立即可用，作为引擎的提示内容：

```json
{
  "ui": [
    {
      "id": "diff-hint",
      "slot": "infoBar",
      "sync": true,
      "triggers": ["PostToolUse:FileReplace"]
    }
  ]
}
```

## 环境变量

脚本通过环境变量接收上下文：

| 变量 | 描述 | 可用时机 |
|------|------|----------|
| `XBOT_WORK_DIR` | 当前工作目录 | 始终 |
| `XBOT_WIDGET_ID` | 正在渲染的 widget ID | Widget 渲染时 |
| `XBOT_PLUGIN_CONFIG` | 插件配置（JSON） | 始终（若存在配置） |
| `XBOT_HOOK_EVENT` | Hook 事件名 | Hook 触发时 |
| `XBOT_TOOL_NAME` | 触发 hook 的工具名 | 工具 hook |
| `XBOT_TOOL_OUTPUT` | 工具输出（截断到 8KB） | PostToolUse hook |
| `XBOT_TOOL_INPUT` | 工具输入 | 工具 hook |
| `XBOT_MODEL` | 当前 LLM 模型名 | 带会话上下文的 hook 事件 |
| `XBOT_MAX_CONTEXT` | 最大上下文 token 数 | 带会话上下文的 hook 事件 |
| `XBOT_TOKEN_USAGE` | token 用量，格式 `prompt/completion` | 带 token 数据的 hook 事件 |
| `XBOT_PROMPT_TOKENS` | Prompt token 数 | 带 token 数据的 hook 事件 |
| `XBOT_COMP_TOKENS` | Completion token 数 | 带 token 数据的 hook 事件 |
| `XBOT_COMMAND_NAME` | 命令名 | 命令执行时 |
| `XBOT_COMMAND_ARGS` | 命令参数 | 命令执行时 |

## 输出格式

脚本 stdout 会被解析出样式提示：

| 格式 | 样式 | 描述 |
|------|------|------|
| `text` | 普通 | 默认样式 |
| `dim\|text` | 暗淡 | 弱化/变暗文本 |
| `ok\|text` | 成功 | 绿色文本 |
| `warn\|text` | 警告 | 黄色文本 |
| `err\|text` | 错误 | 红色文本 |
| `info\|text` | 信息 | 蓝色文本 |
| `accent\|text` | 强调 | 高亮文本 |
| `md\|<markdown>` | 原文 | 多行 markdown 内容 |
| `diff\|<diff>` | 原文 | 多行 unified diff（保留 ANSI） |

`|` 分隔符将样式与内容分开。对于 `md|` 和 `diff|`，前缀之后的完整多行内容会被保留。

## 按 WorkDir 的输出缓存

脚本插件维护按 workDir 划分的输出缓存：`workDir → widgetID → output`。每个 CLI 窗口（不同 workDir）看到各自的内容。缓存：

- 在刷新时填充（周期或触发）
- 目录不存在时驱逐
- 变更检测：只有输出真正变化时才会触发 `NotifyUpdated()`

## 命令

脚本可以注册斜杠命令：

```json
{
  "contributes": {
    "commands": [
      {
        "name": "/deploy",
        "description": "Deploy the current project"
      }
    ]
  }
}
```

用户输入 `/deploy production` 时，脚本以 `XBOT_COMMAND_NAME=deploy`、`XBOT_COMMAND_ARGS=production` 运行。脚本的 stdout 成为命令响应。

## 配置注入

插件配置以 JSON 形式通过 `XBOT_PLUGIN_CONFIG` 注入：

```bash
#!/bin/bash
# 读取插件配置
config=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.greeting // "Hello"')
echo "$config, World!"
```

## 完整示例

```bash
#!/bin/bash
# main.sh — Git 分支 widget

# 获取当前 git 分支
branch=$(git -C "$XBOT_WORK_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null)

if [ -n "$branch" ]; then
    # 检查未提交的改动
    if [ -n "$(git -C "$XBOT_WORK_DIR" status --porcelain 2>/dev/null)" ]; then
        echo "warn|$branch*"
    else
        echo "ok|$branch"
    fi
else
    echo "dim|no-git"
fi
```

## 参见

- [快速开始](./getting-started/) — 创建你的第一个脚本插件
- [Widget 系统](./widgets/) — Widget 系统概览
- [Hook 系统](./hooks/) — Hook 事件
- [插件配置](./configuration/) — 插件配置
- [API 参考：环境变量](./api-reference/environment-variables/) — 完整环境变量参考
