---
title: "插件管理面板"
weight: 14
---

插件管理面板（`xbot.plugin-manager`）是整个系统的自举范例：**它本身就是一个插件**，只消费公开 API——对能力模型的高保真演示（dogfooding）。第三方可以写更好的面板替换它。

## 插件侧

`web/src/plugins/manager/pluginManager.ts`：

```ts
export const manifest = {
  id: 'xbot.plugin-manager',
  name: 'Plugin Manager',
  version: '0.1.0',
  description: '管理插件：查看/启用/禁用/卸载/重载（自举实现，本身也是一个插件）',
  permissions: ['rpc', 'plugins', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.plugin-manager.panel',
      container: 'right_sidebar',
      title: '插件',
      icon: 'blocks',
      // 内置视图标记：宿主 loadViewComponent 识别此标记直接返回静态组件。
      entry: 'builtin:xbot.plugin-manager.panel',
    },
  ],
} satisfies PluginManifest

export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  return () => {}   // 无初始化副作用；视图由贡献点渲染
}
```

它在启动时由 `PluginRuntimeBootstrap` 经 `runtime.activateBuiltin(manifest, module)` **最先**激活——与第三方插件同一条门控/校验/激活路径，只是静态 import（不走 URL 加载）。

## 面板

`PluginManagerPanel.tsx` 在 `BuiltinView` 内渲染（同步渲染——内置视图为何不走异步加载见[ESM 模块格式](module-format.md)）。数据流：

| 动作 | 机制 |
|---|---|
| 列出插件 | `runtime.rpc.call('plugin_status', { rescan: true })` —— 重新扫描磁盘插件目录，返回**后端 Go 插件系统**的插件（script/grpc runtime） |
| 重载 | `plugin_reload` RPC → 刷新 |
| 启用/禁用 | `plugin_set_enabled` RPC → **主动同步前端运行时**（见下） |
| 安装 | `installPluginFile` 上传（文件输入） |

面板展示**后端**插件全景（daily-jokes、dashboard、git-info、github、theme-party……）——与前端 Web 插件 v2 运行时是两套系统。它同时经 `runtime.listPluginStates()` / `usePluginRuntime()` 列出前端运行时插件状态（`PluginRuntimeState`：id/name/version/status/permissions/contributionIds）。

## 启用/禁用：主动前端同步

切换插件状态时**主动**同步前端运行时——WS `web_plugin_init` 广播不被信任为唯一路径（广播链路不可靠）：

```ts
await runtime.rpc.call('plugin_set_enabled' as never, { id, enabled } as never)
// 拉最新 web_plugin_list，对该插件 activate / deactivate。
const res = await runtime.rpc.call('web_plugin_list' as never, {} as never) as { plugins?: WebPluginDecl[] }
const decl = res?.plugins?.find((p) => p.id === id)
if (decl) {
  if (enabled && decl.enabled) {
    await runtime.activate(toManifest(decl), decl.module_url)
  } else if (!enabled) {
    runtime.deactivate(id)
  }
}
```

## PluginRuntimeState

管理面板展示的运行时状态（`web/src/plugin-runtime/registry.ts`）：

```ts
export interface PluginRuntimeState {
  id: string
  name: string          // 来自 manifest.name
  version: string
  enabled: boolean
  status: 'active' | 'inactive' | 'error' | 'reloading'
  error?: string
  permissions: readonly Permission[]
  contributionIds: readonly string[]   // 面板展示用
}
```

状态变化经 `RegistryHooks.onStateChange` → `PluginRuntimeHost.onPluginStateChange` 上报（管理面板数据源）。

## 启动时禁用插件必须跳过

`PluginRuntimeBootstrap` 激活初始清单时跳过 `enabled=false` 的插件——禁用的前端插件不得注册 view 并生效：

```ts
for (const decl of res?.plugins ?? []) {
  if (!decl.enabled) {
    console.debug(`[plugin-runtime] 跳过已禁用的插件 ${decl.id}（enabled=false）`)
    continue
  }
  bumpPluginLoadToken(decl.id)
  const manifest = toManifest(decl)
  await runtime.activate(manifest, decl.module_url)
}
```

## 权限白名单同步

后端权限白名单（`plugin/permissions.go` 的 `allPermissions`）必须列出运行时用到的每个前端 `Permission` 值——面板的 `plugin_reload` 会按白名单重校验 manifest。新增前端权限需要对应 Go 常量（注释模式：`// Matches the frontend Permission 'rpc' (web/src/plugin-api/manifest.ts)`）。白名单在 Go 二进制里——使用新权限的 reload 需要重启 server。

## 清单重载

`/plugin reload-all`（agent 命令）或重启 server 触发后端重载；`WatchConfig` **未接线**——改 config.json 的 `disabled_plugins` 不触发重载。stdio 插件二进制更新后同样需要 `/plugin reload`。
