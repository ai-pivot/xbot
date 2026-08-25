---
title: "Script 插件"
weight: 6
---

Script 插件按周期性刷新循环 + Hook 触发执行外部命令（bash、Python、Node——任何可执行程序）。运行时实现为 `plugin/script_runtime.go`；两个完整示例：`plugin/examples/git-info/`（状态组件）与 `plugin/examples/file-diff/`（工具提示 diff）。

## 契约

脚本：

1. 以**会话工作目录**为 CWD 运行；
2. 通过**环境变量**接收上下文；
3. 输出写入 **stdout**，以换行结尾；
4. 输出按 `style|text` 行格式解析。

## 输出格式

`parseScriptOutput`（`plugin/script_runtime.go`）把 `style|text` 行解析为 `WidgetSpan`：

```bash
echo "ok|git:main ✓"      # ok     → 强调/绿色
echo "warn|git:feat/x Δ3" # warn   → 黄色
echo "err|build failed"   # err    → 红色
echo "dim|git: —"         # dim    → 弱化
echo "info|syncing..."    # info   → 蓝色
echo "accent|42 items"    # accent → 高亮
echo "plain text"         # 无前缀 → 普通样式
```

两个多行前缀是特例（`plugin/script_runtime.go:744`）：`md|`（markdown，进度面板由 glamour 渲染）与 `diff|`（带 ANSI 着色的 unified diff）。其余输出都是单行。

`file-diff.sh` 中的示例（toolHint 组件生成 diff）：

```bash
echo "md|"
echo "\`\`\`diff"
echo "--- a/path/file.go"
echo "+++ b/path/file.go"
echo "$raw_diff" | head -40
echo "\`\`\`"
```

## 环境变量

由 `runScript` 注入（`plugin/script_runtime.go:692`）：

| 变量 | 来源 |
|---|---|
| `XBOT_WORK_DIR` | 会话工作目录 |
| `XBOT_WIDGET_ID` | 正在渲染的组件 ID（多组件插件据此分支） |
| `XBOT_PLUGIN_CONFIG` | 插件合并配置，JSON 字符串 |
| `XBOT_HOOK_EVENT` | 触发本次运行的最新 Hook 事件 |
| `XBOT_TOOL_NAME` | 最新 Hook 的工具名 |
| `XBOT_TOOL_INPUT` | 最新 Hook 的工具输入（JSON 字符串） |
| `XBOT_TOOL_OUTPUT` | 最新 Hook 的工具输出 |
| `XBOT_MODEL` / `XBOT_MAX_CONTEXT` | 来自 `HookPayload.Extra` 的会话上下文 |
| `XBOT_TOKEN_USAGE` / `XBOT_PROMPT_TOKENS` / `XBOT_COMP_TOKENS` | Token 用量（`"prompt/completion"` 格式） |
| `XBOT_COMMAND_NAME` / `XBOT_COMMAND_ARGS` | 作为贡献命令被调用时 |

⚠️ `XBOT_WIDGET_ID` 很重要：插件声明多个组件时，脚本**每个组件各跑一次**（ID 已设置），输出按 `workDir → widgetID` 缓存。不按它分支，所有组件会显示相同内容。

## 组件、触发器与同步提示

清单中的 `contributes.ui[]` 驱动一切（`UISlotContribution`）：

```json
{
  "id": "file-diff",
  "runtime": "script",
  "entry": "bash file-diff.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [
      {
        "id": "diff-summary",
        "slot": "toolHint",
        "priority": 5,
        "sync": true,
        "description": "文件修改后在进度面板中显示 unified diff",
        "triggers": ["PostToolUse:FileReplace*", "PostToolUse:FileCreate*", "PostToolUse:FileEdit*", "PostToolUse:Write*"]
      }
    ]
  }
}
```

- **`slot`** —— 组件渲染位置。CLI 槽位有 `infoBar`、`statusBarRight`、`toolHint`、`footer`、`titleBar`（见 [Widget 组件](../widgets/)）。
- **`triggers`** —— `"事件名:匹配器"` 字符串。`subscribeTrigger`（`plugin/script_runtime.go:508`）解析并订阅到 Hook；Hook 触发时脚本立即重跑。支持的事件：`PreToolUse`、`PostToolUse`、`PostToolUseFailure`、`UserPromptSubmit`、`AgentStop`、`SessionStart`、`SessionEnd`、`SubAgentStart`、`SubAgentStop`、`PreCompact`、`PostCompact`、`CronFired`、`WebhookReceived`。
- **`sync: true`** —— `toolHint` 槽位必须同步：Hook 触发时脚本**内联**执行（不走异步触发通道），剥掉 `md|`/`diff|` 前缀后存储为提示内容。引擎在 Hook 返回后立即通过 `GetHintContent()` 读取，附加到 `ToolProgress.ToolHints`——这就是 `file-diff` 能在编辑工具完成后立刻在进度面板渲染实时 diff 的原因。
- **`refreshInterval`** —— Go duration 字符串（`"30s"`、`"1m"`）；所有组件中的最短间隔生效（`plugin/script_runtime.go:175`）。

⚠️ Script 插件的触发器注册为**全局 Hook**（`registerGlobalHook`，`plugin/script_runtime.go:608`）——它们与会话无关，必须对所有会话生效。按会话输出由 `workDir → widgetID → output` 缓存处理，而非会话过滤。

## 内部循环

`scriptPlugin.refreshLoop`（`plugin/script_runtime.go:361`）：

1. 启动时立即运行，之后每个 `interval` 周期运行；
2. `triggerCh`（缓冲 8——满则跳过本次触发，下个 tick 补上）触发时也运行；
3. `runAndUpdate` 收集所有已知 workDir（缓存 + `OnWorkDirChanged` 待处理 + 当前），清理已删除目录，对每个组件 × 每个 workDir 各跑一次脚本；
4. 变更检测对比上次快照，仅当输出实际变化时调用 `widgetReg.NotifyUpdated()`。

## 平台特定入口

`resolvedEntry`（`plugin/script_runtime.go:620`）按操作系统选择命令：

```json
{
  "entry": "bash run.sh",
  "entry_windows": "powershell -File run.ps1",
  "entry_linux": "bash run.sh",
  "entry_darwin": "bash run.sh"
}
```

## 运行时限制

- 每次运行 **10s 超时**（`context.WithTimeout(parent, 10*time.Second)`）。
- 入口命令用 `strings.Fields` 切分（无 shell 解释）；相对脚本路径相对插件目录解析。
- `Deactivate` 取消后台上下文并最多等待 5s 让循环退出（避免 Windows 临时目录清理因文件句柄占用失败）。
- 高频 Hook 触发会**覆盖**存储的 `lastHook`——脚本只看到最新事件。
