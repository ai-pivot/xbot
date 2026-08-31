/**
 * PanelLayout ——「一切皆面板」停靠引擎（布局 v5.1「Focus + Drawer」）。
 *
 * 数据模型 v2：`Record<panelId, { loc: PanelLocation, collapsed: boolean }>`。
 * loc.zone 分发到五个渲染宿主（同一 state，一个面板恰好渲染一处）：
 *  - side     → PanelDock 钉选堆叠区（data-panel-zone="side"）
 *  - chip     → SideChips 底部启动器（data-panel-zone="chip"）
 *  - top      → TopRail（徽章 rail，rails.tsx）
 *  - bottom   → BottomRailBadges（徽章 rail，rails.tsx）
 *  - floating → FloatingLayer（自由浮层）
 *
 * v5.1 钉选堆叠（四条硬性要求之「高度可设置 + 永不挤压」）：
 *  - 堆叠区 flex-1 min-h-0 overflow-y-auto，面板自然高度堆叠——废除 flex-1/
 *    flex 收缩分配，任何面板不被压缩；超高整栏滚动（chips 条与相邻面板高度
 *    均不变）。
 *  - 钉选面板高度 loc.h：存在 → body height=h（内部滚动）；缺省 → 自适应
 *    内容 max-h 320。底边拖拽 handle 调高（clamp 140–640）。
 *  - 默认分配（数据表 PINNED_DEFAULTS，零过程式特化）：core.sessions → side
 *    永远置顶（默认 h 420，无 ✕）；其余内置面板 → chip；插件面板尊重
 *    contribution（right_sidebar → side h 220；未知容器 → chip）。
 *
 * 持久化：localStorage `xbot:panel-layout-v2`（缓存）+ user_settings
 * `web:ui:panel-layout-v2`（权威，syncSettingToServer debounce 写回 +
 * SETTINGS_SYNCED_EVENT 重读）。v1 数据（`xbot:panel-layout`）读时迁移：
 * docked→{zone:'side', order=dockOrder 序}；floating→保留 xywh。v2→v5.1 迁移
 * （migrateV2Layout）：zone 'side' 的非钉选面板 → 'chip'（v5 无钉选概念，先全
 * 收 chips 用户可再钉）；幂等；坏数据回退默认布局。
 *
 * 拖拽协议 v5（修 v4 三 bug）：
 *  1. 拖动/缩放中只改本地 drag state（渲染跟随），pointerup 才 update+persist 一次
 *     （v4 每帧 update→persist，localStorage 写 + 服务端 spam）。
 *  2. dock 重排 move 中真实写入 dropHint（v4 从未写入，插入线永不显示）。
 *  3. 重排基于渲染顺序 sideIds 计算（v4 基于裸 dockOrder，初次为空必 no-op），
 *     落盘完整 order。
 * 新交互：zone 高亮 + 形态预告（move 中 elementFromPoint().closest(
 * '[data-panel-zone]') 判 activeZone，宿主根元素 accent 虚线 ring；ghost 按
 * zone 显示徽章/完整面板预告）；跨 zone 放置（side→插入位 order；chip→收纳；
 * top/bottom→segment 按落点左右半；floating→原地 xywh）；Esc/pointercancel/
 * 落点无 zone 零状态变更；4px 位移阈值防误触；floating 默认落位主区中上部
 * （layer 宽 40% × 高 22%，阶梯 offset），不再 48,56 盖侧栏。
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'

import { panelRegistry } from '@/plugin-runtime/panelRegistry'
import type {
  PanelDefinition,
  PanelLayoutEntry,
  PanelLocation,
  PanelZone,
  RailSegment,
} from '@/plugin-api'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import type { TabManager } from '@/hooks/useTabManager'
import { PanelChrome } from './PanelChrome'
import { SideChips } from './rails'

const LS_KEY_V2 = 'xbot:panel-layout-v2'
const LS_KEY_V1 = 'xbot:panel-layout'
const MIN_W = 220
const MIN_H = 120
const DOCK_BODY_MAX_H = 320
const FLOATING_STEP = 24
/** 拖拽位移阈值：小于此值的 pointerup 视为误触（点击），零状态变更。 */
const DRAG_THRESHOLD = 4
/** jsdom/无布局环境合成 floating 默认 xywh 用的虚拟视口（仅 layer 尺寸为 0 时）。 */
const FALLBACK_VIEWPORT = { w: 1280, h: 800 }

// ── v5.1 钉选堆叠常量（数据表驱动，零过程式特化）────────────────────────────

/** 钉选面板 body 高度拖拽 clamp 边界。 */
const DOCK_H_MIN = 140
const DOCK_H_MAX = 640
/** chip → side 钉选时的默认 body 高度。 */
const PIN_DEFAULT_H = 220
/**
 * v5.1 唯一钉选面板默认值表（内置面板数据表，非插件特化——插件面板绝不进入
 * 此表，插件位置一律尊重 contribution）。key = 面板 id；h = 默认 body 高度。
 * core.sessions 永远置顶：无 ✕（不可取消钉选），默认 h 420。
 */
const PINNED_DEFAULTS: Readonly<Record<string, { h: number }>> = {
  'core.sessions': { h: 420 },
}

export type { PanelDefinition, PanelLayoutEntry, PanelLocation, PanelZone, RailSegment }

/** v2 布局状态。 */
export type PanelLayoutState = Record<string, PanelLayoutEntry>

/** v1 布局状态（迁移源）。 */
interface PanelRectV1 {
  mode: 'docked' | 'floating'
  x: number
  y: number
  w: number
  h: number
  collapsed: boolean
}

interface PanelLayoutStateV1 {
  panels: Record<string, PanelRectV1>
  dockOrder: string[]
}

// ── 纯函数：读取 / 校验 / 迁移（导出供测试）─────────────────────────────────

const ZONES: readonly PanelZone[] = ['side', 'chip', 'top', 'bottom', 'floating']
const SEGMENTS: readonly RailSegment[] = ['left', 'center', 'right']

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** 单 entry 校验：zone/segment 合法 + order 数字 + collapsed 布尔；floating 需 xywh 数字；h（side/floating）存在时须为有限数字。 */
export function isValidPanelEntry(v: unknown): v is PanelLayoutEntry {
  if (!isRecord(v) || !isRecord(v.loc)) return false
  const loc = v.loc
  if (!ZONES.includes(loc.zone as PanelZone)) return false
  if (typeof loc.order !== 'number' || !Number.isFinite(loc.order)) return false
  if (typeof v.collapsed !== 'boolean') return false
  if (loc.segment !== undefined && !SEGMENTS.includes(loc.segment as RailSegment)) return false
  if (loc.h !== undefined && (typeof loc.h !== 'number' || !Number.isFinite(loc.h))) return false
  if (loc.zone === 'floating') {
    for (const k of ['x', 'y', 'w', 'h'] as const) {
      if (typeof loc[k] !== 'number' || !Number.isFinite(loc[k])) return false
    }
  }
  return true
}

/**
 * 解析 v2 JSON 字符串。整体结构坏（非 JSON/非对象）→ null（调用方回退默认布局）；
 * 单个 entry 坏 → 丢弃该 entry。
 *
 * ⚠️ 不按 knownIds 过滤——插件面板异步注册，useState 初始化时 knownIds 可能
 * 只有 core.* 面板。如果过滤，插件面板的 collapsed/zone/h 状态在刷新后全部丢失。
 * 未知 id 的 entry 保留在 state 里（渲染时 byZone 只渲染 defs 里的面板，无害）；
 * 插件注册后 entryOf 从 state 读到存储的状态 → 恢复。
 */
export function parsePanelLayoutV2(raw: string | null, _knownIds?: ReadonlySet<string>): PanelLayoutState | null {
  if (raw == null) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!isRecord(parsed)) return null
  const state: PanelLayoutState = {}
  for (const [id, entry] of Object.entries(parsed)) {
    if (isValidPanelEntry(entry)) state[id] = entry
  }
  return state
}

/**
 * v1→v2 迁移（纯函数，幂等）：docked→{zone:'side', order=dockOrder 序}；
 * floating→保留 xywh。不在 dockOrder 的 docked 面板接在其后；未知 id 丢弃；
 * 结构坏 → null（回退默认布局）。
 */
export function migrateV1Layout(raw: string | null, knownIds: ReadonlySet<string>): PanelLayoutState | null {
  if (raw == null) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (!isRecord(parsed) || !isRecord(parsed.panels) || !Array.isArray(parsed.dockOrder)) return null
  const v1 = parsed as unknown as PanelLayoutStateV1
  // docked order：dockOrder 序 + 未列出的 docked 面板依次接尾。
  let nextOrder = 0
  const orderById = new Map<string, number>()
  for (const id of v1.dockOrder) {
    if (typeof id === 'string' && knownIds.has(id) && !orderById.has(id)) orderById.set(id, nextOrder++)
  }
  const state: PanelLayoutState = {}
  for (const [id, rect] of Object.entries(v1.panels)) {
    if (!knownIds.has(id) || !isRecord(rect)) continue
    const r = rect as unknown as PanelRectV1
    const collapsed = typeof r.collapsed === 'boolean' ? r.collapsed : id !== 'core.sessions'
    if (r.mode === 'floating') {
      const x = typeof r.x === 'number' && Number.isFinite(r.x) ? r.x : 0
      const y = typeof r.y === 'number' && Number.isFinite(r.y) ? r.y : 0
      const w = typeof r.w === 'number' && Number.isFinite(r.w) ? r.w : 320
      const h = typeof r.h === 'number' && Number.isFinite(r.h) ? r.h : 280
      state[id] = { loc: { zone: 'floating', order: 0, x, y, w, h }, collapsed }
    } else {
      // v1 docked：sessions → side（钉选）；非 sessions → chip（v5.2 直接归入
      // chips，不再经 migrateV2Layout 二次迁移——避免刷新时把用户 pin 的
      // side 面板错误迁移回 chip）。
      if (id === 'core.sessions') {
        let order = orderById.get(id)
        if (order === undefined) {
          order = nextOrder++
          orderById.set(id, order)
        }
        state[id] = { loc: { zone: 'side', order }, collapsed }
      } else {
        // v1 docked 非 sessions → chip（v5.2 直接归入 chips，保留 dockOrder 序）
        let order = orderById.get(id)
        if (order === undefined) {
          order = nextOrder++
          orderById.set(id, order)
        }
        state[id] = { loc: { zone: 'chip', order }, collapsed }
      }
    }
  }
  return state
}

// ── v5.1 默认分配（数据表驱动，零过程式特化）─────────────────────────────────

/**
 * 单面板默认 entry：PINNED_DEFAULTS 命中（core.sessions）→ side 钉选（默认
 * h 420，展开）；其余内置面板 → chip 收纳；插件面板尊重 contribution
 * （def.location：right_sidebar → side h 220；未知容器 → chip）——零特化。
 */
function defaultEntryOf(id: string, def: PanelDefinition | undefined, order: number): PanelLayoutEntry {
  const pinned = PINNED_DEFAULTS[id]
  if (pinned) return { loc: { zone: 'side', order, h: pinned.h }, collapsed: false }
  if (def?.source === 'core') return { loc: { zone: 'chip', order }, collapsed: true }
  if (def?.location) return { loc: def.location, collapsed: true }
  return { loc: { zone: 'chip', order }, collapsed: true }
}

/**
 * 默认布局（无任何存储时）：core.sessions → side 置顶（默认 h 420），其余
 * 内置面板 → chip，插件面板按 def.location（contribution 默认位置）。
 */
export function defaultPanelLayout(defsInDefOrder: readonly PanelDefinition[]): PanelLayoutState {
  const state: PanelLayoutState = {}
  defsInDefOrder.forEach((def, i) => {
    state[def.id] = defaultEntryOf(def.id, def, i)
  })
  return state
}

/**
 * v2 → v5.1 迁移（纯函数，幂等）：v5 无钉选概念——持久化中 zone 'side' 的
 * 非 sessions 面板全部 → 'chip'（用户可再钉选）；side 的 h 规范化到拖拽 clamp
 * 边界；chip 清掉无意义的高度/分段/浮层字段。重复执行结果一致。
 */
/**
 * v2→v5.1 迁移：v1 旧格式（mode: docked/floating）的面板 → v5.1 zone 格式。
 * 已是 v2 格式（loc.zone 存在）的 side 面板不迁移——用户 pin 的面板刷新后保持
 * pin + collapsed（修 v5.1 回归：之前把所有 side 非 sessions 迁回 chip）。
 */
export function migrateV2Layout(prev: PanelLayoutState): PanelLayoutState {
  let changed = false
  const next: PanelLayoutState = {}
  for (const [id, entry] of Object.entries(prev)) {
    // v2 格式（已有 loc.zone）：只做 side 面板的 h clamp，不改变 zone/collapsed。
    if (entry.loc.zone === 'side' && entry.loc.h != null) {
      const h = Math.min(Math.max(DOCK_H_MIN, entry.loc.h), DOCK_H_MAX)
      if (h !== entry.loc.h) {
        next[id] = { ...entry, loc: { ...entry.loc, h } }
        changed = true
        continue
      }
    }
    next[id] = entry
  }
  return changed ? next : prev
}

// ── 拖拽状态 ────────────────────────────────────────────────────────────────

/** layer 容器的视口几何（getBoundingClientRect 的必要子集；jsdom 安全）。 */
interface LayerRect {
  left: number
  top: number
  width: number
  height: number
}

const ZERO_LAYER: LayerRect = { left: 0, top: 0, width: FALLBACK_VIEWPORT.w, height: FALLBACK_VIEWPORT.h }

function layerRectOf(el: HTMLElement | null): LayerRect {
  const r = el?.getBoundingClientRect()
  if (!r || (r.width === 0 && r.height === 0)) return ZERO_LAYER
  return { left: r.left, top: r.top, width: r.width, height: r.height }
}

interface PanelDragState {
  kind: 'panel'
  id: string
  /** 当前指针位置（视口坐标）。 */
  pointer: { x: number; y: number }
  /** pointerdown 起点（4px 阈值判定）。 */
  startX: number
  startY: number
  /** 超过阈值后的真实拖拽（之前不显示 ghost/不判定 zone）。 */
  started: boolean
  /** 指针相对面板左上角的抓取偏移（视口坐标差；side 面板起拖为面板中心预告）。 */
  grabOffset: { x: number; y: number }
  /** 起拖时的面板尺寸（floating 跟随 + ghost 完整预告用）。 */
  originW: number
  originH: number
  /** 起拖时 layer 容器几何（渲染跟随与 up 落盘共用，拖动中不变）。 */
  layer: LayerRect
}

/** 浮窗 resize 方向（四角 + 四边，子串包含方向字母：'ne' 含 n+e）。 */
export type ResizeDir = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw'

interface ResizeDragState {
  kind: 'resize'
  id: string
  /** resize 方向（哪条边/角被拖）。 */
  dir: ResizeDir
  startX: number
  startY: number
  /** 起拖矩形（move 从 startRect + 指针总 delta 绝对计算——零增量累计误差）。 */
  startRect: { x: number; y: number; w: number; h: number }
  /** 当前矩形（move 中本地跟随；up 落盘）。 */
  curX: number
  curY: number
  curW: number
  curH: number
  layer: LayerRect
}

/** v5.1 side 面板底边调高（move 中本地跟随，up 一次落盘；clamp 140–640）。 */
interface HeightDragState {
  kind: 'height'
  id: string
  startX: number
  startY: number
  curH: number
}

type DragState = PanelDragState | ResizeDragState | HeightDragState

export interface DropHint {
  targetId: string
  before: boolean
}

interface PanelDockContextValue {
  tabManager: TabManager
  defs: PanelDefinition[]
  /** 合成默认后的单面板布局（读 state[id]，无则默认）。 */
  entryOf: (id: string) => PanelLayoutEntry
  /** 指定 zone 的渲染顺序（order 升序；side 序 = 重排与落盘的基准——修 v4 bug 3）。 */
  zoneIds: (zone: PanelZone) => string[]
  /** 拖拽/缩放本地状态（move 中渲染跟随的数据源；非 null 时零持久化）。 */
  drag: DragState | null
  /** 拖拽悬停判定的目标 zone（宿主 ring 高亮 + ghost 形态预告）。 */
  activeZone: PanelZone | null
  dropHint: DropHint | null
  dragSrcId: string | null
  /** 拖拽 ghost 跟随的指针位置（视口坐标；阈值内/resize 为 null）。 */
  dragPointer: { x: number; y: number } | null
  toggleCollapse: (id: string) => void
  /** 升浮窗（rail 徽章 ⤢ / chips 单击 / docked ⤶ / 双击）：主区中上部 + 阶梯 offset 落位。 */
  floatPanel: (id: string) => void
  /** 收回 chips（floating 关闭按钮 / 双击标题）——v5.1：浮动退出一律回收纳态。 */
  dockPanel: (id: string) => void
  /** 钉选（chips 📌）：zone 'side'，append 堆叠尾，默认 h 220。 */
  pinPanel: (id: string) => void
  /** 取消钉选（side 面板 ✕）：→ 'chip'。PINNED_DEFAULTS 面板不可取消（无 ✕ 入口）。 */
  unpinPanel: (id: string) => void
  onGripPointerDown: (id: string) => (e: ReactPointerEvent<HTMLElement>) => void
  onTitlePointerDown: (id: string) => (e: ReactPointerEvent<HTMLElement>) => void
  /** floating 全方向 resize（pointerdown 起始；四角+四边手柄，dir 见 ResizeDir）。 */
  onResizePointerDown: (id: string) => (dir: ResizeDir, e: ReactPointerEvent<HTMLElement>) => void
  /** v5.1 side 面板底边调高（拖拽协议 v5：move 零持久化，up 一次落盘 clamp 140–640）。 */
  onHeightPointerDown: (id: string) => (e: ReactPointerEvent<HTMLElement>) => void
  registerDockEl: (el: HTMLElement | null) => void
  registerLayerEl: (el: HTMLElement | null) => void
}

const PanelDockContext = createContext<PanelDockContextValue | null>(null)

export function usePanelDock(): PanelDockContextValue {
  const ctx = useContext(PanelDockContext)
  if (!ctx) throw new Error('usePanelDock must be used within <PanelDockProvider>')
  return ctx
}

function persist(state: PanelLayoutState): void {
  try {
    const json = JSON.stringify(state)
    localStorage.setItem(LS_KEY_V2, json)
    // 照抄 userSettings.ts 模式：写 localStorage 立即 + debounce 写回 server。
    syncSettingToServer(LS_KEY_V2, json)
  } catch {
    /* storage unavailable */
  }
}

function safeGet(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function PanelDockProvider({ tabManager, children }: { tabManager: TabManager; children: ReactNode }): ReactNode {
  // 面板定义（订阅 registry——插件加载/卸载时刷新）。
  const [defs, setDefs] = useState<PanelDefinition[]>(() => panelRegistry.listPanels())
  useEffect(() => {
    const recompute = () => setDefs(panelRegistry.listPanels())
    recompute()
    return panelRegistry.subscribePanels(recompute)
  }, [])

  const defMap = useMemo(() => new Map(defs.map((d) => [d.id, d])), [defs])
  const knownIds = useMemo(() => new Set(defs.map((d) => d.id)), [defs])

  // 布局状态 v2：localStorage v2 → v1 迁移 → 默认布局（未知 id 丢弃）。
  // v2/v1 迁移产物统一过 migrateV2Layout（v5.1：side 非 sessions → chip）。
  const [state, setState] = useState<PanelLayoutState>(() => {
    const list = panelRegistry.listPanels()
    const known = new Set(list.map((d) => d.id))
    const loaded = parsePanelLayoutV2(safeGet(LS_KEY_V2), known) ?? migrateV1Layout(safeGet(LS_KEY_V1), known)
    return loaded ? migrateV2Layout(loaded) : defaultPanelLayout(list)
  })
  const stateRef = useRef(state)
  stateRef.current = state

  // defs 变化（插件注册/注销）→ 不清理未知 id 的 entry（插件异步注册，
  // knownIds 初始可能不含插件 id——清理会丢失它们的 collapsed/h 等持久化状态）。
  // 未知 id 的 entry 留在 state 里无害：byZone 只渲染 defs 里的面板，entry
  // 在插件注册后自动恢复。persist 写入时也保留（不丢数据）。
  // 仅清理已知已卸载的面板——由 panelRegistry.unregisterPanel 触发（如需）。

  // Server sync（SETTINGS_SYNCED_EVENT）→ localStorage 已被更新，重读（权威覆盖）。
  useEffect(() => {
    const handler = () => {
      const known = new Set(panelRegistry.listPanels().map((d) => d.id))
      const loaded = parsePanelLayoutV2(safeGet(LS_KEY_V2), known)
      if (loaded) setState(migrateV2Layout(loaded))
    }
    window.addEventListener(SETTINGS_SYNCED_EVENT, handler)
    return () => window.removeEventListener(SETTINGS_SYNCED_EVENT, handler)
  }, [])

  const update = useCallback((fn: (prev: PanelLayoutState) => PanelLayoutState) => {
    setState((prev) => {
      const next = fn(prev)
      persist(next)
      return next
    })
  }, [])

  // 未显式设置的面板 → 合成默认（不写入，交互时才固化）。
  const entryOf = useCallback(
    (id: string): PanelLayoutEntry => {
      const stored = stateRef.current[id]
      if (stored) return stored
      const order = knownIds.has(id) ? [...knownIds].indexOf(id) : 0
      return defaultEntryOf(id, defMap.get(id), order)
    },
    [knownIds, defMap],
  )

  // ── 五宿主渲染顺序（渲染序 = 拖拽重排与落盘的唯一基准）──────────────────
  // ⚠️ deps 必须含 state：entryOf 读 stateRef 引用稳定，zone/order 落盘后
  // stateRef.current 已变——缺 state 则 zoneIds 永不重算，拖拽落盘后渲染序
  // 不更新（测试「重排基于渲染序」复现）。
  const byZone = useCallback(
    (zone: PanelZone): string[] =>
      defs
        .filter((d) => entryOf(d.id).loc.zone === zone)
        .sort((a, b) => entryOf(a.id).loc.order - entryOf(b.id).loc.order)
        .map((d) => d.id),
    [defs, entryOf, state],
  )
  const zoneIds = byZone

  // ── 状态变更（唯一写入口：全部走 update→persist 一次）────────────────────

  const toggleCollapse = useCallback(
    (id: string) => {
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        return { ...prev, [id]: { ...cur, collapsed: !cur.collapsed } }
      })
    },
    [update, entryOf],
  )

  /** 收回 chips（v5.1：floating 退出一律回收纳态——「临时使用不占侧栏」）。 */
  const dockPanel = useCallback(
    (id: string) => {
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        if (cur.loc.zone !== 'floating') return prev
        const { x: _x, y: _y, w: _w, h: _h, segment: _s, ...rest } = cur.loc
        return { ...prev, [id]: { ...cur, loc: { ...rest, zone: 'chip' } } }
      })
    },
    [update, entryOf],
  )

  /** 钉选（chips 📌）：zone 'side'，append 堆叠尾，默认 h 220（floating 已有 h 则 clamp 复用）。 */
  const pinPanel = useCallback(
    (id: string) => {
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        if (cur.loc.zone === 'side') return prev
        // append 到 side 渲染序尾（order = 现有最大 +1）。
        const maxOrder = defs.reduce((m, d) => {
          const loc = (prev[d.id] ?? entryOf(d.id)).loc
          return loc.zone === 'side' ? Math.max(m, loc.order) : m
        }, -1)
        const h = cur.loc.h != null ? Math.min(Math.max(DOCK_H_MIN, cur.loc.h), DOCK_H_MAX) : PIN_DEFAULT_H
        const { x: _x, y: _y, w: _w, segment: _s, ...rest } = cur.loc
        return { ...prev, [id]: { ...cur, loc: { ...rest, zone: 'side', order: maxOrder + 1, h }, collapsed: false } }
      })
    },
    [update, entryOf, defs],
  )

  /** 取消钉选（side 面板 ✕）→ 'chip'。PINNED_DEFAULTS 面板无 ✕ 入口（不可取消）。 */
  const unpinPanel = useCallback(
    (id: string) => {
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        if (cur.loc.zone !== 'side' || PINNED_DEFAULTS[id]) return prev
        const { h: _h, segment: _s, x: _x, y: _y, w: _w, ...rest } = cur.loc
        return { ...prev, [id]: { ...cur, loc: { ...rest, zone: 'chip' } } }
      })
    },
    [update, entryOf],
  )

  /** 升浮窗：主区中上部落位（layer 宽 40% × 高 22%，阶梯 offset 防重叠）。 */
  const floatPanel = useCallback(
    (id: string) => {
      const c = layerRectOf(layerElRef.current)
      const def = defMap.get(id)
      const w = def?.defaultSize?.w ?? Math.round(c.width * 0.4)
      const h = def?.defaultSize?.h ?? Math.round(c.height * 0.22)
      const floatingCount = Object.values(stateRef.current).filter((e) => e.loc.zone === 'floating').length
      const offset = floatingCount * FLOATING_STEP
      const x = Math.max(0, Math.round((c.width - w) / 2) + offset)
      const y = Math.max(0, Math.round(c.height * 0.12) + offset)
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        return {
          ...prev,
          [id]: { ...cur, loc: { zone: 'floating', order: floatingCount, x, y, w, h }, collapsed: false },
        }
      })
    },
    [update, entryOf, defMap],
  )

  // ── 拖拽状态（move 中零持久化；up 才 update+persist 一次——修 v4 bug 1）──
  const [drag, setDrag] = useState<DragState | null>(null)
  const [dropHint, setDropHint] = useState<DropHint | null>(null)
  const [activeZone, setActiveZone] = useState<PanelZone | null>(null)
  const dragRef = useRef<DragState | null>(null)
  dragRef.current = drag
  const dockElRef = useRef<HTMLElement | null>(null)
  const layerElRef = useRef<HTMLElement | null>(null)
  const registerDockEl = useCallback((el: HTMLElement | null) => { dockElRef.current = el }, [])
  const registerLayerEl = useCallback((el: HTMLElement | null) => { layerElRef.current = el }, [])

  /** move 中判定悬停 zone：elementFromPoint 最近的 [data-panel-zone] 宿主。 */
  const zoneAtPoint = useCallback((clientX: number, clientY: number): PanelZone | null => {
    const el = document.elementFromPoint(clientX, clientY)
    const zone = el?.closest<HTMLElement>('[data-panel-zone]')?.dataset.panelZone
    return ZONES.includes(zone as PanelZone) ? (zone as PanelZone) : null
  }, [])

  /** move 中判定 side 插入目标（渲染序 sideIds 上的 [data-dock-item]）。 */
  const sideHintAtPoint = useCallback((clientX: number, clientY: number): DropHint | null => {
    const el = document.elementFromPoint(clientX, clientY)?.closest<HTMLElement>('[data-dock-item]')
    const targetId = el?.dataset.dockItem
    if (!targetId || !el) return null
    const r = el.getBoundingClientRect()
    return { targetId, before: clientY < r.top + r.height / 2 }
  }, [])

  /** 结束拖拽（正常落点 / 取消共用）：清全部本地拖拽态。 */
  const endDrag = useCallback(() => {
    setDrag(null)
    setDropHint(null)
    setActiveZone(null)
  }, [])

  /** pointerup 落点 → 跨 zone 放置（规格 5；落点无 zone = 取消零变更）。 */
  const placeDropped = useCallback(
    (id: string, ev: { clientX: number; clientY: number }) => {
      const zone = zoneAtPoint(ev.clientX, ev.clientY)
      if (!zone) return // 取消：零状态变更
      const cur = stateRef.current[id] ?? entryOf(id)
      const d = dragRef.current
      if (zone === 'floating') {
        // floating：原地 xywh（跟随中的面板位置）。
        const layer = d?.kind === 'panel' ? d.layer : layerRectOf(layerElRef.current)
        const w = d?.kind === 'panel' ? d.originW : (cur.loc.w ?? 320)
        const h = d?.kind === 'panel' ? d.originH : (cur.loc.h ?? 280)
        const gx = d?.kind === 'panel' ? d.grabOffset.x : w / 2
        const gy = d?.kind === 'panel' ? d.grabOffset.y : h / 2
        const x = Math.max(0, Math.round(ev.clientX - layer.left - gx))
        const y = Math.max(0, Math.round(ev.clientY - layer.top - gy))
        update((prev) => ({
          ...prev,
          [id]: { ...(prev[id] ?? cur), loc: { zone: 'floating', order: 0, x, y, w, h }, collapsed: false },
        }))
        return
      }
      if (zone === 'chip') {
        // chips 收纳（拖入底部 chips 条 / 📌 反向）：无高度语义。
        update((prev) => {
          const e = prev[id] ?? entryOf(id)
          const { h: _h, segment: _s, x: _x, y: _y, w: _w, ...rest } = e.loc
          return { ...prev, [id]: { ...e, loc: { ...rest, zone: 'chip' } } }
        })
        return
      }
      if (zone === 'side') {
        // side：插入位 order（基于渲染序 sideIds——修 v4 bug 3；落盘完整 order）。
        const sideIds = zoneIds('side')
        const hint = sideHintAtPoint(ev.clientX, ev.clientY)
        const others = sideIds.filter((x) => x !== id)
        let index: number
        if (hint && hint.targetId !== id) {
          const to = others.indexOf(hint.targetId)
          index = to === -1 ? others.length : hint.before ? to : to + 1
        } else {
          index = others.length
        }
        const order = [...others.slice(0, index), id, ...others.slice(index)]
        update((prev) => {
          const next = { ...prev }
          order.forEach((pid, i) => {
            const e = next[pid] ?? entryOf(pid)
            const { segment: _s, x: _x, y: _y, w: _w, ...rest } = e.loc
            const h = PINNED_DEFAULTS[pid]
              ? (e.loc.h != null ? Math.min(Math.max(DOCK_H_MIN, e.loc.h), DOCK_H_MAX) : PINNED_DEFAULTS[pid].h)
              : (e.loc.h != null ? Math.min(Math.max(DOCK_H_MIN, e.loc.h), DOCK_H_MAX) : undefined)
            next[pid] = { ...e, loc: { ...rest, zone: 'side', order: i, ...(h !== undefined ? { h } : {}) } }
          })
          return next
        })
        return
      }
      // top/bottom：segment 按落点左右半（rail 左半 → left、右半 → right；
      // center 无拖放入口，类型预留），order = 该 zone 现有最大 +1。
      const railEl = document.querySelector<HTMLElement>(`[data-panel-zone="${zone}"]`)
      const r = railEl?.getBoundingClientRect()
      const leftHalf = r ? ev.clientX < r.left + r.width / 2 : true
      const segment: RailSegment = leftHalf ? 'left' : 'right'
      const maxOrder = defs.reduce((m, dd) => {
        const loc = (stateRef.current[dd.id] ?? entryOf(dd.id)).loc
        return loc.zone === zone ? Math.max(m, loc.order) : m
      }, -1)
      update((prev) => {
        const e = prev[id] ?? entryOf(id)
        const { x: _x, y: _y, w: _w, h: _h, ...rest } = e.loc
        return { ...prev, [id]: { ...e, loc: { ...rest, zone, segment, order: maxOrder + 1 } } }
      })
    },
    [zoneAtPoint, sideHintAtPoint, zoneIds, update, entryOf, defs],
  )

  // 统一拖拽入口：grip（side 面板）与 floating 标题共用，跨 zone 放置。
  const startPanelDrag = useCallback(
    (id: string, e: ReactPointerEvent<HTMLElement>) => {
      if (e.button !== 0) return
      e.preventDefault()
      const handle = e.currentTarget
      const cur = stateRef.current[id] ?? entryOf(id)
      const layer = layerRectOf(layerElRef.current)
      const floating = cur.loc.zone === 'floating'
      const w = cur.loc.w ?? 320
      const h = cur.loc.h ?? 280
      try {
        handle.setPointerCapture(e.pointerId)
      } catch {
        /* pointer capture unsupported (jsdom) */
      }
      setDrag({
        kind: 'panel',
        id,
        pointer: { x: e.clientX, y: e.clientY },
        startX: e.clientX,
        startY: e.clientY,
        started: false,
        grabOffset: floating
          ? { x: e.clientX - layer.left - (cur.loc.x ?? 0), y: e.clientY - layer.top - (cur.loc.y ?? 0) }
          : { x: w / 2, y: h / 2 },
        originW: w,
        originH: h,
        layer,
      })
      // 拖拽期间全局样式：禁止文本选中 + 抓取手势（防止指针抖动时选中文字/光标闪烁）。
      const prevCursor = document.body.style.cursor
      const prevUserSelect = document.body.style.userSelect
      document.body.style.cursor = 'grabbing'
      document.body.style.userSelect = 'none'

      const detach = () => {
        handle.removeEventListener('pointermove', onMove)
        handle.removeEventListener('pointerup', onUp)
        handle.removeEventListener('pointercancel', onCancel)
        window.removeEventListener('keydown', onKey, true)
        // 恢复全局样式。
        document.body.style.cursor = prevCursor
        document.body.style.userSelect = prevUserSelect
      }
      const onMove = (ev: PointerEvent) => {
        const d = dragRef.current
        if (!d || d.kind !== 'panel' || d.id !== id) return
        const started = d.started
          || Math.abs(ev.clientX - d.startX) > DRAG_THRESHOLD
          || Math.abs(ev.clientY - d.startY) > DRAG_THRESHOLD
        setDrag({ ...d, pointer: { x: ev.clientX, y: ev.clientY }, started })
        if (!started) return
        const zone = zoneAtPoint(ev.clientX, ev.clientY)
        setActiveZone(zone)
        setDropHint(zone === 'side' ? sideHintAtPoint(ev.clientX, ev.clientY) : null)
      }
      const onUp = (ev: PointerEvent) => {
        detach()
        const d = dragRef.current
        endDrag()
        // 阈值内松手 = 点击误触，零状态变更。
        if (!d || d.kind !== 'panel' || !d.started) return
        placeDropped(id, ev)
      }
      const onCancel = () => {
        detach()
        endDrag() // 取消：零状态变更
      }
      const onKey = (ev: KeyboardEvent) => {
        if (ev.key !== 'Escape') return
        detach()
        endDrag()
      }
      handle.addEventListener('pointermove', onMove)
      handle.addEventListener('pointerup', onUp)
      handle.addEventListener('pointercancel', onCancel)
      window.addEventListener('keydown', onKey, true)
    },
    [entryOf, endDrag, placeDropped, sideHintAtPoint, zoneAtPoint],
  )

  const onGripPointerDown = useCallback(
    (id: string) => (e: ReactPointerEvent<HTMLElement>) => startPanelDrag(id, e),
    [startPanelDrag],
  )

  const onTitlePointerDown = useCallback(
    (id: string) => (e: ReactPointerEvent<HTMLElement>) => startPanelDrag(id, e),
    [startPanelDrag],
  )

  /** floating 全方向 resize（四角+四边；move 中本地跟随，up 一次写入；Esc/pointercancel 恢复）。 */
  const onResizePointerDown = useCallback(
    (id: string) => (dir: ResizeDir, e: ReactPointerEvent<HTMLElement>) => {
      if (e.button !== 0) return
      e.preventDefault()
      e.stopPropagation()
      const handle = e.currentTarget
      const cur = stateRef.current[id] ?? entryOf(id)
      const layer = layerRectOf(layerElRef.current)
      // 起拖矩形（move 从 startRect + 指针总 delta 绝对计算——零增量累计误差）。
      const startRect = { x: cur.loc.x ?? 0, y: cur.loc.y ?? 0, w: cur.loc.w ?? 320, h: cur.loc.h ?? 280 }
      try {
        handle.setPointerCapture(e.pointerId)
      } catch {
        /* pointer capture unsupported (jsdom) */
      }
      setDrag({
        kind: 'resize', id, dir, startX: e.clientX, startY: e.clientY, startRect,
        curX: startRect.x, curY: startRect.y, curW: startRect.w, curH: startRect.h, layer,
      })

      const detach = () => {
        handle.removeEventListener('pointermove', onMove)
        handle.removeEventListener('pointerup', onUp)
        handle.removeEventListener('pointercancel', onCancel)
        window.removeEventListener('keydown', onKey, true)
      }
      const onMove = (ev: PointerEvent) => {
        const d = dragRef.current
        if (!d || d.kind !== 'resize' || d.id !== id) return
        const dx = ev.clientX - d.startX
        const dy = ev.clientY - d.startY
        const r = d.startRect
        // 方向分量（dir 子串匹配方向字母：'ne' 含 n+e）。
        const we = d.dir.includes('e')
        const ws = d.dir.includes('s')
        const ww = d.dir.includes('w')
        const wn = d.dir.includes('n')
        // 尺寸期望值：e/s 拖大右侧/下侧；w/n 拖大左侧/上侧（右/下边缘固定）。
        let w = r.w + (we ? dx : 0) - (ww ? dx : 0)
        let h = r.h + (ws ? dy : 0) - (wn ? dy : 0)
        // clamp 下限 MIN_W/MIN_H；上限按方向——e/s 不越浮层右/下缘（r.x/r.y 起），
        // w/n 不越过初始左/上缘（右/下边缘 r.x+r.w 为极限：x=0 时 w 最大）。
        w = Math.min(Math.max(MIN_W, w), Math.max(MIN_W, ww ? r.x + r.w : d.layer.width - r.x))
        h = Math.min(Math.max(MIN_H, h), Math.max(MIN_H, wn ? r.y + r.h : d.layer.height - r.y))
        // 位置联动：w/n 方向拖动时对侧边缘固定（clamp 保证 x/y ≥ 0——w 上限即右缘，
        // x = 右缘 - w；w 收到 MIN_W 时 x 最大 = 右缘 - MIN_W）。
        const x = ww ? r.x + r.w - w : r.x
        const y = wn ? r.y + r.h - h : r.y
        setDrag({ ...d, curX: x, curY: y, curW: w, curH: h })
      }
      const onUp = () => {
        detach()
        const d = dragRef.current
        endDrag()
        if (!d || d.kind !== 'resize') return
        update((prev) => {
          const e2 = prev[id] ?? entryOf(id)
          return { ...prev, [id]: { ...e2, loc: { ...e2.loc, x: d.curX, y: d.curY, w: d.curW, h: d.curH } } }
        })
      }
      const onCancel = () => {
        detach()
        endDrag() // 取消：尺寸/位置不落盘（零状态变更）
      }
      const onKey = (ev: KeyboardEvent) => {
        if (ev.key !== 'Escape') return
        detach()
        endDrag()
      }
      handle.addEventListener('pointermove', onMove)
      handle.addEventListener('pointerup', onUp)
      handle.addEventListener('pointercancel', onCancel)
      window.addEventListener('keydown', onKey, true)
    },
    [entryOf, endDrag, update],
  )

  /**
   * v5.1 side 面板底边调高（复用 v5 拖拽协议：move 中零持久化本地跟随，
   * pointerup 一次 update+persist；pointer capture；handle touch-none）。
   * clamp 140–640；Esc/pointercancel 零状态变更。起始 h：loc.h ?? 钉选默认
   * （PINNED_DEFAULTS）?? 自适应上限（DOCK_BODY_MAX_H）。
   */
  const onHeightPointerDown = useCallback(
    (id: string) => (e: ReactPointerEvent<HTMLElement>) => {
      if (e.button !== 0) return
      e.preventDefault()
      e.stopPropagation()
      const handle = e.currentTarget
      const cur = stateRef.current[id] ?? entryOf(id)
      const startH = cur.loc.h ?? PINNED_DEFAULTS[id]?.h ?? DOCK_BODY_MAX_H
      try {
        handle.setPointerCapture(e.pointerId)
      } catch {
        /* pointer capture unsupported (jsdom) */
      }
      setDrag({ kind: 'height', id, startX: e.clientX, startY: e.clientY, curH: startH })

      const detach = () => {
        handle.removeEventListener('pointermove', onMove)
        handle.removeEventListener('pointerup', onUp)
        handle.removeEventListener('pointercancel', onCancel)
        window.removeEventListener('keydown', onKey, true)
      }
      const onMove = (ev: PointerEvent) => {
        const d = dragRef.current
        if (!d || d.kind !== 'height' || d.id !== id) return
        const h = Math.min(Math.max(DOCK_H_MIN, d.curH + (ev.clientY - d.startY)), DOCK_H_MAX)
        setDrag({ ...d, startX: ev.clientX, startY: ev.clientY, curH: h })
      }
      const onUp = () => {
        detach()
        const d = dragRef.current
        endDrag()
        if (!d || d.kind !== 'height') return
        update((prev) => {
          const e2 = prev[id] ?? entryOf(id)
          return { ...prev, [id]: { ...e2, loc: { ...e2.loc, h: d.curH } } }
        })
      }
      const onCancel = () => {
        detach()
        endDrag() // 取消：高度不落盘（零状态变更）
      }
      const onKey = (ev: KeyboardEvent) => {
        if (ev.key !== 'Escape') return
        detach()
        endDrag()
      }
      handle.addEventListener('pointermove', onMove)
      handle.addEventListener('pointerup', onUp)
      handle.addEventListener('pointercancel', onCancel)
      window.addEventListener('keydown', onKey, true)
    },
    [entryOf, endDrag, update],
  )

  // openPanel 入口（RightSidebarControlContext / AgentPanel onOpenTasks）：
  // 面板展开（collapsed=false）。side 面板展开；floating 面板展开；chip 面板
  // pin 到 side（展开可见，不弹浮窗）。
  useEffect(() => {
    const handler = (e: Event) => {
      const id = (e as CustomEvent<{ id?: string }>).detail?.id
      if (!id || !panelRegistry.getPanel(id)) return
      const cur = stateRef.current[id] ?? entryOf(id)
      if (cur.loc.zone === 'chip') {
        // chip 面板：pin 到 side 展开（不弹浮窗——v5.2 设计稿确认）。
        pinPanel(id)
        return
      }
      update((prev) => {
        const c = prev[id] ?? entryOf(id)
        if (!c.collapsed) return prev
        return { ...prev, [id]: { ...c, collapsed: false } }
      })
    }
    window.addEventListener('xbot:panel-request', handler)
    return () => window.removeEventListener('xbot:panel-request', handler)
  }, [update, entryOf, pinPanel])

  const value = useMemo<PanelDockContextValue>(
    () => ({
      tabManager,
      defs,
      entryOf,
      zoneIds,
      drag,
      activeZone,
      dropHint,
      dragSrcId: drag?.kind === 'panel' ? drag.id : null,
      dragPointer: drag?.kind === 'panel' && drag.started ? drag.pointer : null,
      toggleCollapse,
      floatPanel,
      dockPanel,
      pinPanel,
      unpinPanel,
      onGripPointerDown,
      onTitlePointerDown,
      onResizePointerDown,
      onHeightPointerDown,
      registerDockEl,
      registerLayerEl,
    }),
    // ⚠️ deps 必须含 state：entryOf 读 stateRef 引用稳定——collapse/拖拽落盘只改
    // state，若缺则 context value 永不重建（v4 已修，保持）。v5 另需 drag +
    // activeZone + dropHint（拖拽本地跟随渲染全靠 context 重建）。
    [tabManager, defs, entryOf, zoneIds, state, drag, activeZone, dropHint, toggleCollapse, floatPanel, dockPanel, pinPanel, unpinPanel, onGripPointerDown, onTitlePointerDown, onResizePointerDown, onHeightPointerDown, registerDockEl, registerLayerEl],
  )

  return <PanelDockContext.Provider value={value}>{children}</PanelDockContext.Provider>
}


/** zone 宿主 ring 高亮样式（activeZone 命中宿主根元素时）。 */
export function zoneHighlightStyle(active: boolean): CSSProperties | undefined {
  return active ? { outline: '1.5px dashed var(--accent)', outlineOffset: -4 } : undefined
}

/**
 * side 宿主（v5.1 钉选堆叠）：flex-col = 钉选堆叠区（flex-1 min-h-0
 * overflow-y-auto，面板自然高度堆叠，永不挤压——超高整栏滚动）+ SideChips
 * （shrink-0 固定底部启动器）。外层容器保持 data-panel-zone="side"（拖拽
 * 落点判定宿主）；SideChips 自带 data-panel-zone="chip"。
 */
export function PanelDock(): ReactNode {
  const dock = usePanelDock()
  const setDockEl = useCallback((el: HTMLDivElement | null) => dock.registerDockEl(el), [dock])
  const zoneActive = dock.activeZone === 'side'
  const sideIds = dock.zoneIds('side')
  return (
    <div
      ref={setDockEl}
      data-panel-zone="side"
      data-zone-active={zoneActive || undefined}
      className="flex min-h-0 flex-1 flex-col gap-1.5 overflow-hidden p-1.5"
      style={zoneHighlightStyle(zoneActive)}
    >
      <div data-testid="panel-dock-stack" className="flex min-h-0 flex-1 flex-col gap-1.5 overflow-hidden">
        {sideIds.map((id) => {
          const def = dock.defs.find((d) => d.id === id)
          if (!def) return null
          const entry = dock.entryOf(id)
          // 高度渲染跟随：调高拖拽中用本地 curH（零持久化），否则 loc.h ?? 钉选默认。
          const heightDrag = dock.drag && dock.drag.kind === 'height' && dock.drag.id === id ? dock.drag : null
          const h = heightDrag
            ? heightDrag.curH
            : (entry.loc.h != null ? entry.loc.h : (PINNED_DEFAULTS[id]?.h ?? PIN_DEFAULT_H))
          const isDropTarget = dock.dropHint?.targetId === id
          // flex 比例分配：面板按 flex-basis(h) 比例撑满堆叠区，无空白
          const flexBasis = entry.collapsed ? 'auto' : `${h}px`
          return (
            <PanelChrome
              key={id}
              id={id}
              icon={def.icon}
              title={def.title}
              badge={def.badges?.() ?? null}
              mode="docked"
              collapsed={entry.collapsed}
              onToggleCollapse={() => dock.toggleCollapse(id)}
              onToggleMode={() => dock.floatPanel(id)}
              onUnpin={PINNED_DEFAULTS[id] ? undefined : () => dock.unpinPanel(id)}
              onGripPointerDown={dock.onGripPointerDown(id)}
              isDragSource={dock.dragSrcId === id}
              dropIndicator={isDropTarget ? (dock.dropHint!.before ? 'before' : 'after') : null}
              emptyHint={def.emptyHint}
              onResizeHeightPointerDown={dock.onHeightPointerDown(id)}
              style={{ flex: entry.collapsed ? '0 0 auto' : `1 1 ${flexBasis}`, minHeight: 0 }}
            >
              {def.render({ tabManager: dock.tabManager })}
            </PanelChrome>
          )
        })}
        {sideIds.length === 0 ? (
          <div className="flex flex-1 items-center justify-center px-4 text-center text-[11px] text-text-muted">
            暂无钉选面板
          </div>
        ) : null}
      </div>
      <SideChips />
    </div>
  )
}

/** floating 宿主：窗口内浮层（AppShell 根容器内 absolute inset-0，非 body portal）。 */
export function FloatingLayer(): ReactNode {
  const dock = usePanelDock()
  const setLayerEl = useCallback((el: HTMLDivElement | null) => dock.registerLayerEl(el), [dock])
  const zoneActive = dock.activeZone === 'floating'
  return (
    <div
      ref={setLayerEl}
      data-panel-zone="floating"
      data-zone-active={zoneActive || undefined}
      className="pointer-events-none absolute inset-0 z-40"
      style={zoneHighlightStyle(zoneActive)}
    >
      {dock.zoneIds('floating').map((id) => {
        const def = dock.defs.find((d) => d.id === id)
        if (!def) return null
        const entry = dock.entryOf(id)
        const d = dock.drag
        // 拖动跟随：本地 drag state 渲染（零持久化），up 落盘。
        const follow = d && d.kind === 'panel' && d.id === id && d.started ? d : null
        // resize 跟随：本地 curX/curY/curW/curH 渲染（零持久化），up 落盘。
        // 左/上方向拖动时 x/y 联动（对侧边缘固定）。
        const resizing = d && d.kind === 'resize' && d.id === id ? d : null
        const x = follow
          ? Math.max(0, follow.pointer.x - follow.layer.left - follow.grabOffset.x)
          : resizing
            ? resizing.curX
            : (entry.loc.x ?? 0)
        const y = follow
          ? Math.max(0, follow.pointer.y - follow.layer.top - follow.grabOffset.y)
          : resizing
            ? resizing.curY
            : (entry.loc.y ?? 0)
        const style: CSSProperties = {
          left: x,
          top: y,
          width: follow ? follow.originW : (resizing ? resizing.curW : (entry.loc.w ?? 320)),
          ...(entry.collapsed
            ? { height: undefined }
            : follow
              ? { height: follow.originH }
              : resizing
                ? { height: resizing.curH }
                : { height: entry.loc.h ?? 280 }),
        }
        return (
          <PanelChrome
            key={id}
            id={id}
            icon={def.icon}
            title={def.title}
            badge={def.badges?.() ?? null}
            mode="floating"
            collapsed={entry.collapsed}
            onToggleCollapse={() => dock.toggleCollapse(id)}
            onToggleMode={() => dock.dockPanel(id)}
            onClose={() => {
              dock.dockPanel(id)
              if (!dock.entryOf(id).collapsed) dock.toggleCollapse(id)
            }}
            onTitlePointerDown={dock.onTitlePointerDown(id)}
            onTitleDoubleClick={() => dock.dockPanel(id)}
            onResizePointerDown={dock.onResizePointerDown(id)}
            emptyHint={def.emptyHint}
            style={style}
          >
            {def.render({ tabManager: dock.tabManager })}
          </PanelChrome>
        )
      })}
      <DragGhost />
    </div>
  )
}

/**
 * 拖拽 ghost（fixed 相对视口；pointer-events-none 不挡 elementFromPoint）。
 * 形态预告：activeZone floating → 完整面板预览；side/top/bottom → 徽章形态。
 */
function DragGhost(): ReactNode {
  const { drag, dragPointer, activeZone, defs } = usePanelDock()
  const def = drag?.kind === 'panel' && drag.started ? defs.find((p) => p.id === drag.id) : null
  if (!def || !dragPointer) return null
  const Icon = pluginIcon(def.icon)
  const isFullPreview = activeZone === 'floating'
  return (
    <div
      data-testid="panel-drag-ghost"
      data-ghost-mode={isFullPreview ? 'panel' : 'badge'}
      className="flex items-center gap-1.5 rounded-lg px-2 py-1.5 text-[11px] font-semibold"
      style={{
        position: 'fixed',
        zIndex: 50,
        // transform 替代 left/top——GPU 合成层，不触发布局回流，拖拽更顺滑。
        transform: `translate3d(${dragPointer.x + 8}px, ${dragPointer.y + 8}px, 0)`,
        // theme token（light/dark 自适应）：bg-primary 高不透明 + var(--border) 描边
        background: 'color-mix(in srgb, var(--bg-primary) 95%, transparent)',
        border: '1px solid var(--border)',
        boxShadow: '0 8px 24px rgba(0,0,0,0.4)',
        color: 'var(--text-primary)',
        pointerEvents: 'none',
        willChange: 'transform',
        ...(isFullPreview ? { width: 240, height: 160, alignItems: 'flex-start' } : {}),
      }}
    >
      {/* eslint-disable-next-line react-hooks/static-components -- pluginIcon
          返回 lucide 映射表中的稳定图标组件引用（无状态），规则误报。 */}
      <Icon className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
      {def.title}
    </div>
  )
}
