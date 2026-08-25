---
title: "权限列表"
weight: 7
---

权限字符串完整列表（`plugin/permissions.go`）。插件在 `plugin.json` 的 `permissions` 中声明。通配符 `"*"`（仅 Go 侧）授予全部；未知权限在 manifest 校验时失败。

## 后端权限（Go）

| 常量 | 字符串 | 授予 |
|------|--------|------|
| `PermToolsRegister` | `tools.register` | 注册工具与中间件（`RegisterTool`、`RegisterTools`、`UseMiddleware`）。 |
| `PermToolsCall` | `tools.call` | 调用工具。 |
| `PermHooksSubscribe` | `hooks.subscribe` | 订阅生命周期 hook（`OnPreToolUse` 等）与注册插件错误回调。 |
| `PermContextEnrich` | `context.enrich` | 注册上下文注入器（`EnrichContext`）。 |
| `PermStoragePrivate` | `storage.private` | 访问插件私有键值存储（`Storage()` 及类型化助手）。 |
| `PermStorageShared` | `storage.shared` | 访问共享插件存储。 |
| `PermNetworkOutbound` | `network.outbound` | 发起出站网络请求。 |
| `PermBusRead` | `bus.read` | 从事件总线读取（配合 `bus.plugin`）。 |
| `PermBusWrite` | `bus.write` | 发布到事件总线（配合 `bus.plugin`）。 |
| `PermBusPlugin` | `bus.plugin` | 使用插件间事件总线。`Subscribe` 需要 `bus.plugin`+`bus.read`；`Publish` 需要 `bus.plugin`+`bus.write`。 |
| `PermUIContribute` | `ui.contribute` | 贡献 UI widget（`ContributeUI`）。manifest 的 `contributes.ui` 同样要求此权限。 |
| `PermChannelsRegister` | `channels.register` | 注册自定义 Channel provider。 |
| `PermCommandsRegister` | `commands.register` | 注册斜杠命令。 |
| `PermCronSchedule` | `cron.schedule` | 调度与取消定时任务。 |
| `PermUIThemes` | `ui.themes` | 贡献主题（`ContributeTheme`）。 |
| `PermUIOverlay` | `ui.overlay` | 注册与控制覆盖层（`RegisterOverlay`、`ShowOverlay`、`HideOverlay`）。 |
| `PermNotificationsSend` | `notifications.send` | 发送通知与播放音效（`Notify`、`PlaySound`）。 |

## 前端权限（Web 插件 v2）

与 `web/src/plugin-api/manifest.ts` 的 `Permission` 类型一致，且同时注册在 Go 后端白名单（`allPermissions`）中——**两份名单必须保持同步**（不同步会导致 reload 失败：`unknown permission "ui"`）：

| 权限 | 授予（前端 `PluginContext<P>` 能力） |
|------|--------------------------------------|
| `events` | 类型化事件总线访问（`ctx.events.on/once`）。 |
| `commands` | 命令注册/执行（`ctx.commands.register/execute/registerKeybinding`）。 |
| `rpc` | 后端 RPC 调用（`ctx.rpc.call/notify`）。 |
| `state` | 键值状态存储（`ctx.state`）。 |
| `ui` | UI 能力：toast、面板开关、编辑器视图 tab（`ctx.ui.openViewTab/openFileTab`）。 |
| `plugins` | 插件间注册表（`ctx.plugins`）。 |
| `config` | 插件配置 API（`ctx.config`）。 |

## 强制机制

- **Go**：`PermissionChecker`（`NewPermissionChecker`、`Has`、`HasAll`、`HasAny`）——`pluginContextImpl` 在每个能力调用处检查权限，违规返回 `*PermissionError`（通知类静默警告）。
- **Web**：能力即类型——`PluginContext<P>` 仅暴露权限位于 `P` 中的能力接口；访问未声明能力是编译期错误。
