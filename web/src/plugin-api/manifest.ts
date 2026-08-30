/**
 * 插件清单（manifest）——类型化贡献点声明。
 *
 * 每个贡献点是判别联合 `Contribution` 的成员；插件用 `satisfies PluginManifest`
 * 让编译器校验形状、保留字面量类型（供后续 `PluginContext<typeof manifest.permissions>` 推导）。
 */
import type { ComponentDecl } from './components'
import type { EventMap } from './events'
import type { MessageRendererContribution } from './renderer'

/** 能力权限：决定 `PluginContext<P>` 上哪些能力接口可用（§3.2 能力即类型）。 */
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config' | 'files'

/** 视图容器（映射到前端布局位）。 */
export type ViewContainer = 'right_sidebar' | 'panel' | 'bottom' | 'info_bar' | 'status_bar_right' | 'iteration' | 'main'

export interface ViewContribution {
  kind: 'view'
  /** 全局唯一：`<pluginId>.<viewId>`。 */
  id: string
  /** 渲染容器。 */
  container: ViewContainer
  title: string
  icon?: string
  /** ESM 模块路径（相对插件包根）。entry 导出的默认组件即视图。 */
  entry?: string
  /** L1 声明式视图：type + props（无需 entry）。 */
  component?: ComponentDecl
  /**
   * 容器内对齐（插件通用配置，引擎读取渲染）——引擎不针对具体插件硬编码。
   * - 'start'（默认）：靠左/靠上
   * - 'end'：靠右/靠下（如 iter-stats 徽章在顶栏右对齐）
   * status_bar_right 容器常配合 align:'end' 把内容推到右侧。
   */
  align?: 'start' | 'end'
  /**
   * 参数化动态视图（VSCode webviewPanel 语义）：不出现在 activity bar /
   * 侧栏 tab / 布局注册表，只能通过 ctx.ui.openViewTab({viewId, params})
   * 打开（如 git diff / commit 详情）。tab 内容由 openViewTab 的 params
   * 参数化，同一 view 可开多个 tab 实例（按 key 去重）。
   */
  dynamic?: boolean
}

export interface CommandContribution {
  kind: 'command'
  id: string
  title: string
  keybinding?: string
  /** 禁用/显示条件表达式（保留给未来 when 求值器）。 */
  when?: string
}

export interface ToolbarContribution {
  kind: 'toolbar'
  id: string
  title: string
  icon?: string
  /** 点击后执行的命令 id。 */
  command: string
}

export interface ContextMenuContribution {
  kind: 'contextMenu'
  id: string
  title: string
  /** 匹配消息/文件类型（保留给未来）。 */
  when?: string
  command: string
}

export interface SettingContribution {
  kind: 'setting'
  key: string
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect'
  label: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string }>
  /** 分组名：同一 section 的属性在设置面板归为一组。缺省归入插件标题组。 */
  section?: string
  /** 敏感值：UI 中以掩码输入框展示。 */
  secret?: boolean
  /** 文本输入框占位符提示。 */
  placeholder?: string
  /** 必填项提示。 */
  required?: boolean
}

/** 事件处理器贡献点：订阅 `EventMap` 中的事件。 */
export interface EventHandlerContribution<E extends keyof EventMap = keyof EventMap> {
  kind: 'eventHandler'
  event: E
  /** 处理逻辑模块路径。模块导出 `handler(payload: EventMap[E])`。 */
  entry: string
  /** 订阅所需权限（缺省继承插件 permissions）。 */
  permission?: string
}

/** 消息渲染器贡献点（§3.5：matches 精化 render 参数类型）。 */
export type { MessageRendererContribution }

export interface ThemeContribution {
  kind: 'theme'
  cssVars: Record<string, string>
}

export type Contribution =
  | ViewContribution
  | CommandContribution
  | MessageRendererContribution
  | ToolbarContribution
  | ContextMenuContribution
  | SettingContribution
  | EventHandlerContribution
  | ThemeContribution
  | import('./ambience').AmbienceContribution

/** 插件清单。`contributes` 必须是 Contribution 数组；`permissions` 是能力源头。 */
export interface PluginManifest {
  /** 全局唯一插件 id（如 `xbot.git-info`）。 */
  id: string
  name: string
  version: string
  description?: string
  /** 能力声明（§3.2）——决定 `ctx` 形状。 */
  permissions?: readonly Permission[]
  /** 强依赖：先于本插件激活的插件 id 列表（§3.7.2 拓扑排序）。 */
  activationDependencies?: readonly string[]
  /** 贡献点（类型化声明）。 */
  contributes: readonly Contribution[]
  /** 前端入口模块（ESM）。纯后端插件无此字段。 */
  entry?: string
}

/**
 * 运行时贡献点注册接口（activate 内可动态注册额外贡献点）。
 * 每个注册调用返回 disposable，卸载时自动清理。
 */
export interface ContributionAPI {
  register(contribution: Contribution): Disposable
  registerAll(contributions: readonly Contribution[]): Disposable
}

/** 可清理句柄：调用即释放，幂等。 */
export type Disposable = () => void

/** 插件激活/停用的生命周期元信息。 */
export interface PluginMeta {
  id: string
  version: string
}
