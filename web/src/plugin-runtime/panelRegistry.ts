/**
 * panelRegistry ——「一切皆面板」统一 Panel API（布局 v4/v5）。
 *
 * 内置面板（source='core'）与插件面板（ctx.panels / view 贡献点自动转换）
 * 唯一注册入口。通知模式仿 registry.ts 的 view 订阅（subscribeViews/
 * notifyViewsChanged）：订阅者收到通知后重读 listPanels()。
 *
 * 本模块不持有布局状态（mode/x/y/collapsed 归 PanelLayout 的持久化层）——
 * registry 只回答「有哪些面板、怎么渲染」。
 *
 * v5：container→zone 映射（mapContainerToLocation）与 view 贡献点→面板定义
 * 构建（buildPanelDefs）也在这里——通用 container 语义规则，框架零插件特化
 * （禁止任何 if pluginId==='xxx'）。
 *
 * v5.1「Focus + Drawer」：side=钉选堆叠（core.sessions 永远置顶）；chip=底部
 * chips 启动器（未知容器/其余内置面板兜底）；right_sidebar 类侧栏容器 → side
 * 默认 h 220；bar 类容器 → 徽章（不变）。
 */
import { createElement, Fragment, type ReactNode } from 'react'

import type {
  PanelDefinition,
  PanelLocation,
  ViewContainer,
  ViewContribution,
} from '@/plugin-api'

export type {
  PanelBadge,
  PanelDefinition,
  PanelLayoutEntry,
  PanelLocation,
  PanelMode,
  PanelRenderContext,
  PanelsAPI,
  PanelZone,
  RailSegment,
} from '@/plugin-api'

type PanelListener = () => void

// ── v5.1：container → zone 通用映射 ──────────────────────────────────────────

/** 同 zone 内的默认排序键（注册侧无排序信息时）。 */
const DEFAULT_ORDER = 0

/**
 * bar 类容器 → 徽章 location。bar 类 = 条状区域直渲染容器（顶栏右侧、底栏
 * 信息条）——声明到这些容器的 view 贡献点不进面板堆叠区/chips，以徽章形态
 * 注册（badgeRender），由对应 rail 的渲染点消费。这是通用 container 语义规则
 * （数据表驱动），不是插件特化；新 bar 容器在此追加一条即可。
 */
const BADGE_CONTAINER_LOCATIONS: Readonly<Record<string, { zone: PanelLocation['zone']; segment?: PanelLocation['segment'] }>> = {
  status_bar_right: { zone: 'top', segment: 'right' },
  info_bar: { zone: 'bottom' },
  bottom: { zone: 'bottom' },
}

/**
 * v5.1 侧栏类容器 → 钉选 location。插件声明了侧栏容器即默认钉选（zone
 * 'side'），默认 body 高度 h=220（可拖拽调高）。数据表驱动——新侧栏容器
 * 追加一条即可，零插件特化。
 */
const SIDE_CONTAINER_LOCATIONS: Readonly<Record<string, { zone: PanelLocation['zone']; h: number }>> = {
  right_sidebar: { zone: 'side', h: 220 },
}

/**
 * container → 面板位置（v5.1「Focus + Drawer」）：bar 类容器（
 * BADGE_CONTAINER_LOCATIONS）→ 徽章 zone；侧栏类容器（SIDE_CONTAINER_LOCATIONS）
 * → 钉选 side（默认 h 220）；其余（含 panel/iteration/main 与未知容器）兜底
 * zone 'chip'（底部 chips 启动器）——plugin.json 的 container 声明零破坏
 * （读侧映射）。
 */
export function mapContainerToLocation(container: ViewContainer): PanelLocation {
  const badge = BADGE_CONTAINER_LOCATIONS[container]
  if (badge) return { order: DEFAULT_ORDER, ...badge }
  const side = SIDE_CONTAINER_LOCATIONS[container]
  if (side) return { order: DEFAULT_ORDER, ...side }
  return { zone: 'chip', order: DEFAULT_ORDER }
}

// ── v5：view 贡献点 → 面板定义构建（含徽章合并）──────────────────────────────

/** runtime.listAllViews() 的单个条目。 */
export interface ViewWithPlugin {
  pluginId: string
  view: ViewContribution
}

/** buildPanelDefs 的单个产物：面板定义 + 它归属的插件与主 view。 */
export interface BuiltPanel {
  def: PanelDefinition
  /** def 归属的主 view（side 面板 = 声明 view；独立徽章面板 = 徽章 view 本身）。 */
  view: ViewContribution
  pluginId: string
}

/**
 * 把插件 view 贡献点构建为面板定义（通用 container 语义，零插件特化）：
 * - bar 类容器 → 徽章贡献：同 pluginId 另有主 view（side/chip/main 面板类）
 *   时，转为主面板的 badgeRender（同 panelId 合并，不产生独立面板）；否则注册
 *   为独立徽章面板——徽章面板无面板主体（主体即徽章），render 置 null，
 *   保证旧面板引擎全量渲染 defs 时无可见残留。
 * - 面板类容器（side 钉选 / chip 启动器 / 其余兜底）→ 主面板，location 直接
 *   采用 mapContainerToLocation 产物（right_sidebar → side h 220；未知 → chip）。
 * 同插件多个徽章贡献按声明序合并（Fragment 包裹）；同插件多个主 view 时
 * 首个生效（声明序，与 runtime.listAllViews 顺序一致）。
 */
export function buildPanelDefs(
  views: ReadonlyArray<ViewWithPlugin>,
  renderView: (pluginId: string, view: ViewContribution) => ReactNode,
): BuiltPanel[] {
  const isBadgeView = (view: ViewContribution): boolean => {
    const zone = mapContainerToLocation(view.container).zone
    return zone === 'top' || zone === 'bottom'
  }

  // 按 pluginId 分组：主 view（面板类容器）与徽章贡献（bar 类容器）。
  const mainViews = new Map<string, ViewWithPlugin>()
  const badgesByPlugin = new Map<string, ViewWithPlugin[]>()
  for (const entry of views) {
    if (isBadgeView(entry.view)) {
      const list = badgesByPlugin.get(entry.pluginId)
      if (list) list.push(entry)
      else badgesByPlugin.set(entry.pluginId, [entry])
    } else if (!mainViews.has(entry.pluginId)) {
      mainViews.set(entry.pluginId, entry)
    }
  }

  const composeBadgeRender = (badges: ReadonlyArray<ViewWithPlugin>): (() => ReactNode) => {
    if (badges.length === 1) {
      const { pluginId, view } = badges[0]
      return () => renderView(pluginId, view)
    }
    return () =>
      createElement(
        Fragment,
        null,
        ...badges.map(({ pluginId, view }) => renderView(pluginId, view)),
      )
  }

  const out: BuiltPanel[] = []
  // 主面板（zone side/chip）：同插件的徽章贡献合并为 badgeRender。
  // location 直接采用 container 映射产物（side 默认 h 220 / chip 兜底）。
  for (const { pluginId, view } of mainViews.values()) {
    const badges = badgesByPlugin.get(pluginId)
    out.push({
      pluginId,
      view,
      def: {
        id: view.id,
        title: view.title,
        icon: view.icon ?? '',
        defaultSlot: 'left',
        defaultMode: 'docked',
        location: mapContainerToLocation(view.container),
        render: () => renderView(pluginId, view),
        ...(badges ? { badgeRender: composeBadgeRender(badges) } : {}),
        source: pluginId,
      },
    })
  }
  // 独立徽章面板：插件没有主 view 时的徽章贡献（每个徽章 view 一个 def）。
  for (const { pluginId, view } of views) {
    if (!isBadgeView(view) || mainViews.has(pluginId)) continue
    out.push({
      pluginId,
      view,
      def: {
        id: view.id,
        title: view.title,
        icon: view.icon ?? '',
        defaultSlot: 'left',
        defaultMode: 'docked',
        location: mapContainerToLocation(view.container),
        // 徽章面板无面板主体（主体即徽章）；render 置 null——旧面板引擎
        // （dock/floating 全量渲染 defs）过渡期不产生可见残留。
        render: () => null,
        badgeRender: () => renderView(pluginId, view),
        source: pluginId,
      },
    })
  }
  return out
}

// ── 面板注册表 ────────────────────────────────────────────────────────────────

class PanelRegistryImpl {
  private panels = new Map<string, PanelDefinition>()
  private listeners = new Set<PanelListener>()

  /** 注册/覆盖面板定义。 */
  registerPanel(def: PanelDefinition): void {
    this.panels.set(def.id, def)
    this.notify()
  }

  /** 注销面板（不存在时静默）。 */
  unregisterPanel(id: string): void {
    if (this.panels.delete(id)) this.notify()
  }

  /** 当前全部面板（注册顺序）。返回副本——调用方可安全持有/排序。 */
  listPanels(): PanelDefinition[] {
    return [...this.panels.values()]
  }

  getPanel(id: string): PanelDefinition | undefined {
    return this.panels.get(id)
  }

  /** 订阅面板集合变化（注册/注销）。返回退订函数。 */
  subscribePanels(listener: PanelListener): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  private notify(): void {
    for (const listener of [...this.listeners]) {
      try {
        listener()
      } catch (error) {
        console.error('[panel-registry] subscriber failed', error)
      }
    }
  }
}

/** 全局单例。 */
export const panelRegistry = new PanelRegistryImpl()
