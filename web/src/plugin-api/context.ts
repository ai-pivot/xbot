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
import type { ShareAPI } from './share'

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
  share: ShareAPI
}

export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & {
  /** 运行时元信息（所有插件可用）。 */
  readonly meta: PluginMeta
  /** 动态注册贡献点（所有插件可用）。 */
  readonly contributes: ContributionAPI
  /**
   * 插件自带文案的解析器（**所有插件可用，无需权限**）。
   *
   * 设计契约（2026-09-19 用户要求「插件 i18n 应该是插件通用功能」）：
   *   - **文案随插件清单走**（`web.i18n: { "<locale>": { "<key>": "<text>" } }`），
   *     绝不塞进宿主的 `i18n/*.ts` —— 那是跨插件的命名空间污染，也让插件无法独立分发；
   *   - 解析顺序：当前宿主 locale → `en` → 表里第一个可用 locale → `fallback` → key；
   *   - `locale` 是只读的当前语言快照（宿主切换语言后，插件重新读取即为新值）。
   */
  readonly i18n: I18nAPI
}

/** 插件自带的 i18n（见 PluginContext.i18n 的契约）。 */
export interface I18nAPI {
  /** 当前宿主语言（BCP-47，例如 "zh-CN"、"en"、"ja"）。 */
  readonly locale: string
  /** 取插件自己的文案；找不到时逐级回退，最终回退到 `fallback ?? key`（永不返回 undefined 语义）。 */
  t(key: string, fallback?: string): string
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
