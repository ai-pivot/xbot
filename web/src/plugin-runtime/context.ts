/**
 * PluginContext 构建——按权限注入能力（§3.2 能力即类型）。
 *
 * 运行时侧：`buildContext(permissions)` 返回与 `PluginContext<P>` 形状一致的
 * 对象——只包含插件声明的权限对应的能力。未声明的能力在类型上是 never，
 * 运行时也不存在（纵深防御，但权威在编译期）。
 */
import type { Permission, PluginContext } from '@/plugin-api'
import type { CommandsAPI } from '@/plugin-api'
import type { EventsAPI } from '@/plugin-api'
import type { RPCAPI } from '@/plugin-api'
import type { StateAPI } from '@/plugin-api'
import type { UIAPI } from '@/plugin-api'
import type { PanelsAPI } from '@/plugin-api'
import type { PluginsAPI } from '@/plugin-api'
import type { ConfigAPI } from '@/plugin-api'
import type { ContributionAPI, Disposable, PluginMeta } from '@/plugin-api'
import type { Contribution } from '@/plugin-api'

export interface ContextServices {
  meta: PluginMeta
  events: EventsAPI
  commands: CommandsAPI
  rpc: RPCAPI
  state: StateAPI
  ui: UIAPI
  panels: PanelsAPI
  plugins: PluginsAPI
  config: ConfigAPI
  registerContribution: (c: Contribution) => Disposable
}

/**
 * 构建运行时 ctx。permissions 决定哪些能力被注入；
 * 未声明的能力在返回对象上为 undefined（编译期是 never）。
 */
export function buildContext(
  permissions: readonly Permission[],
  svc: ContextServices,
): PluginContext<readonly Permission[]> {
  const ctx: Record<string, unknown> = {
    meta: svc.meta,
    contributes: {
      register: svc.registerContribution,
      registerAll: (contributions: readonly Contribution[]) => {
        const disposables = contributions.map((c) => svc.registerContribution(c))
        return () => {
          for (const d of disposables.reverse()) d()
        }
      },
    } satisfies ContributionAPI,
  }
  const has = (p: Permission) => permissions.includes(p)
  if (has('events')) ctx.events = svc.events
  if (has('commands')) ctx.commands = svc.commands
  if (has('rpc')) ctx.rpc = svc.rpc
  if (has('state')) ctx.state = svc.state
  if (has('ui')) {
    ctx.ui = svc.ui
    ctx.panels = svc.panels
  }
  if (has('plugins')) ctx.plugins = svc.plugins
  if (has('config')) ctx.config = svc.config
  return ctx as PluginContext<readonly Permission[]>
}
