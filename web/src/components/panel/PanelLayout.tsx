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
import { useI18n } from '@/providers/i18n'

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

const LS_KEY_V2 = 'xbot:panel-layout-v2'
const LS_KEY_V1 = 'xbot:panel-layout'
/** 拖拽位移阈值：小于此值的 pointerup 视为误触（点击），零状态变更。 */

// ── v5.1 钉选堆叠常量（数据表驱动，零过程式特化）────────────────────────────

/** 钉选面板 body 高度拖拽 clamp 边界。 */
const DOCK_H_MIN = 140
const DOCK_H_MAX = 640
/** chip → side 钉选时的默认 body 高度。 */
const PIN_DEFAULT_H = 360
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
  // 插件面板：尊重 contribution 的 zone/segment，但 side 面板高度取「声明值」与
  // 「统一默认」的较大者——插件声明的 220 常常装不下内容（用户报告"git 面板高度
  // 太小、交互几乎不可用"）。总高超出时 panel-dock-stack 整栏滚动。
  if (def?.location) {
    const loc = def.location
    return {
      loc:
        loc.zone === 'side'
          ? { ...loc, h: Math.max(loc.h ?? PIN_DEFAULT_H, PIN_DEFAULT_H) }
          : loc,
      collapsed: true,
    }
  }
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
 * PINNED_DEFAULTS 面板（core.sessions）的**不变量**：永远 `{zone:'side', collapsed:false}`。
 *
 * 这些面板是左栏的常驻内容（`unpinPanel` 早已拒绝"取消钉选"）：一旦被折叠/浮窗/
 * 收进 chips，左栏就只剩空态提示 —— 用户看到的是"点一下会话面板没了"。状态层统一
 * 收敛（而不是各入口各写一遍 guard），持久化/跨设备同步过来的旧状态也会自愈。
 */
/**
 * 清理 rail 徽章面板被拖拽/钉选污染的 layout entry（2026-09-20）。
 *
 * 纯徽章面板（`badgeRender` 有值 + 声明 location.zone 为 top/bottom）没有面板
 * 主体，只能待在徽章 rail。历史上的拖拽/浮窗/钉选把它们的 entry 改成了
 * side/chip/floating ⇒ 它们会出现在 ActivityBar 里（用户报"runner 选择栏跑到
 * 侧边栏，点两下就过去"）。拖拽+浮窗已删除，这里把污染 entry 直接删掉（回落
 * def.location 声明值 = 徽章 rail）。
 */
export function sanitizeRailBadges(
  state: PanelLayoutState,
  defs: readonly PanelDefinition[],
): PanelLayoutState {
  let changed = false
  const next: PanelLayoutState = { ...state }
  for (const def of defs) {
    const zone = def.location?.zone
    if (def.badgeRender == null || (zone !== 'top' && zone !== 'bottom')) continue
    const entry = next[def.id]
    if (entry && entry.loc.zone !== zone) {
      // ⚠️ 必须【修正 zone】而不是 delete entry —— 删掉后 entryOf 会走
      // defaultEntryOf 兜底（collapsed:true 等默认值），rail 反而不渲染它
      // （用户 2026-09-20 报"runner 选择栏不见了"）。保留 entry 其余字段。
      next[def.id] = { ...entry, loc: { ...entry.loc, zone } }
      changed = true
    }
  }
  return changed ? next : state
}

export function enforcePinnedState(state: PanelLayoutState): PanelLayoutState {
  let changed = false
  const next: PanelLayoutState = { ...state }
  for (const [id, e] of Object.entries(state)) {
    const pinned = PINNED_DEFAULTS[id]
    if (!pinned) continue
    if (e.loc.zone === 'side' && !e.collapsed) continue
    next[id] = {
      collapsed: false,
      loc: {
        zone: 'side',
        order: e.loc.order,
        h: Math.max(DOCK_H_MIN, Math.min(DOCK_H_MAX, e.loc.h ?? pinned.h)),
      },
    }
    changed = true
  }
  return changed ? next : state
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

/**
 * v5.1 side 面板底边调高（move 中本地跟随，up 一次落盘；clamp 140–640）。
 * v6 成对分配：拖面板 i 时下一个【展开】面板等量反向补偿（总高恒定，拖拽有
 * 真实的"空间重新分配"反馈——用户报"拖拽不符合人类直觉"的根因修复）。
 * nextId=null 表示 i 是最后一个展开面板（下面无面板可补偿，只改自己）。
 */
interface HeightDragState {
  kind: 'height'
  id: string
  nextId: string | null
  startX: number
  startY: number
  curH: number
  nextCurH: number
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
  toggleCollapse: (id: string) => void
  /** 最小拖拽状态（只服务底边调高）。 */
  drag: DragState | null
  /**
   * 点击 chip 图标 = 该面板【独占左侧栏】（VSCode Activity Bar 模式）：
   * pin 到 side + 展开 + 其他 side 面板全部折叠 → 它占满左栏全高（grow）。
   * 再次点击已独占的面板 = 取消独占（折叠自己 + 展开会话列表）。
   * 取代旧的小浮层（340×440 空间局促、遮挡内容、点外部即消失）。
   */
  focusPanel: (id: string) => void
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
    const base = enforcePinnedState(loaded ? migrateV2Layout(loaded) : defaultPanelLayout(list))
    return sanitizeRailBadges(base, list)
  })
  const stateRef = useRef(state)
  stateRef.current = state

  // ⚠️ defs 变化（插件异步注册）时重跑 rail 徽章净化：初始化那一刻 defs 可能
  // 还不含插件的 rail badge（如 xbot.ssh-runner.bar）⇒ 它的被污染 entry
  // （zone side/chip，历史拖拽留下的）不会在初始化时被修正 ⇒ 面板既不在
  // bottom rail 也不该在侧栏（用户 2026-09-20 报「runner 选择栏不见了」）。

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
      if (loaded) setState((prev) => sanitizeRailBadges(enforcePinnedState(migrateV2Layout(loaded)), panelRegistry.listPanels()) ?? prev)
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

  /** rail 徽章净化：defs（插件异步注册）变化时，把被历史拖拽污染的 entry
   *  （zone side/chip/floating）修正回声明 zone（top/bottom）。
   *  ⚠️ 必须在 defs 变化时重跑：初始化那一刻插件的 rail badge 还没注册
   *  （xbot.ssh-runner.bar 只能由插件 activate() 的 ctx.panels.register 提供）
   *  ⇒ 只做初始化净化会让它永远停在污染的 side（用户 2026-09-20 报
   *  「runner 选择栏不见了」）。走 update 以持久化，避免每次刷新重复修正。 */
  useEffect(() => {
    setState((prev) => {
      const next = sanitizeRailBadges(prev, defs)
      // 无变更 ⇒ 返回原引用（零持久化，保持「未交互不写 v2」契约）；
      // 有变更 ⇒ 手动 persist（不能走 update —— 它无条件写盘）。
      if (next === prev) return prev
      persist(next)
      return next
    })
  }, [defs])

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
      // ⛔ PINNED_DEFAULTS（core.sessions）不可折叠：它是左栏的常驻内容，收起后
      // 整个面板（含自己的 header）从堆叠里消失、左栏只剩空态提示 —— 用户看到
      // 的就是"点一下会话面板没了"（2026-09-15：「sessions 这一行还有一个有完全
      // 一样的 bug 的按钮」）。整栏收起请用左侧图标栏点激活项 / 边缘把手。
      if (PINNED_DEFAULTS[id]) return
      update((prev) => {
        const cur = prev[id] ?? entryOf(id)
        return { ...prev, [id]: { ...cur, collapsed: !cur.collapsed } }
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

  /**
   * 点击 chip/ActivityBar 图标 = 该面板【独占左侧栏】（VSCode Activity Bar 模型）。
   * 独占 = 目标 pin 到 side + 展开，其他 side 面板全部折叠 → 它占满左栏全高。
   * ⚠️ 不做"再次点击折叠自己"——那会让侧栏变成 329px 的空白区（用户报"布局有问题"）。
   * 收起侧栏由 ActivityBar 层面处理（点已激活图标 → 收起整栏）。
   */
  const focusPanel = useCallback(
    (id: string) => {
      update((prev) => {
        const next = { ...prev }
        const entryOfId = (pid: string) => next[pid] ?? entryOf(pid)
        const sideIds = defs.filter((d) => entryOfId(d.id).loc.zone === 'side').map((d) => d.id)
        const cur = entryOfId(id)
        // 独占：其他 side 面板折叠，目标 pin 到 side + 展开。
        for (const pid of sideIds) {
          if (pid === id) continue
          const e = entryOfId(pid)
          if (!e.collapsed) next[pid] = { ...e, collapsed: true }
        }
        const maxOrder = sideIds.reduce((m, pid) => Math.max(m, entryOfId(pid).loc.order), -1)
        const { x: _x, y: _y, w: _w, segment: _s, ...rest } = cur.loc
        next[id] = {
          ...cur,
          loc: {
            ...rest,
            zone: 'side',
            order: cur.loc.zone === 'side' ? cur.loc.order : maxOrder + 1,
            h: cur.loc.h ?? PIN_DEFAULT_H,
          },
          collapsed: false,
        }
        return next
      })
    },
    [update, entryOf, defs],
  )





  // ── 拖拽状态（move 中零持久化；up 才 update+persist 一次——修 v4 bug 1）──
  const dockElRef = useRef<HTMLElement | null>(null)
  const layerElRef = useRef<HTMLElement | null>(null)
  const registerDockEl = useCallback((el: HTMLElement | null) => { dockElRef.current = el }, [])
  const registerLayerEl = useCallback((el: HTMLElement | null) => { layerElRef.current = el }, [])



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

  /** 最小拖拽状态（只服务 side 面板底边调高；拖拽移动/浮窗已删 2026-09-20）。 */
  const [drag, setDrag] = useState<DragState | null>(null)
  const dragRef = useRef<DragState | null>(null)
  dragRef.current = drag
  const endDrag = useCallback(() => setDrag(null), [])

  const onHeightPointerDown = useCallback(
    (id: string) => (e: ReactPointerEvent<HTMLElement>) => {
      if (e.button !== 0) return
      e.preventDefault()
      e.stopPropagation()
      const handle = e.currentTarget
      const sideIds = zoneIds('side')
      const idx = sideIds.indexOf(id)
      const nextId =
        sideIds.slice(idx + 1).find((pid) => !(stateRef.current[pid] ?? entryOf(pid)).collapsed) ?? null
      const el = document.querySelector<HTMLElement>(`[data-panel-id="${id}"]`)
      const startH =
        el?.getBoundingClientRect().height || (stateRef.current[id] ?? entryOf(id)).loc.h || PIN_DEFAULT_H
      const nextEl = nextId ? document.querySelector<HTMLElement>(`[data-panel-id="${nextId}"]`) : null
      const nextStartH = nextEl?.getBoundingClientRect().height || 0
      // 补偿面板过小（≤ MIN，或 jsdom 无布局高度 0）时无法再让出空间 → 不补偿
      // （只改自己），否则"回推"会让拖拽变成零位移。
      const compensateId = nextId && nextStartH > DOCK_H_MIN ? nextId : null
      try {
        handle.setPointerCapture(e.pointerId)
      } catch {
        /* pointer capture unsupported (jsdom) */
      }
      setDrag({ kind: 'height', id, nextId: compensateId, startX: e.clientX, startY: e.clientY, curH: startH, nextCurH: nextStartH })

      const detach = () => {
        handle.removeEventListener('pointermove', onMove)
        handle.removeEventListener('pointerup', onUp)
        handle.removeEventListener('pointercancel', onCancel)
        window.removeEventListener('keydown', onKey, true)
      }
      const onMove = (ev: PointerEvent) => {
        const d = dragRef.current
        if (!d || d.kind !== 'height' || d.id !== id) return
        let h = Math.min(Math.max(DOCK_H_MIN, startH + (ev.clientY - e.clientY)), DOCK_H_MAX)
        let nextH = nextStartH
        if (compensateId) {
          // 补偿面板等量反向；触底时回推被拖面板，保证两者都在 [MIN, MAX] 内。
          nextH = Math.min(Math.max(DOCK_H_MIN, nextStartH - (h - startH)), DOCK_H_MAX)
          h = startH + (nextStartH - nextH)
        }
        setDrag({ ...d, curH: h, nextCurH: nextH })
      }
      const onUp = () => {
        detach()
        const d = dragRef.current
        endDrag()
        if (!d || d.kind !== 'height') return
        update((prev) => {
          const next = { ...prev }
          const e2 = next[id] ?? entryOf(id)
          next[id] = { ...e2, loc: { ...e2.loc, h: d.curH } }
          if (d.nextId) {
            const e3 = next[d.nextId] ?? entryOf(d.nextId)
            next[d.nextId] = { ...e3, loc: { ...e3.loc, h: d.nextCurH } }
          }
          return next
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
    [entryOf, endDrag, update, zoneIds],
  )

  const value = useMemo<PanelDockContextValue>(
    () => ({
      tabManager,
      defs,
      entryOf,
      zoneIds,
      drag,
      toggleCollapse,
      focusPanel,
      onHeightPointerDown,
      registerDockEl,
      registerLayerEl,
    }),
    // ⚠️ deps 必须含 state：entryOf 读 stateRef 引用稳定——collapse/拖拽落盘只改
    // state，若缺则 context value 永不重建（v4 已修，保持）。v5 另需 drag +
    // activeZone + dropHint（拖拽本地跟随渲染全靠 context 重建）。
    [tabManager, defs, entryOf, zoneIds, state, drag, toggleCollapse, focusPanel, onHeightPointerDown, registerDockEl, registerLayerEl],
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
  const { t } = useI18n()
  const dock = usePanelDock()
  const setDockEl = useCallback((el: HTMLDivElement | null) => dock.registerDockEl(el), [dock])
  const zoneActive = false
  const sideIds = dock.zoneIds('side')
  // ⚠️ 空间分配模型（VSCode 式 + 消灭底部空白）：
  // 最后一个【展开】面板 flex-1 吸收剩余空间——旧模型所有面板 `flex: 0 0 h`
  // （永不 grow）时，折叠面板多则总高远小于容器，底部留大片空白（用户："折叠的
  // 部分多，都占不满区域好丑"；E2E 实测 664 vs 960 → 空白 296px）。
  // grow 面板自己的 handle 隐藏（它已弹性，拖它无意义——VSCode 的最后一个
  // section 下方也没有分隔条）；要调它就拖它上面那个面板的 handle（成对分配会
  // 让它自动补偿）。
  const lastExpandedId = [...sideIds].reverse().find((pid) => !dock.entryOf(pid).collapsed)
  // 可见（展开）面板。⚠️ 空态判定必须用【可见】而非【存在】：折叠掉最后一个展开
  // 面板（或把它升为浮窗）时，旧条件 sideIds.length === 0 为假 ⇒ 既没有面板也没有
  // 提示 ⇒ 左栏变成一片黑的"坏掉"观感（2026-09-15 用户：「点 Sessions 这个词有bug，
  // 点完这样」）。空态提示必须给出，让用户知道面板去哪了、从哪拿回来。
  const visibleSideIds = sideIds.filter((pid) => !dock.entryOf(pid).collapsed)
  return (
    <div
      ref={setDockEl}
      data-panel-zone="side"
      data-zone-active={zoneActive || undefined}
      className="flex min-h-0 flex-1 flex-col overflow-hidden"
      style={zoneHighlightStyle(zoneActive)}
    >
      {/* ⚠️ 只渲染【展开】的面板——入口全在 ActivityBar（最左边缘垂直图标列）。
          旧模型把折叠面板的 header 也堆在侧栏底部（"统计/插件/技能/Git" 四行），
          与新 ActivityBar 的图标功能重复、视觉杂乱（VSCode 的侧栏只显示当前 view）。 */}
      <div data-testid="panel-dock-stack" className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain">
        {visibleSideIds.map((id) => {
          const def = dock.defs.find((d) => d.id === id)
          if (!def) return null
          const entry = dock.entryOf(id)
          // 高度渲染跟随：被拖面板用 curH、补偿面板用 nextCurH（零持久化），
          // 否则 loc.h ?? 钉选默认。
          const heightDrag = dock.drag?.kind === 'height' ? dock.drag : null
          const h =
            heightDrag?.id === id
              ? heightDrag.curH
              : heightDrag?.nextId === id
                ? heightDrag.nextCurH
                : entry.loc.h != null
                  ? entry.loc.h
                  : (PINNED_DEFAULTS[id]?.h ?? PIN_DEFAULT_H)
          // flex 比例分配：面板按 flex-basis(h) 比例撑满堆叠区，无空白
          const flexBasis = entry.collapsed ? 'auto' : `${h}px`
          return (
            <PanelChrome
              key={id}
              id={id}
              icon={def.icon}
              title={def.labelKey ? t(def.labelKey) : def.title}
              badge={def.badges?.() ?? null}
              mode="docked"
              collapsed={entry.collapsed}
              pinned={PINNED_DEFAULTS[id] !== undefined}
              onToggleCollapse={() => dock.toggleCollapse(id)}
              emptyHint={def.emptyHint}
              headerExtra={def.headerExtra ? def.headerExtra({ tabManager: dock.tabManager }) : undefined}
              onResizeHeightPointerDown={id === lastExpandedId ? undefined : dock.onHeightPointerDown(id)}
              style={{
                // ⚠️ 空间分配模型（VSCode 式）：
                // - 折叠：0 0 auto（只有 header 高）
                // - 最后一个展开面板：1 0 <h>px —— grow=1 吸收剩余空间（消灭"折叠多
                //   时底部大片空白"），**shrink=0 空间不足时绝不压缩**（旧写法 1 1 0
                //   + minHeight 会在面板总高超出容器时把面板压到 140px → 用户报
                //   "左侧边栏挤死了，展开 git 里面东西都无法交互"）。总高超出时由
                //   容器滚动承接，每个面板保持自己的高度。
                // - 其他展开面板：0 0 hpx（保持自己高度，绝不被兄弟压缩）
                // 拖拽走成对分配（见 onHeightPointerDown）：被拖面板变高 → 下一个
                // 展开面板等量变矮 → 总高恒定，拖拽有真实的重新分配反馈。
                flex: entry.collapsed ? '0 0 auto' : id === lastExpandedId ? `1 0 ${flexBasis}` : `0 0 ${flexBasis}`,
                minHeight: 0,
              }}
            >
              {def.render({ tabManager: dock.tabManager })}
            </PanelChrome>
          )
        })}
        {visibleSideIds.length === 0 ? (
          <div className="flex flex-1 items-center justify-center px-4 text-center text-[11px] text-text-muted">
            {t('panel.noPinned')}
          </div>
        ) : null}
      </div>
    </div>
  )
}

