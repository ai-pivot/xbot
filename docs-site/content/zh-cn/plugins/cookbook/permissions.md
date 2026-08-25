---
title: "权限系统"
weight: 17
---

插件声明所需能力；xbot 强制执行。目录在后端 `plugin/permissions.go` 与前端 `web/src/plugin-api/manifest.ts`。权限模型是**声明式**的：插件只能访问它声明过的 API——前端在**编译期**强制（能力即类型），后端靠约定与注册路径。

## 后端权限目录

`plugin/permissions.go` 常量：

| 权限 | 授予 |
|---|---|
| `tools.register` | 注册工具 |
| `tools.call` | 调用工具 |
| `hooks.subscribe` | 订阅生命周期 Hook |
| `context.enrich` | 注册上下文注入器 |
| `storage.private` | 私有键值存储 |
| `storage.shared` | 共享插件存储 |
| `network.outbound` | 出站网络请求 |
| `bus.read` | 订阅事件总线 |
| `bus.write` | 发布到事件总线 |
| `bus.plugin` | 使用插件间事件总线（另需 `bus.read`/`bus.write`） |
| `ui.contribute` | 贡献 UI 组件 |
| `ui.themes` | 贡献主题 |
| `ui.overlay` | 注册/控制覆盖层 |
| `channels.register` | 注册自定义 Channel 提供者 |
| `commands.register` | 注册斜杠命令 |
| `cron.schedule` | 调度定时任务 |
| `notifications.send` | 发送通知与播放声音 |
| `rpc` | 前端视图 ↔ 后端进程 RPC（`web_plugin_rpc`）——对应前端 `'rpc'` |
| `events` | 类型化事件总线（`ctx.events`）——对应前端 `'events'` |
| `commands` | 命令注册/执行（`ctx.commands`）——对应前端 `'commands'` |
| `state` | 键值状态存储（`ctx.state`）——对应前端 `'state'` |
| `ui` | UI 能力：toast、面板、编辑器视图 tab（`ctx.ui`）——对应前端 `'ui'` |
| `plugins` | 插件间注册表（`ctx.plugins`）——对应前端 `'plugins'` |

前端权限 `'config'`（插件自身配置）存在于类型系统中（`web/src/plugin-api/context.ts Permission`），镜像后端由 `Config()` 授予的配置访问。

## 检查权限

```go
checker := plugin.NewPermissionChecker(manifest.Permissions)
checker.Has(plugin.PermToolsRegister)
checker.HasAll("bus.plugin", "bus.read")
checker.HasAny("ui.contribute", "ui.themes")
```

`"*"` 条目授予一切（通配符）。`IsValidPermission(p)` 判断字符串是否为已知权限。

## 在清单中声明

```json
{
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich", "storage.private"]
}
```

清单校验在加载时**拒绝未知权限字符串**（`plugin/manifest.go validateManifest`）——`"tool.register"` 这样的笔误导致发现失败，而非静默授予空权限。

## 前端：能力即类型

```ts
permissions: ['rpc', 'events'] as const,

export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  ctx.rpc.call('plugin.list', {})   // ✅ 已声明 'rpc'
  ctx.ui.showToast('hi')            // ❌ 编译错误：'ui' 不在 P → never
}
```

`PluginContext<P>` 仅在声明时把每个 `Permission` 映射为对应 API（`web/src/plugin-api/context.ts`）。运行时 `buildContext` 严格按声明注入——未声明的 API 是 `undefined`。

## 同步规则（关键）

⚠️ **后端白名单与前端 `Permission` 类型必须同步。** `plugin/permissions.go` 的 `allPermissions` 编译进 Go 二进制；清单声明了白名单里没有的新前端权限会在 reload 时被拒绝（`unknown permission "ui"` 是真实事故）。新增前端 `Permission` 值时，在同一次改动中更新 `permissions.go` 常量 + `allPermissions`（遵循 `PermRPC` 的注释模式：`// Matches the frontend Permission 'rpc' (web/src/plugin-api/manifest.ts)`）。

## 最佳实践

1. **只声明用到的权限。** 权限在插件面板中展示给用户——精简的列表建立信任。git-fancy 事故同时证明反面：**声明你用到的每个权限**——漏掉 `"ui"` 会让 `ctx.ui` undefined，静默 no-op 失败。
2. **事件总线需要三个权限**（`bus.plugin` + `bus.read` + `bus.write`）——单有一个不起作用。
3. **在测试中检查 `IsValidPermission` 输出**——保留一个单元测试断言清单权限列表可解析（见 `plugins/xbot-git-fancy/main_test.go` 的 `TestManifestPermissions` 模式）。
4. `"*"` 可用但不推荐在调试之外使用。
