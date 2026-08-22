/**
 * @xbot/plugin-api — 类型即契约（Type-as-Contract）。
 *
 * 插件清单（manifest）、能力上下文（PluginContext）、事件表（EventMap）、
 * RPC 方法表（BackendRPC）、消息渲染器（MessageRenderer）、声明式组件
 * （ComponentDecl）全部在这里以类型固定。插件 `import type` 本包，
 * 编译期即获得全部契约；运行时由 PluginRuntime（@/plugin-runtime）消费。
 *
 * 命名原则（反 cordis）：disposable 就叫 disposable，event 就叫 event，
 * rpc 就叫 rpc。不借用 effect/fiber/epoch 等 PL 术语。
 */

export type * from './manifest'
export type * from './context'
export type * from './events'
export type * from './rpc'
export type * from './renderer'
export type * from './components'
export type * from './safe'
export type * from './progress'
export type * from './state'
export type * from './ui'
export type * from './plugins'
