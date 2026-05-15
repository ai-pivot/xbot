---
title: "插件系统"
weight: 40
---

# 插件系统

插件让你给 xbot 加「小程序」——在底栏显示 git 状态、自动弹出 diff 预览、每次提交前跑 lint……不用改代码，写一个 JSON + 一个脚本就行。

## 最简单的用法：让 AI 帮你装

你不需要自己写配置文件。直接告诉 xbot 你想要什么：

> 「帮我在底栏加一个显示当前 git 分支的插件」

> 「装一个插件，每次编辑文件后自动显示 diff」

> 「加个插件，在 Agent 报错时弹一个桌面通知」

AI 会帮你创建 `plugin.json` 和脚本文件，然后自动重载生效。你只需要描述你想要的效果。

### 重载插件

AI 创建完插件后会自动重载。你也可以手动操作：

| 方式 | 命令 |
|------|------|
| 在对话里说 | 「重载所有插件」 |
| TUI 斜杠命令 | `/plugin reload-all` |
| 重载单个插件 | `/plugin reload <插件ID>` |
| 查看插件状态 | `/plugin` |

## 插件长什么样

一个插件就是一个文件夹，至少包含两个文件：

```
~/.xbot/plugins/my-plugin/
├── plugin.json     ← 插件描述（名字、做什么、什么时候触发）
└── my-script.sh    ← 实际干活的脚本
```

**举个例子**——底栏显示 git 分支：

`plugin.json`:
```json
{
  "id": "my-git",
  "name": "My Git Status",
  "version": "1.0.0",
  "description": "显示当前 git 分支和改动状态",
  "runtime": "script",
  "entry": "bash git.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [{
      "id": "git-branch",
      "slot": "infoBar",
      "priority": 10,
      "triggers": ["PostToolUse:Shell*", "PostToolUse:FileReplace*"]
    }]
  }
}
```

`git.sh`:
```bash
#!/bin/bash
set -euo pipefail
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ]; then
  echo "dim|git: —"
  exit 0
fi
changes=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ') || changes=0
if [ "$changes" -gt 0 ]; then
  echo "warn|git:${branch} Δ${changes}"
else
  echo "ok|git:${branch} ✓"
fi
```

脚本输出的格式是 `样式|文字`，样式可以是 `dim`、`ok`、`warn`、`err`、`info`、`accent`。

## 插件放哪里

| 位置 | 作用 |
|------|------|
| `~/.xbot/plugins/<id>/` | 用户级，所有项目都能用 |
| `<项目>/.xbot/plugins/<id>/` | 项目级，只在这个项目生效（可以提交到 git 共享给团队） |

## 插件能做什么

### 显示信息（Widget）

在 TUI 界面上显示动态内容：

| 位置 | `slot` 值 | 说明 |
|------|-----------|------|
| 底部信息栏 | `infoBar` | 适合状态信息：git 分支、环境名、倒计时…… |
| 工具执行区 | `toolHint` | 适合一次性提示：diff 摘要、测试结果…… |

### 触发时机

通过 `triggers` 控制插件什么时候运行：

| 触发器 | 含义 |
|--------|------|
| `PostToolUse:Shell*` | 运行 Shell 命令后 |
| `PostToolUse:FileReplace*` | 编辑文件后 |
| `PostToolUse:FileCreate*` | 创建文件后 |
| `AgentStop:` | AI 回复完成后 |
| `SessionStart:` | 会话开始时 |
| `PreToolUse:Shell*` | 运行 Shell 命令前 |

触发器支持通配符：`Shell*` 匹配所有 Shell 工具调用。

### 脚本能拿到什么信息

脚本运行时，以下环境变量自动可用：

| 变量 | 内容 |
|------|------|
| `XBOT_TOOL_NAME` | 触发的工具名，如 `Shell`、`FileReplace` |
| `XBOT_TOOL_OUTPUT` | 工具执行结果（最长 8KB） |
| `XBOT_TOOL_INPUT` | 工具输入参数（JSON 格式） |
| `XBOT_WORK_DIR` | 当前工作目录 |

## 参考手册

### plugin.json 完整字段

```json
{
  "id": "my-plugin",           // 必填，全局唯一 ID
  "name": "My Plugin",         // 显示名
  "version": "1.0.0",          // 语义化版本
  "description": "做什么的",    // 一句话描述
  "author": "your-name",
  "runtime": "script",         // "script" | "native" | "grpc"
  "entry": "bash main.sh",     // 入口命令（script 类型）
  "permissions": [...],        // 需要的权限
  "contributes": {             // 插件贡献的功能
    "ui": [...],               // UI 组件
    "tools": [...],            // 自定义工具
    "hooks": [...]             // 生命周期钩子
  }
}
```

### 权限列表

| 权限 | 用途 |
|------|------|
| `ui.contribute` | 显示 UI 组件（widget） |
| `hooks.subscribe` | 订阅生命周期事件 |
| `tools.register` | 注册自定义工具 |
| `tools.call` | 调用其他工具 |
| `storage.private` | 插件私有存储 |
| `context.enrich` | 注入系统提示 |

### 高级：定时刷新

给 widget 加 `refreshInterval` 可以让它定时自动刷新，不需要等触发器：

```json
"ui": [{
  "id": "clock",
  "slot": "infoBar",
  "priority": 0,
  "refreshInterval": "30s",
  "triggers": ["SessionStart:"]
}]
```

### 高级：同步模式

`toolHint` 类型的 widget 可以设置 `"sync": true`，让插件在工具执行完的同一瞬间同步运行，确保结果立即显示：

```json
"ui": [{
  "id": "diff",
  "slot": "toolHint",
  "sync": true,
  "triggers": ["PostToolUse:FileReplace*"]
}]
```

### CLI 命令速查

| 命令 | 说明 |
|------|------|
| `/plugin` | 插件状态概览 |
| `/plugin list` | 列出所有插件 |
| `/plugin reload <id>` | 重载单个插件 |
| `/plugin reload-all` | 重载所有插件 |
| `/plugin health` | 健康检查 |
| `/plugin install <目录>` | 从目录安装插件 |
| `/plugin uninstall <id>` | 卸载插件 |
| `/plugin widgets` | 查看 widget 状态 |
