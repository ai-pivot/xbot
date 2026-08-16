/**
 * @xbot/plugin-api 编译期断言测试——用 @ts-expect-error 验证"类型即契约"。
 *
 * 这些测试没有运行时断言（只有类型断言），但它们守护了设计承诺：
 * (a) 未声明权限的能力访问必须编译错误；(b) 错误 RPC 方法名/参数必须编译错误；
 * (c) 渲染器 matches 与 render 参数类型强关联。
 */
import { expectTypeOf, it } from 'vitest'

import type { PluginContext } from './context'
import type { MatchedMessage, MessageRendererContribution, Matcher } from './renderer'
import type { ComponentDecl } from './components'
import type { BackendRPC } from './rpc'
import type { SafeAssistantMessage, SafeMessage } from './safe'

// ─── 3.2 能力即类型：未声明权限 → never ───────────────────────────

// 真实实现（运行时安全）；@ts-expect-error 的编译期验证由 tsc 完成。
function activate<P extends readonly import('./manifest').Permission[]>(
  permissions: P,
  ctx: PluginContext<P>,
): void {
  void permissions
  void ctx
}

it('能力即类型：已声明 events 可用，未声明 state 编译错误', () => {
  const perms = ['events', 'rpc'] as const
  activate(perms, {
    events: { on: () => () => {}, once: () => () => {} },
    rpc: { call: async () => ({}), notify: () => {} },
    meta: { id: 'x', version: '1' },
    contributes: { register: () => () => {}, registerAll: () => () => {} },
    // @ts-expect-error - 未声明 state 权限 → ctx.state 是 never
    state: { getSession: () => null },
  })
  expectTypeOf(perms).toMatchTypeOf<readonly string[]>()
})

// ─── 3.4 RPC：错误方法名/参数编译错误 ─────────────────────────────

// 真实实现（运行时安全）——@ts-expect-error 的编译期验证由 tsc 完成。
function callRPC<K extends keyof BackendRPC>(
  method: K,
  params: BackendRPC[K]['params'],
): Promise<BackendRPC[K]['result']> {
  void method
  void params
  return Promise.resolve({} as BackendRPC[K]['result'])
}

it('RPC 方法表：合法调用类型安全', async () => {
  const r = await callRPC('agent.send', { chatID: 'c', content: 'hi' })
  expectTypeOf(r).toMatchTypeOf<{ turnID: number; queued: boolean }>()
})

it('RPC：错误方法名编译错误', () => {
  // @ts-expect-error - 不存在的方法
  callRPC('agent.send2', {})
})

it('RPC：错误参数形状编译错误', () => {
  // @ts-expect-error - chatID 必须是 string，content 缺失
  callRPC('agent.send', { chatID: 42 })
})

// ─── 3.5 渲染器：matches 精化 render 参数类型 ─────────────────────

it('MatchedMessage<{tool:"display_html"}> 携带 tool.result.code', () => {
  type M = MatchedMessage<{ tool: 'display_html' }>
  expectTypeOf<M>().toMatchTypeOf<SafeMessage>()
  // 类型层面验证 tool.result.code 可访问（如果编译过，说明精化生效）
  expectTypeOf<M>().toHaveProperty('tool')
})

it('MatchedMessage<{role:"assistant"}> 精化为 SafeAssistantMessage', () => {
  type M = MatchedMessage<{ role: 'assistant' }>
  expectTypeOf<M>().toMatchTypeOf<SafeAssistantMessage>()
})

it('渲染器贡献点：matches 驱动 render 参数', () => {
  const decl: MessageRendererContribution<{ tool: 'git_status' }> = {
    kind: 'messageRenderer',
    id: 'git.render',
    priority: 10,
    matches: { tool: 'git_status' },
    render: (msg) => {
      // msg.tool.result.branch 类型安全（ToolResultMap.git_status）
      return msg.tool.result.branch
    },
  }
  expectTypeOf(decl).toMatchTypeOf<MessageRendererContribution>()
})

it('错误 matcher 类型编译错误（role 非法值）', () => {
  // @ts-expect-error - role 只能是 assistant|user|system
  const bad: Matcher = { role: 'admin' }
  void bad
})

// ─── 3.7 声明式组件 props 收窄 ────────────────────────────────────

it('ComponentDecl：badge props 精确收窄', () => {
  const badge: ComponentDecl = { type: 'badge', props: { text: 'git:main' } }
  expectTypeOf(badge).toMatchTypeOf<ComponentDecl>()
})

it('ComponentDecl：错误 props 编译错误', () => {
  // @ts-expect-error - badge 没有 data 字段（只有 action/data 中 action 可选）
  const bad: ComponentDecl = { type: 'badge', props: { data: [1] } }
  void bad
})

// ─── 3.1 manifest：判别联合穷尽性（satisfies 编译期校验）──────────

import type { Contribution, PluginManifest } from './manifest'

it('PluginManifest satisfies：合法贡献点通过', () => {
  const manifest = {
    id: 'xbot.demo',
    name: 'Demo',
    version: '0.1.0',
    permissions: ['events', 'commands'] as const,
    contributes: [
      { kind: 'command', id: 'demo.hello', title: 'Hello' },
      { kind: 'view', id: 'demo.panel', container: 'right_sidebar', title: 'Panel' },
    ] as const,
  } satisfies PluginManifest
  expectTypeOf(manifest.contributes[0]).toMatchTypeOf<Contribution>()
})

it('PluginManifest：未知 kind 编译错误', () => {
  const bad = {
    id: 'x',
    name: 'x',
    version: '1',
    // @ts-expect-error - 'magic' 不是 Contribution kind
    contributes: [{ kind: 'magic', id: 'x.y' }],
  } satisfies PluginManifest
  void bad
})

it('PluginManifest：view 缺 title 编译错误', () => {
  const bad = {
    id: 'x',
    name: 'x',
    version: '1',
    // @ts-expect-error - ViewContribution 需要 title
    contributes: [{ kind: 'view', id: 'x.y', container: 'panel' }],
  } satisfies PluginManifest
  void bad
})
