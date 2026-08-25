---
title: "类型即契约"
weight: 2
---

Web 插件系统的核心思想：**类型系统即契约**。插件能做的一切都体现为编译器可检查的签名——没有运行时 `any` 魔法、没有隐式作用域、不借 PL 术语（`effect`/`fiber`/`epoch`）。清理函数就叫 `Disposable`。

四件类型武器：

1. **判别联合** —— 贡献点形状（`web/src/plugin-api/manifest.ts` 的 `Contribution`）。
2. **能力即类型** —— `PluginContext<P>` 把声明的权限映射为可用能力接口；未声明的能力是 `never`（`web/src/plugin-api/context.ts`）。
3. **条件类型精化** —— 渲染器的 `matches` 条件精化 `render` 收到的消息类型（`web/src/plugin-api/renderer.ts`）。
4. **声明合并** —— `EventMap`、`BackendRPC`、`ToolResultMap`、`PluginExportsMap` 是扩展点，后端插件与类型包通过发布 `.d.ts` 增补。

## 1. 判别联合 + `satisfies`

每个贡献点是判别联合的成员。插件用 `satisfies PluginManifest` 让编译器校验形状、**同时保留字面量类型**（供后续 `PluginContext<typeof manifest.permissions>` 推导）：

```ts
// web/src/plugin-api/manifest.ts
export type Contribution =
  | ViewContribution
  | CommandContribution
  | MessageRendererContribution
  | ToolbarContribution
  | ContextMenuContribution
  | SettingContribution
  | EventHandlerContribution
  | ThemeContribution

export interface PluginManifest {
  id: string
  name: string
  version: string
  description?: string
  permissions?: readonly Permission[]
  activationDependencies?: readonly string[]
  contributes: readonly Contribution[]
  entry?: string
}
```

```ts
export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'commands', 'rpc'] as const,
  contributes: [
    { kind: 'view', id: 'demo.panel', container: 'right_sidebar',
      title: 'Demo', entry: './panel' },
    { kind: 'command', id: 'demo.hello', title: 'Hello', keybinding: 'ctrl+shift+h' },
  ] as const,
} satisfies PluginManifest   // 错误的 kind / 缺字段 → 编译错误
```

- **穷尽性**：`contributes` 的每个元素必须命中联合的某个成员；`switch (c.kind)` 处理贡献点时，新增 kind 会触发编译器"未处理分支"警告——扩展系统演进时类型强制同步。
- **运行时**：manifest 序列化为 JSON 由后端下发，但**权威类型在编译期**。

## 2. 能力即类型——权限决定 ctx 形状

```ts
// web/src/plugin-api/context.ts
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
  readonly meta: PluginMeta
  readonly contributes: ContributionAPI
}
```

`PluginContext<P>` 是类型函数：对每个能力 `K`，仅当 `K ∈ P` 时该能力接口可用——否则为 `never`，访问未声明的能力是**编译错误**，不是运行时错误：

```ts
export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  ctx.events.on('message.committed', ev => { /* OK：已声明 events */ })
  ctx.rpc.call('session.get', { chatID: 'x' })   // OK：已声明 rpc

  // ✗ 编译错误：'state' 不在 permissions → ctx.state 是 never
  // ctx.state.getSession()
  // ✗ 编译错误：'session.get' 的参数必须是 { chatID: string }
  // void ctx.rpc.call('session.get', { chatID: 42 })
}
```

运行时镜像这一设计：`web/src/plugin-runtime/context.ts` 的 `buildContext(permissions, svc)` 只注入已声明的能力——未声明的在对象上不存在（纵深防御，权威在编译期）。

为什么这是"真类型设计"（而不是 cordis 式伪装）：

- 权限 → 能力的映射是**显式的类型函数**，不是运行时 Proxy 白名单。
- 插件作者**不可能**用到未声明的能力——不是"会报错"，而是"编译不过"。
- 每个能力方法都有完整签名（参数、返回、载荷），无 `any` 泄漏。

## 3. 条件类型精化——匹配了什么，就能安全处理什么

```ts
// web/src/plugin-api/renderer.ts
export type MatchedMessage<M extends Matcher> =
  M extends { tool: infer T extends keyof ToolResultMap }
    ? SafeMessage & { tool: { name: T; result: ToolResultMap[T] } }
    : M extends { uiMode: string }
      ? SafeMessage & { tool: { name: string; uiMode: string; result: unknown } }
      : M extends { role: 'assistant' }
        ? SafeAssistantMessage
        : M extends { role: 'user' }
          ? SafeUserMessage
          : SafeMessage

export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  priority: number
  matches: M
  render: (msg: MatchedMessage<M>, ctx: RenderContext) => ReactNode | null
}
```

`matches` 条件既是运行时过滤器，又是类型源头："你声明匹配了什么，就能安全处理什么"。声明 `matches: { tool: 'shell' }` 的渲染器收到的 `tool.result` 类型就是 `{ command: string; output: string; exitCode: number }`——零 cast。完整派发链见[消息渲染器](message-renderer.md)。

## 4. 声明合并——生态扩展点

四个可增补的接口是生态扩展点。后端插件发布 `.d.ts` 类型包合并进来；前端插件 import 类型包后自动获得精确类型：

| 接口 | 由谁扩展 | 消费方 |
|---|---|---|
| `EventMap` | 后端插件、宿主 | `ctx.events.on(name, …)` 载荷推导 |
| `BackendRPC` | 后端插件（如 `xbot.git-fancy` 增补 `git.status` 等） | `ctx.rpc.call(method, …)` 参数/返回推导 |
| `ToolResultMap` | 带自定义工具的后端插件 | 消息渲染器匹配精化 |
| `PluginExportsMap` | 发布公共 API 的插件 | `ctx.plugins.get(id)` 返回类型 |

示例（来自 `web/src/plugin-api/rpc.ts`——真实方法表已包含 git-fancy 扩展）：

```ts
export interface BackendRPC {
  'session.get': { params: { chatID: string }; result: SessionDetail }
  // …
  // ---- xbot.git-fancy：fancy Git 插件数据源 ----
  'git.status': {
    params: { channel: string; chatID: string }
    result: { branch: string; repo_name: string; changes: Array<{ path: string; status: string; added: number; deleted: number }>; ahead: number; behind: number; commit_hash: string; commit_msg: string; is_repo: boolean }
  }
}
```

空接口（如 `PluginExportsMap {}`）是声明合并的合法扩展点——`eslint-disable no-empty-object-type` 注释标记。
