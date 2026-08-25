---
title: "RPC 方法"
weight: 11
---

插件可用的 RPC 方法参考，分两层：类型化前端 `BackendRPC` 表（`web/src/plugin-api/rpc.ts`）与宿主侧服务端 RPC handler（`serverapp/rpc_table.go`）。

## 前端 `ctx.rpc` — `BackendRPC` 表

```ts
export interface RPCAPI {
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
```

| 方法 | 参数 | 返回值 |
|------|------|--------|
| `session.get` | `{ chatID: string }` | `SessionDetail`（chatID, title, model, busy, maxContext, maxOutput, tokenUsage, createdAt） |
| `session.list` | `{}` | `SessionSummary[]`（chatID, title, model, busy, maxContext, tokenUsage） |
| `agent.send` | `{ chatID: string; content: string }` | `{ turnID: number; queued: boolean }` |
| `agent.cancel` | `{ chatID: string }` | `{}` |
| `plugin.list` | `{}` | `PluginInfo[]`（id, name, version, enabled） |
| `plugin.get_config` | `{ id: string }` | `{ configuration: PluginConfigSchema; values: Record<string, unknown> }` |
| `plugin.set_config` | `{ id: string; key: string; value: unknown }` | `{ status: string; key: string }` |
| `git.status` | `{ channel: string; chatID: string }` | branch, repo_name, changes[], ahead, behind, commit_hash, commit_msg, is_repo |
| `git.log` | `{ channel: string; chatID: string; limit?: number }` | `{ commits: Array<{ hash; author; when; subject }> }` |
| `git.diff` | `{ channel: string; chatID: string; path: string }` | `{ path: string; content: string }` |
| `git.branches` | `{ channel: string; chatID: string }` | `{ current: string; branches: string[] }` |

`git.*` 方法由 `xbot.git-fancy` 插件贡献——这是后端插件发布类型化数据源的标准模式。

## 插件到后端 RPC（`ctx.rpc.call('<pluginId>.<method>')`）

前端插件视图通过 `web_plugin_rpc` 将任意 RPC 路由到所属后端插件进程。Go 后端需声明 `rpc` 权限（`PermRPC`）。后端 `Handler.WebPluginRPC` 接收 `WebPluginRPCParams{ Method, Params }`，返回 `WebPluginRPCResult{ Result, Error }`（不透明 JSON 字符串）。

## 宿主 RPC Handler（serverapp）

| RPC | 参数 | 用途 |
|-----|------|------|
| `plugin_list` | — | 列出插件条目（manifest、state），附 `active`/`total` 计数。 |
| `plugin_reload` | `{ id }` | 热重载单个插件；对带 Web 声明的插件广播 `web_plugin_init`。 |
| `plugin_reload_all` | — | 热重载全部插件；对每个带 Web 声明的插件广播 `web_plugin_init`。 |
| `plugin_widgets` | `{ chat_id, structured }` | 渲染会话的全部 widget 区域（`zones`、`infos`、`count`）。`structured=true` 返回 Web span 结构而非 ANSI。 |
| `web_plugin_list` | — | 仅列出带 Web 声明的插件。 |
| `web_plugin_rpc` | `{ plugin_id, method, params }` | 将前端插件视图 RPC 路由到后端插件进程。 |
| `web_ui_action` | `{ widget_id, action, data, chat_id }` | 用户与 Web UI 组件的交互。路由优先级：所属 channel 插件 → 原生 handler → agent 循环注入。 |
| `genui_action` | — | GenUI 渲染组件的动作（路由逻辑与 `web_ui_action` 类似）。 |

## Stdio 协议方法（插件进程侧）

NDJSON 协议将以下方法分发到插件的 `Handler`（见 `plugin/protocol/protocol.go`）：

| 方法 | Handler 字段 | 方向 |
|------|-------------|------|
| `activate` | `Handler.Activate` | 宿主 → 插件 |
| `deactivate` | `Handler.Deactivate` | 宿主 → 插件（处理完毕后进程退出） |
| `execute_tool` | `Handler.ExecuteTool` | 宿主 → 插件 |
| `hook` | `Handler.Hook` | 宿主 → 插件 |
| `enrich` | `Handler.Enrich` | 宿主 → 插件 |
| `web_ui_action` | `Handler.WebUIAction` | 宿主 → 插件 |
| `web_plugin_rpc` | `Handler.WebPluginRPC` | 宿主 → 插件 |

未知方法收到 `{"error": "unknown method: <name>"}`。
