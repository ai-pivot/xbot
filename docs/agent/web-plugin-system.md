# xbot Web 插件系统 v2 设计方案（类型即契约）

> 状态：设计稿（待评审）
> 目标：让插件**随意强化 Web UI**。核心原则：
> 1. **无服务器端 VM** —— 前端插件是编译后的 ESM 模块，直接在前端加载执行；后端仍是现有 Go/stdio 进程模型。
> 2. **无沙箱** —— 插件是可信代码（信任由安装决定，同浏览器扩展/VSCode 扩展模型）。类型系统约束**契约正确性**，不假装做安全边界。
> 3. **类型即契约（Type-as-Contract）** —— 用真正的类型系统（判别联合、能力即类型、条件类型精化、声明合并）在**编译期**约束插件能做的一切：贡献点形状、能力 API、事件载荷、RPC 方法、渲染器匹配-参数关联。

---

## 1. 前置批判：为什么 cordis 的"类型/effect"是假的（教训提炼）

`~/src/deepseek-harness/vendor/cordis` 的 `ctx.effect()` 本质是**作用域资源生命周期管理**（立即执行回调 → 收集 disposer → 卸载时逆序清理），与 PL 的 algebraic effect（类型化签名 / perform 挂起 / handler 接管 / 可 resume）毫无关系。它借用了 `effect`/`fiber`/`epoch`/`context` 一整柜 PL 词汇，但：

| PL 术语 | cordis 实际 | 真语义 |
|---|---|---|
| `effect` | disposer 收集器 | 可挂起/恢复的控制流 |
| `fiber` | 插件实例 | 轻量协程 |
| `epoch` | 布尔标记 | 代数效应世代 |
| `context` | DI 作用域链 | typeclass/上下文传递 |

**"设计撒谎"的本质**：用 PL 词汇装饰普通 DI 容器。**更深一层的谎言**：cordis 的 `Context` 是**隐式作用域链**——`ctx.on/ctx.provide` 在链上运行时查找实现，调用者无法从签名得知"谁在提供、提供什么形状"。它连**类型化的接口契约**都没有，全是 `any` 形状的运行时魔法。

**本方案从反面出发**：
- 机制叫什么就叫什么（`disposable`/`event`/`rpc`/`contribution`），不借 PL 术语。
- **每个接口都有真类型**：插件能做的每一件事，都体现为编译器可检查的签名。
- **无隐式作用域**：能力通过显式的类型化 `ctx` 注入，未声明的能力在类型上就是 `never`。

---

## 2. 架构总览（无 VM，无沙箱）

```
┌─ xbot Web 前端（插件运行的地方）────────────────────────────┐
│                                                            │
│  PluginRuntime（web/src/plugin-runtime/）                   │
│  ├── loader.ts    ESM 动态 import（编译后模块，版本化 URL）   │
│  ├── registry.ts  类型化贡献点注册表（views/commands/…）      │
│  ├── events.ts    类型化事件总线（EventMap 索引访问）         │
│  ├── rpc.ts       类型化 RPC 桥（BackendRPC 方法表）          │
│  └── slots/       ViewSlot / ToolbarSlot / MessageRenderer   │
│                                                            │
│  插件模块直接 import 进宿主 React 运行时                      │
│  崩溃隔离 = React ErrorBoundary（组件级）+ 贡献点级回滚       │
└──────────────┬─────────────────────────────────────────────┘
               │ web_plugin_* 消息（复用现有 WS/SSE）
┌──────────────▼─────────────────────────────────────────────┐
│ xbot 后端（Go）                                             │
│  ├── 插件激活管理：下发清单（贡献点 + 代码 URL + 权限）        │
│  ├── EventBridge：agent 生命周期事件 → 前端插件事件           │
│  └── WebPluginRPC：前端插件 ↔ 后端插件/核心的 RPC 路由        │
│  （后端插件 = 现有 Go 原生 / stdio 进程，无 VM）              │
└────────────────────────────────────────────────────────────┘
```

**信任模型（替代沙箱）**：
- 用户安装插件 = 信任插件（同 VSCode 扩展 / 浏览器扩展）。
- 类型系统保证**契约正确性**（API 形状、载荷类型、参数关联）。
- 运行时只做最小防御：贡献点 ID 冲突检测、版本检查、ErrorBoundary 崩溃隔离、插件级禁用开关。
- 生态治理：市场审核 + 发布者签名 + 插件页明示权限清单（用户知情）。
- LLM 生成的动态代码（GenUI）**保留 iframe 渲染隔离**——但它不是安全边界（AGENTS.md 已确认：组件代码本就在父页面编译，iframe 隔离的是渲染输出），只是视觉隔离 + 动态代码卫生，不在本方案安全模型之内。

---

## 3. 核心：类型即契约（Type-as-Contract）

### 3.1 插件清单（manifest）是类型化的——判别联合 + `satisfies`

每个贡献点是一个**判别联合的成员**，`satisfies` 让编译器校验形状、同时保留字面量类型（供后续类型推导）：

```ts
// @xbot/plugin-api/src/manifest.ts
import type { ReactNode } from 'react'

export type Contribution =
  | ViewContribution
  | CommandContribution
  | MessageRendererContribution<any>   // 见 §3.5 的精化
  | ToolbarContribution
  | ContextMenuContribution
  | SettingContribution
  | EventHandlerContribution<any>      // 见 §3.4
  | ThemeContribution

export interface PluginManifest {
  id: string
  name: string
  version: string
  /** 能力声明（§3.2）——类型源头 */
  permissions?: readonly Permission[]
  /** 贡献点（§3.1 起全部类型化） */
  contributes: readonly Contribution[]
  /** 前端模块入口（ESM）——可选，纯后端插件无此字段 */
  entry?: string
}
```

插件侧（单一真相源：`manifest` 既是运行时数据又是类型源头）：

```ts
// demo-plugin/src/index.ts
import type { PluginManifest, PluginContext, Disposable } from '@xbot/plugin-api'

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
} satisfies PluginManifest   // 编译期校验：错误的 kind/字段立刻报错
```

- **穷尽性**：`contributes` 的每个元素必须命中 `Contribution` 联合的某个成员；`switch (c.kind)` 处理贡献点时，新增 kind 会触发编译器"未处理分支"警告——**扩展系统演进时类型强制同步**。
- **运行时**：manifest 序列化为 JSON 下发给后端注册（贡献点声明仍然可以查、可以校验），但**权威类型在编译期**。

### 3.2 能力即类型（Capability-as-Type）——权限声明决定 ctx 形状

插件声明 `permissions`，`PluginContext<P>` 按权限**映射**能力接口；未声明的能力在类型上是 `never`——访问即编译错误：

```ts
// @xbot/plugin-api/src/context.ts
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config'

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
  /** 运行时元信息（所有插件可用） */
  readonly meta: PluginMeta
}
```

`config` 权限（§3.8 插件配置端到端）授予插件读写自身配置的能力：

```ts
export interface ConfigAPI {
  /** 读取合并后的配置值（manifest 默认值 + 用户覆盖）。 */
  get(): Promise<Record<string, unknown>>
  /** 持久化单个键；后端广播变更，触发所有 onConfigChange。 */
  set(key: string, value: unknown): Promise<void>
  /** 订阅配置变更（含其它客户端发起）。返回 disposable。 */
  onConfigChange(handler: (config: Record<string, unknown>) => void): Disposable
}
```

**配置声明**：插件在 `plugin.json` 的 `contributes.configuration` 声明 VSCode 风格 schema；前端插件也可在 `web.contributes` 数组里声明 `SettingContribution`（kind: 'setting'），后端 `plugin.ConfigSchema()` 统一提取（`contributes.configuration` 优先）。

**宿主设置面板**：设置对话框新增「插件」分类，`plugin_config` RPC（schema + 当前值）+ `plugin_config_set` 持久化，自动渲染表单并支持搜索过滤配置项（按 label/key/description）。**热重载**：改配置后经 `web_plugin_config_changed` WS 广播，`usePluginRuntimeHost` 调 `runtime.notifyPluginConfigChanged(pluginId, value)` 分发到对应插件的 `onConfigChange`。

插件侧的使用效果：

```ts
export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  ctx.events.on('message.committed', ev => { /* OK：已声明 events */ })
  ctx.commands.register('demo.hello', () => ctx.ui.showToast('hi'))   // OK：已声明 commands+rpc
  void ctx.rpc.call('session.get', { chatID: 'x' })                  // OK：已声明 rpc

  // ✗ 编译错误：未声明 'state' 权限 → ctx.state 类型为 never
  // ctx.state.getSession()
  // ✗ 编译错误：'session.get' 的参数必须是 { chatID: string }
  // void ctx.rpc.call('session.get', { chatID: 42 })

  return () => { /* 卸载清理 */ }   // disposable 就叫 disposable
}
```

**为什么这是"真类型设计"而不是 cordis 式的伪装**：
- 权限 → 能力的映射是**显式的类型函数**（`PluginContext<P>`），不是运行时 Proxy 白名单。编译器替代运行时拦截。
- 插件作者**不可能**用到未声明的能力——不是"会报错"，而是"编译不过"。
- 能力接口的每个方法都有完整签名（参数、返回、载荷），无 `any` 泄漏。

### 3.3 类型化事件总线——事件名 ↔ 载荷的索引访问

```ts
// @xbot/plugin-api/src/events.ts
export interface EventMap {
  'message.committed':   { turnID: number; message: SafeMessage }
  'message.streaming':   { turnID: number; iteration: number; content: string }
  'turn.started':        { turnID: number; trigger: 'user' | 'notification' | 'resume' }
  'turn.ended':          { turnID: number; outcome: 'ok' | 'cancelled' | 'error' }
  'session.switched':    { session: SessionSummary }
  'progress.iteration':  { iteration: number; tools: readonly ToolProgress[] }
  'context.compressed':  { beforeTokens: number; afterTokens: number }
  'command.executed':    { commandId: string; args: unknown }
}

export interface EventsAPI {
  on<K extends keyof EventMap>(
    name: K,
    handler: (payload: EventMap[K]) => void,
  ): Disposable
}
```

- 订阅 `'message.committed'` 时 payload 自动推断为 `{ turnID; message: SafeMessage }`——**零 cast**。
- `SafeMessage`/`SafeAssistantMessage` 等是经过 sanitize 的公共类型（前端从内部消息模型裁剪），插件永远接触不到内部字段。
- 后端插件可扩展自定义事件：发布 `.d.ts` 用**声明合并**向 `EventMap` 增补（§3.6）。

### 3.4 类型化 RPC——方法表驱动

```ts
// @xbot/plugin-api/src/rpc.ts
export interface BackendRPC {
  'session.get':     { params: { chatID: string };              result: SessionDetail }
  'session.list':    { params: {};                             result: SessionSummary[] }
  'agent.send':      { params: { chatID: string; content: string }
                       result: { turnID: number; queued: boolean } }
  'agent.cancel':    { params: { chatID: string };              result: {} }
}

export interface RPCAPI {
  call<K extends keyof BackendRPC>(
    method: K,
    params: BackendRPC[K]['params'],
  ): Promise<BackendRPC[K]['result']>
  notify<K extends keyof BackendRPC>(method: K, params: BackendRPC[K]['params']): void
}
```

- 前端插件调 `ctx.rpc.call('agent.send', ...)` 时，返回类型是 `Promise<{turnID, queued}>`，参数形状编译期校验。
- 前端插件 → **后端插件**的方法调用走同一张表（后端插件发布声明合并扩展）。

### 3.5 消息渲染器：匹配条件精化渲染参数类型（依赖类型思想的 TS 落地）

这是全案最有"PL 味道"的一处：**渲染器的 `matches` 条件在类型上精化 `render` 收到的消息类型**——匹配了什么，就能安全处理什么。类型系统保证"你声明的匹配条件"与"你的渲染函数能处理的形状"严格一致。

```ts
// @xbot/plugin-api/src/renderer.ts
import type { ReactNode } from 'react'

/** 工具结果类型表（核心内置 + 后端插件可扩展） */
export interface ToolResultMap {
  display_html: { code: string; summary: string }
  shell:        { command: string; output: string; exitCode: number }
  web_search:   { query: string; results: readonly unknown[] }
}

type Matcher =
  | { tool: keyof ToolResultMap }
  | { role: 'assistant' | 'user' | 'system' }
  | {}                                  // 通用匹配

/** 条件类型：匹配 → 消息类型精化 */
export type MatchedMessage<M extends Matcher> =
  M extends { tool: infer T extends keyof ToolResultMap }
    ? SafeMessage & { tool: { name: T; result: ToolResultMap[T] } }
  : M extends { role: 'assistant' }
    ? SafeAssistantMessage
  : M extends { role: 'user' }
    ? SafeUserMessage
  : SafeMessage

export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  priority: number                      // 大者优先；null 结果继续 fallback 到下一个
  matches: M
  render(msg: MatchedMessage<M>, ctx: RenderContext): ReactNode | null
}
```

用法（插件作者写出 `matches: { tool: 'display_html' }`，`render` 的 `msg` 自动带 `tool.result.code`）：

```ts
contributes: [
  {
    kind: 'messageRenderer',
    id: 'demo.codeview',
    priority: 10,
    matches: { tool: 'display_html' },
    render: (msg) => <CodeViewer code={msg.tool.result.code} />,   // 类型安全访问
  },
] satisfies Contribution[]
```

- **编译期保证**：`matches: { tool: 'shell' }` 的 renderer 无法访问 `display_html` 专属字段；`matches: { role: 'assistant' }` 的 renderer 收到 `SafeAssistantMessage`（有 `iterations`）。
- **后端插件的工具结果类型**：后端插件发布 `ToolResultMap` 的声明合并，前端插件即可安全匹配其工具——**跨插件契约也是类型化的**。
- **运行时**：调度器按 `matches` 过滤 + priority 排序 + fallback 链（返回 null 交给下一个），内置渲染器（含 GenUI 的 display_html 渲染）永远是最后兜底。

### 3.6 类型扩展机制：声明合并（无魔法，显式发布）

后端插件需要给前端插件提供自定义 RPC 方法 / 事件 / 工具结果类型时，**发布类型声明包**，前端插件通过 `declare module` 扩展：

```ts
// 后端插件 xbot.git 发布 @xbot/plugin-xbot-git/types
declare module '@xbot/plugin-api' {
  interface BackendRPC {
    'git.status': { params: { chatID: string }; result: { branch: string; dirty: boolean } }
  }
  interface EventMap {
    'git.branchChanged': { branch: string }
  }
  interface ToolResultMap {
    git_status: { branch: string; changes: readonly string[] }
  }
}
```

前端插件 `import '@xbot/plugin-xbot-git/types'` 后即可 `ctx.rpc.call('git.status', ...)`——**跨插件契约在编译期闭合**。这是声明合并的合法用法（类型空间扩展，无运行时魔法）。

### 3.7 插件间互操作：Exports API 与激活依赖

声明合并（§3.6）覆盖了"类型表"互操作（RPC 方法 / 事件 / 工具结果），但还缺两块：**插件暴露运行时 API 给别的插件直接调用**（函数级互操作）与**激活顺序**（依赖方必须先于被依赖方激活）。

#### 3.7.1 插件暴露公共 API（Exports API）

约定：**插件的公共 API = 模块的命名导出**（保留名 `manifest`/`activate`/`deactivate` 除外）。

```ts
// xbot.git 插件（被依赖方）
export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void { /* … */ }

export interface GitAPI {
  getStatus(chatID: string): Promise<{ branch: string; dirty: boolean }>
  checkout(branch: string): Promise<void>
}
export const api: GitAPI = { /* … */ }   // 命名导出 = 公共 API
```

消费方类型化获取：

```ts
// @xbot/plugin-api/src/plugins.ts
export interface PluginsAPI {
  /** 同步取已激活插件的公共 API；未激活/禁用/崩溃 → undefined */
  get<K extends keyof PluginExportsMap>(id: K): PluginExportsMap[K] | undefined
  /** 异步确保依赖插件激活后返回其 API（可选依赖的懒加载入口） */
  require<K extends keyof PluginExportsMap>(id: K): Promise<PluginExportsMap[K]>
  /** 订阅依赖插件的激活/停用（依赖动态上下线时降级/恢复） */
  onActivated<K extends keyof PluginExportsMap>(id: K, h: (api: PluginExportsMap[K]) => void): Disposable
  onDeactivated<K extends keyof PluginExportsMap>(id: K, h: () => void): Disposable
}
/** 空表；各插件发布类型包扩展（见下） */
export interface PluginExportsMap {}
```

`xbot.git` 发布类型包，前端插件 `import '@xbot/plugin-xbot-git/types'` 后 `ctx.plugins.get('xbot.git')` 返回精确类型：

```ts
declare module '@xbot/plugin-api' {
  interface PluginExportsMap {
    'xbot.git': import('@xbot/plugin-xbot-git').GitAPI
  }
}
```

**为什么用 `ctx.plugins.get` 而不是直接 `import` 插件模块**：直接 import 绕过生命周期管理（可能拿到未激活/已禁用/版本不符的实例）。运行时经 registry 只返回"已激活且通过校验"的实例。

#### 3.7.2 激活依赖（activationDependencies）

```ts
export const manifest = {
  id: 'xbot.panel',
  activationDependencies: ['xbot.git'],   // 强依赖：先激活 xbot.git，再激活本插件
  contributes: [ /* … */ ],
} satisfies PluginManifest
```

- **前端 PluginRuntime** 用拓扑排序（Kahn，与现有 Go `plugin/dependency.go` 的 `DependencyResolver` 同构语义）解析激活顺序：强依赖先行；**循环 → 报错并跳过相关插件**；**缺失 → 本插件不激活并明示缺失依赖**。
- **强依赖 vs 可选依赖**：`activationDependencies` = 强依赖（阻塞激活，拓扑序保证 activate 时依赖已就绪）；可选依赖用 `ctx.plugins.require`（运行时按需确保激活）。
- **运行时不产生死锁**：activate 只读"已激活"插件的 exports（拓扑序保证）；循环依赖在声明期被拓扑排序拒绝，激活期不可能出现 A 等 B、B 等 A。

#### 3.7.3 互操作矩阵（完整）

| 方向 | 机制 | 类型保障 | 备注 |
|---|---|---|---|
| 前端插件 → 前端插件 | `ctx.plugins.get/require`（同进程函数调用） | `PluginExportsMap` 声明合并 | 零序列化，可直传函数/对象 |
| 前端插件 → 后端插件 | `ctx.rpc.call('plugin-id.method', …)`（跨进程） | `BackendRPC` 声明合并 | 方法表 + 权限可审计 |
| 后端插件 → 前端插件 | `web_plugin_event` 自定义事件（push） | `EventMap` 声明合并 | 单向推送 |
| 后端插件 → 后端插件 | 现有 `Dependencies` + tools/hooks/services | Go 类型 | 现有模型，不变 |

**为什么同进程用 Exports 而非 RPC**：前端插件间不跨进程，函数直调零开销、可传高阶值；RPC 保留给跨进程边界（前端↔后端），方法表 + 权限提供可审计性。

#### 3.7.4 优雅降级

依赖插件不可用（禁用 / 崩溃 / 版本不满足）时：
- `get` 返回 `undefined` → 调用方 feature-detect 降级（不崩、不白屏）。
- `require` reject → catch 后降级。
- 强依赖缺失 → 本插件不激活，插件页明示"缺少依赖 xbot.git"。
- `onDeactivated` 通知 → 运行时卸载依赖后，消费方收到事件可即时降级（而非下次调用才报错）。

---

### 3.8 声明式组件（L1）的 props 也类型化

```ts
// @xbot/plugin-api/src/components.ts
interface ComponentProps {
  badge:    { text: string; color?: 'green'|'red'|'blue'|'amber'|'gray'|'indigo'; dot?: boolean }
  progress: { value: number; label?: string; color?: string }
  metric:   { label: string; value: string | number; delta?: number; trend?: 'up'|'down'|'flat' }
  sparkline:{ data: number[]; color?: string }
  table:    { columns: { key: string; label: string }[]; data: Record<string, unknown>[]; maxHeight?: number }
}

export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]
```

`{ type: 'badge', props: { text: 'x' } }`——props 类型随 type 精确收窄，错误 props（如给 badge 传 `data`）编译不过。

---

## 4. 多级能力模型（简化：无沙箱后只剩两档）

去掉沙箱后，能力分级坍缩为**信任级别的两档**（不再需要 L2 iframe 作为插件渲染路径）：

| 档位 | 机制 | 适用 |
|---|---|---|
| **声明式（L1）** | `ComponentDecl` 类型化组件，数据来自后端/事件 | 状态徽章、进度、指标 |
| **宿主模块（L3）** | 插件 ESM 直接运行在宿主，类型化 `ctx` | **主力**：视图、命令、渲染器、事件 |

> GenUI 的 iframe 保留为**动态代码渲染隔离**（LLM 生成代码的卫生措施），但它与插件系统的信任模型无关，不进入能力分级。

---

## 5. 协议（简化：只传清单和事件，不传代码）

前端插件模块如何到达浏览器？**随构建产物一起发布**（不是运行时传输源码）：

| 机制 | 说明 |
|---|---|
| 插件构建 | 插件作者 `build` 产出编译后的 ESM chunk（`dist/*.js`），随插件包安装到 `~/.xbot/plugins/<id>/web/` |
| 静态托管 | xbot server 把 `plugins/<id>/web/` 作为静态资源暴露（`/plugins/<id>/web/...`），`Cache-Control: immutable`（内容寻址文件名） |
| 清单下发 | `web_plugin_init` 只传：插件 ID、版本、贡献点声明、模块 URL 列表、权限——**不传代码** |

消息族（复用现有 WS/SSE）：

| 消息 | 方向 | 用途 |
|---|---|---|
| `web_plugin_init` | 后端→前端 | 激活：贡献点 + 模块 URL + 权限 |
| `web_plugin_deactivate` | 后端→前端 | 卸载：前端执行 disposables、移除贡献点 |
| `web_plugin_event` | 后端→前端 | 后端事件（agent 生命周期、自定义事件） |
| `web_plugin_action` | 前端→后端 | 用户交互（现有 `web_ui_action` 加 `plugin_id`） |
| `web_plugin_rpc` | 前端→后端 | JSON-RPC 2.0（前端插件 → 后端插件/核心） |
| `web_plugin_push` | 后端→前端 | 后端插件主动推数据 |

**热加载/卸载（hot reload & unload）**：
- **卸载**：后端发 `web_plugin_deactivate {plugin_id}` → 前端按逆序执行该插件所有 disposables → 移除其全部贡献点 → 从 registry 摘除。卸载是**同步幂等**的（重复 deactivate 是 no-op）。
- **热加载**：`web_plugin_init` 可重复发送（替换语义）。前端收到同一插件 ID 的 init 时：先按卸载流程清理旧实例 → 用**版本化 URL**（`?v=<content-hash>`）重新 `import` 模块 → activate 新实例。旧模块实例通过 import map 缓存淘汰（新 URL 强制重新抓取）。
- **触发源**：后端 `WatchConfig`/文件监听检测到插件 manifest 或 web 产物变化 → 主动重发 `web_plugin_init`；用户经插件管理面板手动"重载/禁用/启用" → 发 init/deactivate。
- **依赖联动**：卸载/重载一个有被依赖者的插件时，先按拓扑序（逆序）重载依赖它的插件，再重载本插件——消费方 `onDeactivated`/`onActivated` 事件即时降级/恢复。

**单一启动门控（取消双重校验）**：
- **权威校验 = 前端运行时校验（registry.ts）**：插件模块加载 + activate 前，`registry` 校验其 manifest（贡献点形状、权限与能力的对应、ID 唯一性、依赖是否已激活）。校验失败 → 该插件不激活，UI 明示原因。
- **取消后端 JSON Schema 启动门控**：后端不再在清单下发时做贡献点 Schema 校验（避免两处规则漂移）。后端只做**传输层**检查（插件存在性、权限声明是否允许 `ui.*`），不做贡献点语义校验——贡献点合法性完全由编译期类型 + 前端运行时校验保证（单一权威，无双重门控）。

热更新：`web_plugin_init` 可重复（替换语义，与 `channel_tools`/`web_ui` hot-update 一致）；重载 = 先 deactivate 旧实例（逆序 disposables）再 activate 新实例。

### 5.1 模块格式：ESM + import maps（依赖共享与单例保证）

插件模块格式选 **原生 ESM**，配套 **import maps** 解决插件系统最经典的"双 React 实例"问题：

```html
<!-- 宿主页面：把 bare specifier 解析到宿主提供的单例模块 -->
<script type="importmap">
{
  "imports": {
    "react":            "/vendor/react.js",
    "react-dom":        "/vendor/react-dom.js",
    "@xbot/ui":         "/vendor/xbot-ui.js",
    "@xbot/plugin-api": "/vendor/plugin-api.js"
  }
}
</script>
```

插件代码真正 `import`（而不是从 `window` 摸全局）：

```ts
import { useState } from 'react'
import { XBOT_UI } from '@xbot/ui'
import type { PluginContext } from '@xbot/plugin-api'
```

**关键性质**：
- **单例保证**：所有插件 + 宿主的 `react`/`@xbot/ui` 都解析到同一份模块——零重复 bundle、无双虚拟 DOM。
- **版本策略**：宿主版本为准。插件声明 `dependencies: { "react": ">=18" }`，构建时把 `react`/`@xbot/ui`/`@xbot/plugin-api` 全部 **external**（不打进产物），运行时由 import map 统一解析。版本冲突由 import map 强制统一（插件不能用独立 React 版本——这正是想要的）。
- **构建规范**：插件用 Vite library mode + `external: ['react', 'react-dom', '@xbot/ui', '@xbot/plugin-api']` 产出 ESM chunk。模板脚手架强制此配置，防止误打包。
- **按需加载**：`loader.ts` 动态 `import(decl.entryUrl)`——异步激活天然支持加载态（骨架屏）与失败态（ErrorBoundary 塌陷 + 贡献点回滚，与崩溃隔离同机制）。
- **升级路径**：现在的 GenUI 是无模块系统的全局注入（sucrase 编译 + `window.XBOT_UI`）——v2 把注入式升级为 import map 式，插件获得真 import / 类型检查 / 依赖单例。

### 5.2 插件管理面板（自举实现，dogfooding）

Web 端需要一个管理插件的面板（查看/启用/禁用/卸载/重载、依赖状态、权限清单、崩溃原因）。**这个面板本身就是一个插件**——内置插件 `xbot.plugin-manager`：

```
xbot.plugin-manager（内置，随前端分发）
├── manifest：contributes.view → container: "right_sidebar" / "panel"
├── 视图：插件列表（状态徽章、依赖链、权限、重载/禁用/卸载按钮、崩溃原因）
└── 数据：通过 ctx.plugins 能力 + ctx.rpc.call('plugin.list'/'plugin.setEnabled'/…)
          —— 它消费的正是 §3.7 的互操作 API，与任何第三方插件无差别
```

**为什么自举**：
1. **验证系统本身**——管理面板是"第一个消费者"，它调用的 `ctx.plugins.get`/`ctx.rpc`/贡献点渲染全链路必须是真类型、真运行时。面板有 bug 就是系统有 bug（dogfooding 纪律）。
2. **避免特权**——面板不直接访问宿主内部（没有 `window` 直达、没有注册表后门），只走公开的 `PluginsAPI`/`BackendRPC`。它证明了"插件能管理插件"这个能力模型是完整的。
3. **可替换**——用户/第三方可写一个更好的管理面板插件覆盖内置的（`contributes.view` 同容器多声明按优先级排序）。

**管理面板需要的新 RPC 方法**（`BackendRPC` 扩展）：

```ts
interface BackendRPC {
  'plugin.list':        { params: {}; result: PluginInfo[] }   // 含状态/依赖/权限/崩溃原因
  'plugin.setEnabled':  { params: { id: string; enabled: boolean }; result: {} }
  'plugin.uninstall':   { params: { id: string }; result: {} }
  'plugin.reload':      { params: { id: string }; result: {} } // 触发热加载（重发 web_plugin_init）
}
```

**与现有 `PluginManager` 的关系**：后端 `plugin/list` 等 RPC 直接读现有 `PluginManager`（`plugin/manager.go`）的状态 + 热加载触发 `WatchConfig` 式重载；前端只负责渲染与发起调用。管理面板本身不持有任何后端特权——它只是插件系统能力的一个高保真演示。

### 5.3 技能管理面板（xbot.skill-manager，第二个内置插件）

技能管理（查看/启用/禁用/导出/卸载/安装）同样做成内置前端插件 `xbot.skill-manager`（`web/src/plugins/xbot-skill-manager/`），与 plugin-manager 同范式，但 API 形态不同——**无点号核心 RPC 直传**：

- **4 个 skill RPC**（`skill_list` / `skill_set_enabled` / `skill_get_content` / `skill_validate_path`，后端 `serverapp/rpc_table.go`）经 `runtime.rpc.call('skill_list')` **无点号方法直接发 `/api/rpc`，完全绕过 `web_plugin_rpc`**。`/api/rpc` 的 `handleRPC`（web_rest.go）经 `rpcIdentityFromRequest` 从 cookie 注入 `RPCIdentity{SenderID, CanonicalUserID, CanonicalRole}` → skill RPC 用 `rpcAuthID(ctx)` 取身份（比信任前端 `sender_id` 参数更安全）。**条件**：这些方法必须在 `nonAdminRESTRPCMethods` 白名单（web_rest.go）——`authorizeRESTRPC` 对 admin 全放行，非 admin 只放行白名单方法。
- **install/uninstall 不走核心 RPC 直传**：`app_uninstall` RPC 用显式 `p.SenderID`（非 ctx 身份），前端无法安全直传 → 复用 master 通用市场 REST `/api/app/install-file` + `/api/app/uninstall`（后端从会话身份注入 sender_id）。
- **export 保留薄 REST** `/api/skills/export`（zip 二进制走 RPC 需 base64，REST 下载语义更自然；handler 先 `skill_validate_path` 防任意目录访问，文件名 `sanitizeExportName` 消毒）。embedded 分支用 `tools.ListEmbeddedSkillFiles`/`ReadEmbeddedSkillFile`；磁盘分支 `filepath.Walk`。
- **查看 SKILL.md = 面板内渲染（不开 file tab）**：`handleView` 对全部 skill（embedded 与磁盘路径一视同仁）调 `skill_get_content` RPC → `setViewing({ name, content })` → 面板内 `MarkdownPreview` 展示 + 返回按钮（`skills.back`）。**原因**：master 的 file tab（`panelToTab`，useTabManager.ts）只透传 `filePath`、丢弃 `content`，`FilePanel` 经 `useFileContent` 走 REST 重读磁盘——embedded skill 路径磁盘不存在，开 file tab 必然失败；且 `TabData` 无 `readOnly` 字段（TS2353）。曾为"跨组件开 tab"引入的 `TabManagerProvider`（`App.tsx` 包 `<AppShell />`）随方案一并**回退删除**（唯一消费者即 skill-manager 开 file tab，删除后无消费者）——`useTabManager()` 恢复为直接 `return useTabManagerImpl()`（AppShell/MobileAppShell 各自持有本地实例，绑定各自 Dockview API，互不影响）。
- i18n：`sidebar.skills` + `skills.*` 键（en/zh-CN）。
- **启禁用开关：所有 skill（含 embedded）都必须渲染开关，且必须用项目 radix `Switch` 组件**。① embedded 曾因 `skill.source !== 'embedded'` 条件被排除——但后端 `SetSkillEnabled` 是 `disabled_skills` **黑名单机制，与 source 无关**，embedded 一样可禁用（`isDisabled` 按 name 查黑名单），前端排除是缺陷（用户报告"内嵌 skill 无开关"）。② 曾用裸 `<button role="switch">` 自拼轨道+圆点——容器 `justify-between` 里 path `<span>` 是文本、button 内 `<span>` 是 `absolute` 不占布局空间，button 缺 `shrink-0` 时 min-width 解析为 0，path 较长时开关被 flex 压缩变形（用户报告"开关渲染有问题"）。修复：统一 `Switch`（radix 封装内置 `shrink-0` + 标准样式），`checked={skill.enabled}` + `onCheckedChange`；path 行 `min-w-0 truncate`。守护：`SkillManagerPanel.test.tsx`（embedded 渲染开关 / aria-checked 反映状态 / 点击调 `skill_set_enabled`）。
- **⚠️ 测试 mock `usePluginRuntime` 必须返回【稳定引用】**（`vi.hoisted` 定义一次），不能每次渲染返回新对象字面量——`load` 的 `useCallback` 依赖 `[runtime]`，新引用 → load 每次新函数 → `useEffect` 无限重跑（一直 loading，`findByRole` 超时）。

注册路径与 plugin-manager 完全相同：`usePluginRuntimeHost.ts` 静态 import `SkillManagerPanel` → `builtinViews.set('xbot.skill-manager.panel', ...)`（⚠️ 内置视图必须静态 import，禁止动态 import——React #311 黑屏根因）+ Bootstrap `activateBuiltin(skillManager.manifest, ...)`。**⚠️ 内置视图有【两处】注册点，必须同步**：① `usePluginRuntimeHost.ts` 的 `builtinViews` map（供 `loadBuiltinView` 解析）；② `PluginView.tsx` 的 `BuiltinView` switch（渲染分发，`case view.id` → 组件）。**只改 ① 漏改 ② 的症状：tab 存在（view 注册成功）但点击空白 + 无请求 + 无报错**——switch 命中 `default: return null`，面板组件从未 mount（真实事故：skill-manager 加 ① 时漏 ②，右侧栏「技能」tab 空白）。守护：`PluginView.test.tsx` 断言每个 builtin view 渲染出内容。

### 5.4 插件 editor-view API（VSCode webviewPanel 语义）

插件可以**控制主编辑区 tab**（VSCode `window.createWebviewPanel` 模型）——侧边栏/面板视图做入口列表，点击后在主编辑区打开全宽、参数化的动态 tab（如 git diff / commit 详情）。

**API（UIAPI 扩展，`ctx.ui.openViewTab` / `ctx.ui.openFileTab` / `ctx.ui.openDiffTab`）**：

```ts
ctx.ui.openViewTab({
  viewId: 'xbot.git-fancy.diff',        // 必须是已声明的 view 贡献点 id
  title: 'src/a.go',                    // tab 标题
  icon: 'file-diff',                    // 可选 Lucide 图标名
  key: 'git-diff:worktree:src/a.go',    // 去重逻辑键（缺省按 viewId）
  params: { path: 'src/a.go' },         // 作为 props 传给 view 组件
})

// 打开文件编辑器并拿到控制句柄（VSCode showTextDocument 语义）
const doc = ctx.ui.openFileTab('/repo/src/main.go', {
  line: 42,                                    // 打开后跳到 42 行（居中）
  highlight: { startLine: 40, endLine: 44 },   // 高亮行范围
  language: 'go',                              // 覆盖语法高亮
  viewMode: 'editor',                          // 覆盖初始视图（markdown 可 preview）
})
doc.revealLine(100)                            // 跳行
doc.highlightLines(40, 44)                     // 行高亮（accent 淡染 + 左边条）
doc.getSelection && doc.getContent()           // 读内容（编辑不落盘）
doc.setLanguage('typescript')                  // 动态换语言
doc.onClose(() => console.log('closed'))       // tab 关闭通知
// handle 方法在 tab 关闭后自动 no-op（返回 false）——插件无需关心生命周期

// diff 编辑器句柄
const d = ctx.ui.openDiffTab({ title: 'a.go', original, modified, path: 'src/a.go' })
d.nextDiff(); d.prevDiff()                    // 差异导航
d.setRenderSideBySide(false)                  // 并排 → 行内
```

**编辑器控制链路**（`plugin-runtime/editorRegistry.ts`）：

- **editorId 确定性派生**（`ed-file:<path|key>` / `ed-diff:<diffKey>`）：同一文件/diff 的 id 恒定——重复 open、刷新后布局恢复的 tab（params 携带 id）与 handle 天然对上，无需会话级映射。
- **panel attach**：FilePanel/DiffPanel 挂载时 `attachEditor(editorId, controller)`（controller 是 Monaco 实例的受控子集：reveal/decorations/setModel language 等）；卸载时 detach + 广播 onClose。**FilePanel 顶层只类型导入 monaco**（`import type * as monacoNs`），运行时命名空间从 `MonacoEditor.onEditorMount(editor, monaco)` 回调取得——避免把完整 monaco bundle 拉进测试环境。
- **handle 工厂**（createEditorHandle/createDiffHandle）：方法执行时实时查注册表；实例不在则返回 false（no-op）。新实例覆盖旧实例时旧 detach 不误删（controller 引用比对）。
- **PanelParams 透传**：`editorId/initialLine/initialHighlight/fileLanguage/fileViewMode`（file）+ `editorId`（diff）经 useTabManager 的 openTab/panelToTab 全链路透传；手机端 MobileAppShell 拦截 openTab 时同步透传到 mobileWorkView。

**机制链路**：

- **`dynamic: true` view 声明**（ViewContribution）：参数化动态视图不进 activity bar / 侧栏 tab / layoutRegistry（`usePluginViewPanels` 与 `syncViews` 都过滤），只能经 `openViewTab` 打开。plugin.json 示例：`{"kind":"view","id":"xbot.git-fancy.diff","container":"main","entry":"diff.js","dynamic":true}`。
- **tab 去重**：`PanelParams.viewKey` 优先于 viewId（`tabLogicalKey` → `plugin-view:${key}`）——同一 view 可开**多个** tab 实例（每个文件/commit 一个），同 key 聚焦已有 tab。
- **参数透传**：`PanelParams.viewParams` → `ReactContentRenderer.renderPluginView` → `PluginView` → 插件 view 组件 props（`<state.comp {...viewParams} />`）。**插件 view 组件从 props 拿参数**（如 `{ path, commit }`）。
- **component=viewId**：plugin tab 的 dockview component 名必须用 viewId（`openTab` 里 `component: input.data.viewId ?? 'plugin'`）——`renderPluginView` 按 `view.id === component` 查找，传泛型 `'plugin'` 永远查不到（渲染空白的历史 bug）。
- **模块级桥**（`plugin-runtime/editorTabs.ts`）：PluginUI 在 React 树外，tabManager 在 AppShell 内——`registerEditorTabOpener(fn)` 桥接（AppShell useEffect 注册，卸载清 null）。

**多入口构建（esbuild splitting）**：一个插件的多个 view 各自 entry（index.js/diff.js/commit.js），经 `esbuild --bundle --splitting --format=esm` 产出共享 chunk——`activate(ctx)` 在主入口注入的 rpc/ui 单例（shared 模块）在三个入口间共享（ESM 模块缓存按 URL，chunk 相对路径无 query → 同一实例）。**单入口 bundle 会让每个 view 拿到独立副本，activate 注入对其他 view 不可见**。

**参考实现**：`xbot.git-fancy`（`plugins/xbot-git-fancy/main.go` + `web/src/plugins/git-fancy/`）——侧边栏面板（变更文件 + commit 分页"加载更多"）→ 点击开全宽 diff tab / commit 详情 tab（文件列表 → 再点击开该 commit 内的单文件 diff）。

---

## 6. 与现有系统的关系

| 现有机制 | v2 处理 |
|---|---|
| `WebUIRegistry` 8 slot | 迁移为 `contributes.views`（container 映射到现有布局位），向后兼容（旧声明自动映射） |
| `display_html` 硬编码特判（engine_wire + 前端 4 处） | 迁移为 `messageRenderers` 声明 + `ToolResultMap.display_html`，删硬编码 |
| `web_ui_action` | 保留，扩展 `plugin_id` 字段 |
| 后端插件（Go/stdio/script） | **不变**——继续提供工具/hooks/事件；新增"发布类型声明包 + web 模块"能力 |
| 纯前端插件（无后端进程） | 新增：只需 manifest + web 模块，后端只做清单托管与事件桥 |

**关键边界（沿用 xbot 架构纪律）**：
- 前端插件**只能**通过 `web_plugin_rpc` 调后端（RPC 层隔离，复用 `rpcUserID` 身份解析）；不直接挂 agent 内部 API。
- 事件桥只转发**已经过 sanitize 的事件载荷**（`SafeMessage` 等），插件接触不到内部状态。
- 后端插件仍是现有进程模型——**无 VM、无新运行时**。

---

## 6.1 通用顶栏/状态栏插件容器（status_bar_right）

**架构纪律：主项目只提供通用扩展点（容器 + 数据桥），插件的专属 UI 与逻辑全部在插件里。**
任何把单个插件的 UI 组件写进主项目的做法都是违反插件解耦（历史教训：iter-stats 徽章曾被写进
主项目 `IterStatsBadge.tsx` + `MobileAppShell` 硬编码 → 回归修正是全部移至插件）。

**容器机制**：
- `ViewContainer` 已有 `status_bar_right` / `info_bar` / `bottom` 三种**顶栏/状态栏插件容器**
- 宿主在状态栏挂 `<PluginPanelContainer container="status_bar_right" />`（通用渲染点，任何插件声明到该容器自动出现）：
  - 移动端：`MobileAppShell` 顶栏 header（顶部 status bar）
  - 桌面端：`AppShell` 的 `InfoBar`（底部 VSCode 状态栏风格，`desktop.info_bar` 布局位）
- `VIEW_CONTAINER_TO_SLOT` 把 `status_bar_right` → `desktop.info_bar` 布局位（插件 view 自动注册为可移动布局项）

**数据桥（通用，非插件专属）**：
- 宿主 `iteration-render.tsx` 暴露 `window.__xbot_iteration__`（`setGlobalLiveStats`/`useGlobalLiveStats`/`subscribeGlobalLiveStats`/`getGlobalLiveStats`）
- `LiveIteration` 每次流式指标变化 `setGlobalLiveStats`（tok/s、ttft、completionTokens）——发布的是**通用实时流指标**，任何状态栏插件可订阅，不限于 iter-stats
- 插件 ESM 经 `window.__xbot_iteration__.useGlobalLiveStats` 订阅（不能 import 宿主内部模块）

**iter-stats 插件化落地（模式）**：
1. 后端 `plugin.json`：`web: { entry: "index.js", contributes: [{kind:'view', id, container:'status_bar_right', title, entry:'index.js'}] }` —— view 声明必须放**后端 `web.contributes`**（静态声明），后端 `web_plugin_list` 只返回 `Web.Entry != ""` 的插件
2. 前端 `entry.tsx`：`export default` 徽章组件（`loadPluginViewComponent` 按 `view.entry` 加载 `mod.default`）
3. esbuild 打包：`npx esbuild src/plugins/iteration-stats/entry.tsx --bundle --format=esm --jsx=transform --external:react --outfile=~/.xbot/plugins/xbot.iteration-stats/web/index.js`（React external，运行时从 `window.React` 取）
4. 改 `plugin.json` 后需 `config reload_plugins`（后端 Discover+ActivateAll）让 `web_plugin_list` 返回新声明

**关键陷阱**：
- 前端 ESM 只能经 `window` 桥拿宿主数据；绝不放 `useIterationStats`（迭代处 Context 只在 LiveIteration 内有效）到顶栏。顶栏用 `useGlobalLiveStats`（全局桥）。
- `pluginStats.ts`（builtin manifest 残留，`builtin:` entry）与独立插件路径冲突——iter-stats 改独立插件后必须删，否则 `builtinViews` map 找不到组件。

---

## 7. 实现路径

### Phase 1：类型包先行（1 周）
- 建立 `web/types/plugin-api/`（或独立 npm 包 `@xbot/plugin-api`）：`manifest.ts` / `context.ts` / `events.ts` / `rpc.ts` / `renderer.ts` / `components.ts` 全部类型 + 单测（类型断言测试：`@ts-expect-error` 验证"未声明权限不可用"等）。
- 这是地基：**所有约束先以类型形式固定下来**。

### Phase 2：运行时骨架 + 协议（2 周）
- 前端 `PluginRuntime`（loader/registry/events/rpc/slots）+ ErrorBoundary 崩溃隔离 + **热加载/卸载**（版本化 URL 重 import、disposables 逆序清理、依赖联动重载）。
- 后端 `web_plugin_*` 消息、清单下发（**单一门控：不做贡献点 Schema 校验**，只做传输层检查）、事件桥、静态托管。
- 迁移 `WebUIRegistry` 8 slot → `contributes.views`（向后兼容）。

### Phase 3：管理面板 + 渲染器（2 周）
- 内置插件 `xbot.plugin-manager`（自举）：插件列表/启用/禁用/卸载/重载视图，走公开 `PluginsAPI`/`BackendRPC`。
- `MessageRenderer` 调度器 + fallback 链；`ToolResultMap` 内置表。
- GenUI 从 `display_html` 硬编码迁移为渲染器声明，前端删 4 处特判。

### Phase 4：生态（1 周）
- 插件模板（含 `satisfies` 类型化 manifest 脚手架）、示例仓库、文档、`plugin.json` 的 `web` 字段支持。

---

## 8. 验证标准（Done 定义）

1. **类型即契约**：`@xbot/plugin-api` 的编译期断言测试全绿——(a) 未声明权限的能力访问是 `@ts-expect-error`；(b) 错误 RPC 方法名/参数是 `@ts-expect-error`；(c) 渲染器 `matches: {tool:'shell'}` 访问 `display_html` 字段是 `@ts-expect-error`。
2. 示例插件（L3）实现：注册命令（含快捷键）+ 订阅 `message.committed` + 渲染 Dockview 面板 + 调 `agent.send` RPC——全部类型安全、运行时生效。
3. 示例插件（L1）用 `ComponentDecl` 渲染指标面板——props 类型精确收窄。
4. GenUI 渲染路径完全迁移到 `messageRenderers`，前端零 `display_html` 硬编码。
5. 现有 8 slot widget 在贡献点模型下行为不变（向后兼容）。
6. 插件运行时异常：ErrorBoundary 只塌陷该插件贡献点，主流程不受影响。
7. 后端插件发布类型声明包，前端插件 `declare module` 扩展后，跨插件 RPC/事件/工具结果类型在编译期闭合。
8. **插件互操作**：示例插件 B 经 `activationDependencies: ['xbot.git']` 等待 A 激活，`ctx.plugins.get('xbot.git')` 拿到类型化 `GitAPI` 并调用成功；删除 A 后 B 降级（`get` 返回 undefined，UI 不崩）；循环依赖声明的插件组在激活期被拒绝并报错。
9. **热加载/卸载**：插件 A 激活后，重新下发 `web_plugin_init`（同 ID、新版本 URL）→ 旧实例 disposables 全部执行、贡献点移除、新模块生效；`web_plugin_deactivate` → 幂等卸载、UI 无残留。依赖 A 的 B 在 A 重载期间收到 `onDeactivated`/`onActivated` 且不崩。
10. **插件管理面板（自举）**：内置 `xbot.plugin-manager` 面板列出全部插件（状态/依赖/权限），可启用/禁用/卸载/重载插件并即时反映（热加载生效）；面板自身只用公开 API（无宿主内部访问），替换它不破坏系统。
