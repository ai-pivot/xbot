/**
 * 能力即类型（§3.2）——权限声明决定 ctx 形状。
 *
 * `PluginContext<P>` 是类型函数：对每个能力 `K`，仅当 `K ∈ P` 时该能力接口可用，
 * 否则类型为 `never`——插件作者访问未声明的能力在编译期即报错。
 */
import type { ContributionAPI, Disposable, Permission, PluginMeta } from './manifest'
import type { EventsAPI } from './events'
import type { RPCAPI } from './rpc'
import type { StateAPI } from './state'
import type { UIAPI } from './ui'
import type { PanelsAPI } from './panels'
import type { PluginsAPI } from './plugins'
import type { ConfigAPI } from './config'
import type { FilesAPI } from './files'

interface PermissionAPI {
  events: EventsAPI
  commands: CommandsAPI
  rpc: RPCAPI
  state: StateAPI
  ui: UIAPI
  panels: PanelsAPI
  plugins: PluginsAPI
  config: ConfigAPI
  files: FilesAPI
}

export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & {
  /** 运行时元信息（所有插件可用）。 */
  readonly meta: PluginMeta
  /** 动态注册贡献点（所有插件可用）。 */
  readonly contributes: ContributionAPI
}

/** 命令系统（§3.7 之前的事件/命令能力）。 */
export interface CommandsAPI {
  /** 注册命令处理器；返回 disposable 用于卸载。 */
  register(id: string, handler: (args: unknown) => void | Promise<void>): Disposable
  /** 执行一个已注册命令。 */
  execute(id: string, args?: unknown): Promise<void>
  /** 注册快捷键（keybinding 语法与贡献点一致）。 */
  registerKeybinding(keybinding: string, commandId: string): Disposable
}
