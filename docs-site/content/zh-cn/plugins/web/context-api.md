---
title: "PluginContext API"
weight: 4
---

`PluginContext<P>` 是**对权限的类型函数**：声明的能力以完整类型接口可用；未声明的能力是 `never`。定义于 `web/src/plugin-api/context.ts`。

## 类型

```ts
interface PermissionAPI {
  events: EventsAPI
  commands: CommandsAPI
  rpc: RPCAPI
  state: StateAPI
  ui: UIAPI
  plugins: PluginsAPI
  config: ConfigAPI
}

export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & {
  /** 运行时元信息（所有插件可用）。 */
  readonly meta: PluginMeta
  /** 动态贡献点注册（所有插件可用）。 */
  readonly contributes: ContributionAPI
}
```

`meta` 与 `contributes` 两个成员**无论权限如何都可用**。

## 运行时构建

`web/src/plugin-runtime/context.ts` 的 `buildContext(permissions, svc)` 构建与类型形状一致的运行时对象——只注入已声明的能力，未声明的在对象上不存在（纵深防御，权威在编译期）：

```ts
export function buildContext(
  permissions: readonly Permission[],
  svc: ContextServices,
): PluginContext<readonly Permission[]> {
  const ctx: Record<string, unknown> = {
    meta: svc.meta,
    contributes: {
      register: svc.registerContribution,
      registerAll: (contributions) => {
        const disposables = contributions.map((c) => svc.registerContribution(c))
        return () => { for (const d of disposables.reverse()) d() }
      },
    } satisfies ContributionAPI,
  }
  const has = (p: Permission) => permissions.includes(p)
  if (has('events')) ctx.events = svc.events
  if (has('commands')) ctx.commands = svc.commands
  if (has('rpc')) ctx.rpc = svc.rpc
  if (has('state')) ctx.state = svc.state
  if (has('ui')) ctx.ui = svc.ui
  if (has('plugins')) ctx.plugins = svc.plugins
  if (has('config')) ctx.config = svc.config
  return ctx as PluginContext<readonly Permission[]>
}
```

运行时在 `PluginRuntime` 构造函数中实例化全部服务并跨插件共享；`config` 是例外——每个插件拿到自己的 `PluginConfigService.forPlugin(pluginId)` 绑定（见 `web/src/plugin-runtime/index.ts` `activateModule`）。

## 能力速查

| 能力 | 权限 | API 面 | 文档 |
|---|---|---|---|
| `ctx.events` | `events` | `EventMap` 上的 `on` / `once` | [事件总线](events.md) |
| `ctx.commands` | `commands` | `register` / `execute` / `registerKeybinding` | 本页 |
| `ctx.rpc` | `rpc` | `BackendRPC` 上的 `call` / `notify` | [RPC](rpc.md) |
| `ctx.state` | `state` | `getSession` / `getMessages` / `getPlugins` | 本页 |
| `ctx.ui` | `ui` | `showToast` / `openPanel` / `openViewTab` / `openFileTab` / `openDiffTab` | [Editor View API](editor-view.md) |
| `ctx.plugins` | `plugins` | `get` / `require` / `onActivated` / `onDeactivated` | [插件互操作](interop.md) |
| `ctx.config` | `config` | `get` / `set` / `onConfigChange` | 本页 |
| `ctx.meta` | —（恒可用） | `{ id, version }` | — |
| `ctx.contributes` | —（恒可用） | `register` / `registerAll` | [Manifest](manifest.md) |

## CommandsAPI

```ts
export interface CommandsAPI {
  /** 注册命令处理器；返回 disposable 用于卸载。 */
  register(id: string, handler: (args: unknown) => void | Promise<void>): Disposable
  /** 执行一个已注册命令。 */
  execute(id: string, args?: unknown): Promise<void>
  /** 注册快捷键（keybinding 语法与贡献点一致）。 */
  registerKeybinding(keybinding: string, commandId: string): Disposable
}
```

实现为 `CommandRegistry`（`web/src/plugin-runtime/commands.ts`）：重复注册覆盖旧 handler 并告警；宿主把 `KeyboardEvent` 经 `dispatchKey` 分发（`keybindingFromEvent` 规范化为 `ctrl+shift+h` 这类字符串）；卸载插件时 `removePlugin` 移除其全部命令与快捷键。

## StateAPI —— 只读快照

```ts
export interface StateAPI {
  /** 当前会话摘要（只读快照）。 */
  getSession(): SessionSummary | null
  /** 分页只读消息列表（sanitize 副本）。 */
  getMessages(options?: { limit?: number; before?: number }): readonly SafeMessage[]
  /** 所有插件状态。 */
  getPlugins(): readonly { id: string; version: string; enabled: boolean }[]
}
```

实现（`web/src/plugin-runtime/state.ts`）返回 `structuredClone` 后的数据——插件永远拿不到内部对象引用，无法窥探后续变化。消息经过 `toSafeMessage`（`web/src/plugin-runtime/sanitize.ts`）裁剪，只保留公共字段（`id`/`turnID`/`role`/`content`/`createdAt`），丢弃内部状态（`persisted`/`eventSeq`/`dbID`/…）。

## ConfigAPI

```ts
export interface ConfigAPI {
  /** 读取当前合并后的配置值（默认值 + 用户覆盖）。 */
  get(): Promise<Record<string, unknown>>
  /** 设置单个配置键并持久化。后端广播变更，触发所有 onConfigChange 订阅者。 */
  set(key: string, value: unknown): Promise<void>
  /** 订阅配置变更（含其它客户端/标签页发起的修改）。返回 disposable。 */
  onConfigChange(handler: (config: Record<string, unknown>) => void): Disposable
}
```

链路（`web/src/plugin-runtime/config.ts` + 后端）：`get()`/`set()` 走 `plugin.get_config` / `plugin.set_config` RPC；变更落地后后端经 WS 广播 `web_plugin_config_changed`，`usePluginRuntimeHost` 调 `runtime.notifyPluginConfigChanged(pluginId, value)`，`PluginConfigService` 分发到该插件的 `onConfigChange` 订阅者。宿主设置面板**不走**本 API——它直接按 `plugin.get_config` 的 schema 渲染表单。

## 示例：权限驱动的能力面

```ts
import type { PluginContext, Disposable } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'ui'] as const,
  contributes: [],
} satisfies import('@xbot/plugin-api').PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  // OK —— 已声明 events
  const off = ctx.events.on('turn.ended', (ev) => {
    // OK —— 已声明 ui
    ctx.ui.showToast(`turn ${ev.turnID}: ${ev.outcome}`)
  })
  // ✗ 编译错误 —— 未声明 'rpc' → ctx.rpc 是 never
  // void ctx.rpc.call('session.list', {})
  return () => off()
}
```
