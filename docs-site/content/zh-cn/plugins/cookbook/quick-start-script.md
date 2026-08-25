---
title: "快速上手：Script 插件"
weight: 2
---

用一个 bash 脚本构建 git 状态组件——无需编译、无需 SDK。本篇基于真实示例 `plugin/examples/git-info/`。

## 最终效果

CLI **信息栏**中显示当前 git 分支与未提交变更的组件：

```
git:main ✓          # 干净仓库
git:feat/x Δ3       # 3 个改动文件
git: —               # 不在 git 仓库中
```

## 第一步：编写脚本

`~/.xbot/plugins/git-info/git-info.sh`：

```bash
#!/bin/bash
# 输出格式："style|text"（style：dim、ok、warn、err、info、accent）

set -euo pipefail

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ] || [ "$branch" = "HEAD" ]; then
    echo "dim|git: —"
    exit 0
fi

changes=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ') || changes=0
ahead=$(git rev-list --count @{u}..HEAD 2>/dev/null) || ahead=0
behind=$(git rev-list --count HEAD..@{u} 2>/dev/null) || behind=0

status=""
[ "$changes" -gt 0 ] && status="${status}Δ${changes} "
[ "$ahead" -gt 0 ]   && status="${status}↑${ahead} "
[ "$behind" -gt 0 ]  && status="${status}↓${behind} "

if [ -z "$status" ]; then
    echo "ok|git:${branch} ✓"
elif [ "$changes" -gt 0 ]; then
    echo "warn|git:${branch} ${status}"
else
    echo "info|git:${branch} ${status}"
fi
```

## 第二步：编写清单

`~/.xbot/plugins/git-info/plugin.json`：

```json
{
  "id": "git-info",
  "name": "git-info",
  "version": "1.2.0",
  "description": "在信息栏显示 git 分支与工作区状态。",
  "author": "xbot",
  "runtime": "script",
  "entry": "bash git-info.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [
      {
        "id": "git-branch",
        "slot": "infoBar",
        "priority": 10,
        "description": "git 分支名与脏/净状态",
        "refreshInterval": "30s",
        "triggers": [
          "PostToolUse:Shell*",
          "PostToolUse:Cd*",
          "PostToolUse:FileReplace*",
          "PostToolUse:FileCreate*"
        ]
      }
    ]
  }
}
```

## 第三步：重启 xbot

启动时 xbot 从 `~/.xbot/plugins/<id>/plugin.json` 发现插件。重启后信息栏即出现 `git:main ✓`。

## 工作原理

1. **`runtime: "script"`** —— `plugin.NewScriptRuntime()`（`plugin/script_runtime.go`）把 `entry` 作为外部进程执行（每次运行 10s 超时），周期性刷新并响应 Hook 触发。
2. **`contributes.ui[].slot: "infoBar"`** —— 声明组件槽位。`scriptPlugin.Activate` 把每个 UI 贡献包装成 `widgetAdapter`（`plugin/script_runtime.go:163`），再通过 `ctx.ContributeUI(ui.ID, ui.Slot, adapter, ui.Priority)` 注册。
3. **`refreshInterval: "30s"`** —— 脚本每 30 秒重跑一次（所有组件中的最短间隔生效）。
4. **`triggers: ["PostToolUse:Shell*", ...]`** —— 任意 `Shell*`/`Cd*`/`FileReplace*` 工具执行完毕后，脚本立即重跑（`plugin/script_runtime.go:508 subscribeTrigger`）。
5. **输出格式 `style|text`** —— `parseScriptOutput` 按第一个 `|` 切分；`ok`/`warn`/`dim`/`info`/`err`/`accent` 映射为 `WidgetSpan` 样式。无前缀的输出按普通文本渲染。

脚本以**会话工作目录**作为 CWD 运行（`runScript` 中 `cmd.Dir = workDir`，`plugin/script_runtime.go:683`），因此 `git` 看到的是 agent 当前正在操作的仓库。输出按 **workDir × widget** 缓存（`outputs map[string]map[string]string`），多个 CLI 会话各自看到自己的分支。

## 立即尝试

```bash
mkdir -p ~/.xbot/plugins/git-info
# 把 git-info.sh 和 plugin.json 复制进去
```

脚本内可用的环境变量（由 `runScript` 注入）：

| 变量 | 内容 |
|---|---|
| `XBOT_WORK_DIR` | 当前会话工作目录 |
| `XBOT_WIDGET_ID` | 正在渲染的组件 ID（多组件插件用） |
| `XBOT_TOOL_NAME` / `XBOT_TOOL_INPUT` / `XBOT_TOOL_OUTPUT` | 最近一次 Hook 的工具数据 |
| `XBOT_PLUGIN_CONFIG` | 插件合并配置的 JSON 字符串 |
| `XBOT_HOOK_EVENT` | 最近一次 Hook 事件名 |

下一篇：[Script 插件开发指南](../script-plugins/)，包含完整的环境变量与输出契约。
