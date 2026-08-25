---
title: "插件间互操作"
weight: 9
---

插件的公共 API = **模块命名导出**（除保留的 `manifest`/`activate`/`deactivate` 外）。消费方通过 `ctx.plugins` 获取；类型来自 `PluginExportsMap` 声明合并表。定义于 `web/src/plugin-api/plugins.ts`。

## PluginExportsMap

```ts
/** 空表；各插件发布类型包扩展（声明合并）。 */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type -- 声明合并的合法空表
export interface PluginExportsMap {}
```

发布公共 API 的插件附带 `.d.ts` 类型包：

```ts
declare module '@xbot/plugin-api' {
  interface PluginExportsMap {
    'xbot.git-info': {
      refresh(): Promise<void>
      readonly branch: string | null
    }
  }
}
```

消费方 import 类型包后，`ctx.plugins.get('xbot.git-info')` 获得类型。

## PluginsAPI

```ts
export interface PluginsAPI {
  /** 同步取已激活插件的公共 API；未激活/禁用/崩溃 → undefined。 */
  get<K extends keyof PluginExportsMap>(id: K): PluginExportsMap[K] | undefined
  /** 异步确保依赖插件激活后返回其 API（可选依赖的懒加载入口）。 */
  require<K extends keyof PluginExportsMap>(id: K): Promise<PluginExportsMap[K]>
  /** 订阅依赖插件的激活/停用（动态上下线时降级/恢复）。 */
  onActivated<K extends keyof PluginExportsMap>(id: K, h: (api: PluginExportsMap[K]) => void): Disposable
  onDeactivated<K extends keyof PluginExportsMap>(id: K, h: () => void): Disposable
}
```

## 运行时实现

`PluginInterop`（`web/src/plugin-runtime/plugins.ts`）包装 `ContributionRegistry`：

- `get` 读 `registry.getExports(id)`——激活时记录的导出表。
- `require` 回退到按需激活：运行时给 registry 注入 `ensureActive(id)`（构造接线在 `web/src/plugin-runtime/index.ts`），从后端拉插件声明（`web_plugin_list`）并激活。无法激活则抛错。
- `onActivated` 若依赖已激活则**立即通知一次**（订阅者拿到当前 API），此后每次激活再通知；`onDeactivated` 在依赖卸载（热加载）时触发——消费方优雅降级/恢复。
- `notifyActivated`/`notifyDeactivated` 由 `PluginRuntime.activateModule`/`deactivate` 调用。

## 收集导出

```ts
/** 插件模块命名导出 = 公共 API（保留键除外）。 */
function collectPluginExports(mod: PluginModule): Record<string, unknown> {
  const reserved = new Set(['manifest', 'activate', 'deactivate'])
  const out: Record<string, unknown> = {}
  for (const key of Object.keys(mod)) {
    if (!reserved.has(key)) out[key] = mod[key]
  }
  return out
}
```

## 激活依赖

`manifest.activationDependencies` 声明**强**依赖：必须先于本插件激活的插件 id。校验门控（`ContributionRegistry.validate`）在依赖缺失时拒绝激活：

```ts
for (const dep of manifest.activationDependencies ?? []) {
  if (!this.plugins.has(dep)) {
    return { pluginId: manifest.id, message: `缺少强依赖插件: ${dep}` }
  }
}
```

`PluginRuntimeBootstrap` 先激活内置插件、再激活第三方插件——激活顺序服从下发的清单，强依赖由门控强制执行。

## 示例

```ts
// 提供方插件
export const manifest = {
  id: 'xbot.data-service',
  name: 'Data Service',
  version: '1.0.0',
  permissions: ['rpc'] as const,
  contributes: [],
} satisfies PluginManifest

export async function query(chatID: string) {
  return { rows: [] as readonly string[] }
}

// 消费方插件
export const manifest = {
  id: 'xbot.consumer',
  name: 'Consumer',
  version: '1.0.0',
  permissions: ['plugins', 'rpc'] as const,
  activationDependencies: ['xbot.data-service'],
  contributes: [],
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  // 同步：data-service 已激活（强依赖保证）
  const api = ctx.plugins.get('xbot.data-service')
  void api?.query('x')

  // 异步：OPTIONAL 依赖经 onActivated/require 懒加载
  const off = ctx.plugins.onDeactivated('xbot.data-service', () => {
    ctx.ui.showToast('data service 下线', 'error')
  })
  return () => off()
}
```
