---
title: "RPC Methods"
weight: 11
---

Reference for RPC methods available to plugins, in two layers: the typed frontend `BackendRPC` table (`web/src/plugin-api/rpc.ts`) and the host-side server RPC handlers (`serverapp/rpc_table.go`).

## Frontend `ctx.rpc` — `BackendRPC` Table

```ts
export interface RPCAPI {
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
```

| Method | Params | Result |
|--------|--------|--------|
| `session.get` | `{ chatID: string }` | `SessionDetail` (chatID, title, model, busy, maxContext, maxOutput, tokenUsage, createdAt) |
| `session.list` | `{}` | `SessionSummary[]` (chatID, title, model, busy, maxContext, tokenUsage) |
| `agent.send` | `{ chatID: string; content: string }` | `{ turnID: number; queued: boolean }` |
| `agent.cancel` | `{ chatID: string }` | `{}` |
| `plugin.list` | `{}` | `PluginInfo[]` (id, name, version, enabled) |
| `plugin.get_config` | `{ id: string }` | `{ configuration: PluginConfigSchema; values: Record<string, unknown> }` |
| `plugin.set_config` | `{ id: string; key: string; value: unknown }` | `{ status: string; key: string }` |
| `git.status` | `{ channel: string; chatID: string }` | Branch, repo_name, changes[], ahead, behind, commit_hash, commit_msg, is_repo |
| `git.log` | `{ channel: string; chatID: string; limit?: number }` | `{ commits: Array<{ hash; author; when; subject }> }` |
| `git.diff` | `{ channel: string; chatID: string; path: string }` | `{ path: string; content: string }` |
| `git.branches` | `{ channel: string; chatID: string }` | `{ current: string; branches: string[] }` |

The `git.*` methods are contributed by the `xbot.git-fancy` plugin — the pattern for backend plugins publishing typed data sources.

## Plugin-to-Backend RPC (`ctx.rpc.call('<pluginId>.<method>')`)

Frontend plugin views route arbitrary RPC to the owning backend plugin process via `web_plugin_rpc`. The Go backend declares the `rpc` permission (`PermRPC`) for this. The backend `Handler.WebPluginRPC` receives `WebPluginRPCParams{ Method, Params }` and returns `WebPluginRPCResult{ Result, Error }` (opaque JSON strings).

## Host RPC Handlers (serverapp)

| RPC | Params | Purpose |
|-----|--------|---------|
| `plugin_list` | — | List plugin entries (manifest, state) with `active`/`total` counts. |
| `plugin_reload` | `{ id }` | Hot-reload one plugin; broadcasts `web_plugin_init` for web-declared plugins. |
| `plugin_reload_all` | — | Hot-reload all plugins; broadcasts `web_plugin_init` for each web-declared plugin. |
| `plugin_widgets` | `{ chat_id, structured }` | Render all widget zones for a session (`zones`, `infos`, `count`). `structured=true` returns web span structures instead of ANSI. |
| `web_plugin_list` | — | List plugins with web declarations only. |
| `web_plugin_rpc` | `{ plugin_id, method, params }` | Route a frontend plugin view RPC to the backend plugin process. |
| `web_ui_action` | `{ widget_id, action, data, chat_id }` | User interaction with a web UI component. Routing priority: owning channel plugin → native handler → agent loop injection. |
| `genui_action` | — | Action for GenUI-rendered components (analogous routing to `web_ui_action`). |

## Stdio Protocol Methods (plugin process side)

The NDJSON protocol dispatches these methods to the plugin's `Handler` (see `plugin/protocol/protocol.go`):

| Method | Handler Field | Direction |
|--------|--------------|-----------|
| `activate` | `Handler.Activate` | host → plugin |
| `deactivate` | `Handler.Deactivate` | host → plugin (process exits after) |
| `execute_tool` | `Handler.ExecuteTool` | host → plugin |
| `hook` | `Handler.Hook` | host → plugin |
| `enrich` | `Handler.Enrich` | host → plugin |
| `web_ui_action` | `Handler.WebUIAction` | host → plugin |
| `web_plugin_rpc` | `Handler.WebPluginRPC` | host → plugin |

Unknown methods receive `{"error": "unknown method: <name>"}`.
