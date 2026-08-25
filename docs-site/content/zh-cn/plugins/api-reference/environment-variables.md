---
title: "环境变量"
weight: 6
---

注入 **script 运行时**插件进程的 `XBOT_*` 环境变量参考（`plugin/script_runtime.go`）。stdio 插件不接收这些变量——它们使用 NDJSON 协议。

## 始终注入

| 变量 | 内容 |
|------|------|
| `XBOT_WORK_DIR` | 会话当前工作目录。脚本的 `cmd.Dir` 也会设置为该值（若目录存在）。 |
| `XBOT_PLUGIN_CONFIG` | 插件合并配置序列化后的 JSON 字符串（manifest 默认值叠加用户值）。 |

## Widget 运行

| 变量 | 内容 |
|------|------|
| `XBOT_WIDGET_ID` | **仅当**脚本为特定 widget 运行时设置。多 widget 插件按此变量分支以产生不同输出（单 widget 插件或非 widget 运行时该变量缺失——不设置而非设置为空串）。 |

## Hook 触发运行

脚本作为 hook 触发器运行时可用（值来自 hook payload）：

| 变量 | 内容 |
|------|------|
| `XBOT_HOOK_EVENT` | Hook 事件名（如 `"PostToolUse"`）。 |
| `XBOT_TOOL_NAME` | 工具名（仅非空时设置）。 |
| `XBOT_TOOL_OUTPUT` | 工具输出（仅非空时设置）。 |
| `XBOT_TOOL_INPUT` | 工具输入（仅非空时设置）。 |

## 会话上下文（所有 hook 事件）

从 `HookPayload.Extra` 填充，引擎在每次 LLM 调用与压缩后注入（`hooks.SessionContext` → `plugin_bridge.go` → `HookPayload.Extra` → 环境变量）：

| 变量 | 内容 |
|------|------|
| `XBOT_MODEL` | 当前会话的模型名。 |
| `XBOT_MAX_CONTEXT` | 会话的最大上下文 token 限制。 |
| `XBOT_TOKEN_USAGE` | prompt/completion 合并 token 数，格式 `"<prompt>/<completion>"`（如 `"12345/678"`）。 |
| `XBOT_PROMPT_TOKENS` | Prompt token 数。 |
| `XBOT_COMP_TOKENS` | Completion token 数。 |

## 命令运行

| 变量 | 内容 |
|------|------|
| `XBOT_COMMAND_NAME` | 斜杠命令名（命令处理脚本）。 |
| `XBOT_COMMAND_ARGS` | 命令名之后的全部内容。 |

## 注意事项

- 变量追加到继承的环境（`os.Environ()`），脚本同时能看到宿主进程环境。
- 可选变量在源值为空时**整体省略**——例如无 widget ID 适用时 `XBOT_WIDGET_ID` 不设置（而非设置为空字符串）。
- widget 可通过读取这些变量显示模型名、上下文用量条与 token 成本。
