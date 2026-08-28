/**
 * Panels 能力——「一切皆面板」（布局 v4）的插件侧契约。
 *
 * 内置面板（source='core'）与插件面板共用同一 PanelDefinition / 同一
 * PanelRegistry / 同一 PanelChrome 外壳。插件经 ctx.panels 注册自定义
 * 面板；view 贡献点由宿主自动转换为面板（同一 def 形状）。
 */
import type { ReactNode } from 'react'
import type { Disposable } from './manifest'

/** 面板停靠模式。docked = 左栏堆叠；floating = 窗口内自由浮层。 */
export type PanelMode = 'docked' | 'floating'

/** 标题栏徽标（如未读数/运行任务数）。 */
export interface PanelBadge {
  text: string
  /** CSS 颜色值（作为徽标底色 + 文字色）。 */
  color: string
}

/** render 回调的宿主环境（hooks 语义由 render 的实现者保证——在模块级组件里调用）。 */
export interface PanelRenderContext {
  /** 打开文件/任务 tab 等 dockview 操作（与面板组件现有 props 一致）。 */
  tabManager: import('@/hooks/useTabManager').TabManager
}

// ── 布局 v5/v5.1（面板停靠引擎）─────────────────────────────────────────────

/**
 * 面板停靠区（v5.1「Focus + Drawer」）：side=左栏钉选堆叠；chip=底部 chips
 * 启动器（临时使用不占侧栏）；top/bottom=徽章 rail；floating=自由浮层。
 */
export type PanelZone = 'side' | 'chip' | 'top' | 'bottom' | 'floating'

/** 徽章 rail 的三分段（top/bottom 专属）：left 靠左、center 居中、right 靠右。 */
export type RailSegment = 'left' | 'center' | 'right'

/**
 * v5.1 面板位置：side 按垂直序（order）+ 可选 h（body 高度 px，缺省自适应
 * max-h 320）；chip 为底部启动器（无高度语义）；top/bottom 按 segment + order；
 * floating 按 xywh（相对 FloatingLayer 容器）。
 */
export interface PanelLocation {
  zone: PanelZone
  segment?: RailSegment
  /** 同区/同段内的排序键（小者在前）。 */
  order: number
  /** floating 专属：相对 FloatingLayer 容器的 xywh。 */
  x?: number
  y?: number
  w?: number
  /**
   * 面板高度 px：floating = 浮层高度；side（钉选堆叠）= body 高度（拖拽调高
   * clamp 140–640，缺省自适应 max-h 320）。chip 不消费。
   */
  h?: number
}

/** v5 单面板布局状态（持久化 value 形状：`Record<panelId, PanelLayoutEntry>`）。 */
export interface PanelLayoutEntry {
  loc: PanelLocation
  collapsed: boolean
}

/** 面板定义——内置与插件唯一形态。 */
export interface PanelDefinition {
  /** 唯一 id。插件面板沿用 view.id；内置面板为 core.<name>。 */
  id: string
  title: string
  /** 图标名（经 pluginIcons.ts 的 pluginIcon 映射到 lucide）。 */
  icon: string
  /** 默认停靠槽位（v4 只有左栏）。 */
  defaultSlot: 'left'
  defaultMode: PanelMode
  /** floating 初始尺寸（缺省 320×280）。 */
  defaultSize?: { w: number; h: number }
  /** 渲染面板主体。返回 ReactNode；需要 hooks 时在模块级组件里调用。 */
  render: (ctx: PanelRenderContext) => ReactNode
  /** 标题栏徽标（每次渲染时同步求值；无则返回 null）。 */
  badges?: () => PanelBadge | null
  /**
   * v5 rail 徽章形态：优先于 badges() 文本 pill（rail 徽章条 + ＋N 收纳菜单 +
   * 徽章 popover 的紧凑详情均渲染它）。ctx 与 render 相同。registry 路扩展。
   */
  badgeRender?: (ctx: PanelRenderContext) => ReactNode
  /** 空态协议：render(ctx) 返回 null 时宿主显示统一空态；此文案自定义空态提示（缺省「暂无内容」）。 */
  emptyHint?: string
  /**
   * v5 面板位置声明：registry 路按 view.container 语义写入（面板类容器 →
   * zone 'side'；bar 类容器 → 徽章 zone）。缺省视为 side——ctx.panels.register
   * 的纯面板沿用现 docked 语义，零破坏。
   */
  location?: PanelLocation
  /** 'core' = 内置面板；缺省/其他 = pluginId。 */
  source?: string
}

/** ctx.panels 能力（'ui' 权限）。 */
export interface PanelsAPI {
  /** 注册面板；返回 disposable（调用即注销）。重复 id 覆盖旧定义。 */
  register(def: PanelDefinition): Disposable
  /** 更新已注册面板的标题/图标/徽标（仅限本插件自己的面板）。 */
  update(id: string, patch: Partial<Pick<PanelDefinition, 'title' | 'icon' | 'badges'>>): void
  /** 注销面板（仅限本插件自己的面板）。 */
  unregister(id: string): void
}
