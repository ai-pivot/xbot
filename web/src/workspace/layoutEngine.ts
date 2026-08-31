/**
 * 集中式布局引擎（平铺式窗口管理器 master/stack 语义）。
 *
 * 布局计算是一条集中式管线：任何结构更改（卡片增删、tab 增删/移动）发生后、
 * paint 前，引擎统一重新计算布局呈现策略并应用。不存在"先渲染默认布局
 * 再事后修正"的路径。
 *
 * 两个呈现策略：
 * 1. 尺寸分配（master/stack）：
 *
 *   ┌────────┬──────────────────────┐
 *   │ sec 1  │                      │
 *   ├────────┤      master(s)       │
 *   │ sec 2  │    （80% 宽）         │
 *   ├────────┤                      │
 *   │ sec 3  │                      │
 *   └────────┴──────────────────────┘
 *    堆叠列 20% 宽，卡片上下排列（水平切分）
 *
 *   - master 卡片（含 agent tab 的 group）：root 层水平排列，合计占
 *     masterRatio（默认 80%）宽度，多张平分
 *   - secondary 卡片（sidebar panels）：在与 master 并列的堆叠列内上下
 *     排列，平分容器高度；堆叠列整体宽度由 master 设宽后的 delta 吸收
 *
 * 2. tab 栏可见性（单 tab 隐藏）：卡片只有 1 个 tab 时隐藏 tab 栏——
 *    平铺卡片语义下 header 只在多 tab 时有意义，单 tab 卡片隐藏 header
 *    释放垂直空间（group.model.header.hidden，dockview 原生路径，
 *    toJSON 自动持久化 hideHeader）。
 *
 * 触发管线（统一入口，不散落在 Manager 里）：
 * - `onDidAddGroup` / `onDidRemoveGroup`：卡片增删 → 立即 relayout
 *  （尺寸 + header 策略）。事件在 addPanel/removePanel 调用栈内同步触发
 *  （paint 前），计算结果与结构变更同帧落地
 * - `onDidAddPanel` / `onDidRemovePanel`：group 内 tab 增删/移动 → 只重算
 *   header 策略（不影响尺寸分配）
 * - `onDidLayoutChange`：容器尺寸就绪（初次播种时 `api.width === 0`，
 *   引擎保持 pending，等 autoResize 的 ResizeObserver 触发首次 layout）
 *   或结构变化未被处理时补偿重算；纯 sash 拖拽（结构未变）不覆盖用户
 *   手动调整的比例
 */
import type { DockviewApi, DockviewGroupPanel } from 'dockview-core'

// ── 配置 ─────────────────────────────────────────────────────────────────────

export interface LayoutEngineOptions {
  /** master 卡片（agent 主卡片）合计宽度占比，0-1 */
  masterRatio: number
  /** 堆叠列最小宽度（px，单张 secondary 直接占列时） */
  minSecondaryWidth: number
  /** 堆叠列内单张 secondary 卡片最小高度（px） */
  minSecondaryHeight: number
  /** master 卡片最小宽度（px） */
  minMasterWidth: number
}

export const DEFAULT_LAYOUT_OPTIONS: LayoutEngineOptions = {
  masterRatio: 0.8,
  minSecondaryWidth: 200,
  minSecondaryHeight: 120,
  minMasterWidth: 380,
}

// ── master 判定（唯一事实源，PanelManager 复用） ─────────────────────────────

/** group 是否为 master 卡片（含 agent tab 的 group） */
export function isMasterGroup(group: DockviewGroupPanel): boolean {
  return group.panels.some(
    (p) => (p.params as Record<string, unknown> | undefined)?.type === 'agent',
  )
}

// ── 纯计算 ────────────────────────────────────────────────────────────────────

export interface LayoutGroupInfo {
  id: string
  isMaster: boolean
}

/** 一张卡片的目标尺寸：root 层 master 设宽度，堆叠列内 secondary 设高度 */
export type SizeSpec = { width?: number; height?: number }

/**
 * 集中计算（master/stack 布局）：给定卡片集合与容器尺寸，输出每张卡片的
 * 目标尺寸（master → 宽度，secondary → 高度/单张时宽度）。
 *
 * 返回 null = 不干预（卡片少于 2 张、无 master、无 secondary、或容器尺寸
 * 未就绪）。堆叠列内最后一张 secondary 省略 —— 由它吸收 splitview 的舍入
 * 误差；master 全部设置（堆叠列/同级 branch 吸收 delta，总宽守恒）。
 */
export function computeMasterStack(
  groups: LayoutGroupInfo[],
  totalWidth: number,
  totalHeight: number,
  options: LayoutEngineOptions = DEFAULT_LAYOUT_OPTIONS,
): Map<string, SizeSpec> | null {
  if (groups.length < 2 || totalWidth <= 0 || totalHeight <= 0) return null
  const masters = groups.filter((g) => g.isMaster)
  const secondaries = groups.filter((g) => !g.isMaster)
  if (masters.length === 0 || secondaries.length === 0) return null

  // 堆叠列宽度：理想 = 1 - masterRatio，抬到下限（列太窄不可用），
  // 再被 master 保底封顶（master 拿剩余宽度，容器不足时列被压缩）。
  // Math.round 消除浮点误差（1200 × (1-0.8) = 239.999...）
  let stackWidth = Math.round(totalWidth * (1 - options.masterRatio))
  stackWidth = Math.max(stackWidth, options.minSecondaryWidth)
  const masterFloor = options.minMasterWidth * masters.length
  stackWidth = Math.min(stackWidth, totalWidth - masterFloor)
  if (stackWidth < 0) stackWidth = 0
  const masterEach = Math.floor((totalWidth - stackWidth) / masters.length)

  const plan = new Map<string, SizeSpec>()
  if (secondaries.length === 1) {
    // 单张 secondary 直接在 root 层（与 master 并列）：设列宽
    plan.set(secondaries[0].id, { width: stackWidth })
  } else {
    // 堆叠列内多张：每张设高度（均分容器高，最后一张省略吸收误差）
    const secEachH = Math.max(
      Math.floor(totalHeight / secondaries.length),
      options.minSecondaryHeight,
    )
    for (let i = 0; i < secondaries.length - 1; i++) {
      plan.set(secondaries[i].id, { height: secEachH })
    }
  }
  // master 全部设宽（root 层水平）；堆叠列整体宽度由 delta 吸收
  for (const m of masters) plan.set(m.id, { width: masterEach })
  return plan
}

// ── 引擎 ─────────────────────────────────────────────────────────────────────

/**
 * 集中式布局引擎实例。bindApi 订阅结构变化事件，所有布局更改统一走
 * relayout() 计算 → 应用管线。
 */
export class LayoutEngine {
  private api: DockviewApi | null = null
  private host: HTMLElement | null = null
  private disposables: Array<{ dispose(): void }> = []
  private appliedOnce = false
  private lastAppliedIds = ''
  private readonly options: LayoutEngineOptions
  /** 显式展开收起态的非主卡 group id 集合（默认收起——6px 细条把手） */
  private expandedHeaders = new Set<string>()
  private onHeaderClick: ((e: MouseEvent) => void) | null = null

  constructor(options: Partial<LayoutEngineOptions> = {}) {
    this.options = { ...DEFAULT_LAYOUT_OPTIONS, ...options }
  }

  /** 绑定/解绑 DockviewApi（DockviewContainer onReady / dispose 时调用） */
  bindApi(api: DockviewApi | null, host?: HTMLElement | null): void {
    this.teardown()
    this.expandedHeaders.clear()
    this.api = api
    this.host = host ?? null
    this.appliedOnce = false
    this.lastAppliedIds = ''
    if (!api) return
    // 结构变化 → 立即重算（事件在 addPanel/removePanel 调用栈内同步触发，paint 前）
    this.disposables.push(api.onDidAddGroup(() => this.relayout()))
    this.disposables.push(api.onDidRemoveGroup(() => this.relayout()))
    // group 内 tab 增删/移动 → 只重算 header 策略（不影响尺寸分配）
    this.disposables.push(api.onDidAddPanel(() => this.applyGroupHeaderPolicy()))
    this.disposables.push(api.onDidRemovePanel(() => this.applyGroupHeaderPolicy()))
    // 容器尺寸就绪（首次）/ 结构变化补偿；纯 sash 拖拽不覆盖用户手动比例
    this.disposables.push(
      api.onDidLayoutChange(() => {
        if (!this.appliedOnce || this.structureChanged()) this.relayout()
      }),
    )
    // header 点击委托（收起↔展开）：点击 tab 栏空白区（非 .dv-tab pill——
    // pill 点击是 tab 激活）切换非主卡 header 的收起态。委托在 host 上
    // （LayoutEngine 集中管理 header 策略的交互入口，不散落组件）。
    if (this.host) {
      this.onHeaderClick = (e: MouseEvent) => {
        const target = e.target as HTMLElement | null
        if (!target?.closest) return
        const headerEl = target.closest('.dv-tabs-and-actions-container') as HTMLElement | null
        if (!headerEl || target.closest('.dv-tab')) return
        const groupEl = headerEl.closest('.dv-groupview') as HTMLElement | null
        if (!groupEl) return
        const group = api.groups.find((g) => g.element === groupEl)
        if (group) this.toggleHeader(group.id)
      }
      this.host.addEventListener('click', this.onHeaderClick)
    }
    // bindApi 时布局可能已存在（恢复持久化布局 / 热重建）—— 主动尝试一次。
    // 宽度未就绪时无害（pending，等 onDidLayoutChange 重试）。
    this.relayout()
  }

  dispose(): void {
    this.teardown()
  }

  private teardown(): void {
    for (const d of this.disposables) d.dispose()
    this.disposables = []
    if (this.host && this.onHeaderClick) {
      this.host.removeEventListener('click', this.onHeaderClick)
    }
    this.onHeaderClick = null
  }

  /** 当前卡片集合签名是否与上次应用时不同（sash 拖拽不改变签名） */
  private structureChanged(): boolean {
    return this.groupSignature() !== this.lastAppliedIds
  }

  private groupSignature(): string {
    return this.api?.groups.map((g) => g.id).join(',') ?? ''
  }

  /**
   * 集中计算并应用布局（尺寸分配 + header 可见性策略）。
   *
   * 返回是否完成（false = 容器尺寸未就绪，引擎保持 pending，
   * 待 onDidLayoutChange 首次触发时重试）。
   */
  relayout(): boolean {
    const api = this.api
    if (!api) return false
    // maximize 状态下不干预（退出最大化由 dockview 自行恢复比例）
    if (typeof api.hasMaximizedGroup === 'function' && api.hasMaximizedGroup()) {
      return false
    }
    const groups = api.groups
    if (groups.length === 0) return false
    this.applyGroupHeaderPolicy()

    const totalWidth = api.width
    const totalHeight = api.height
    const infos: LayoutGroupInfo[] = groups.map((g) => ({
      id: g.id,
      isMaster: isMasterGroup(g),
    }))
    const plan = computeMasterStack(infos, totalWidth, totalHeight, this.options)

    if (plan) {
      const byId = new Map(groups.map((g) => [g.id, g]))
      for (const [id, size] of plan) {
        const group = byId.get(id)
        group?.api.setSize(size)
      }
    }
    // 尺寸未就绪（totalWidth/Height <= 0）→ 保持 pending；
    // 否则记录签名（含"不干预但已处理"的场景：<2 卡片、无 master 等）
    if (totalWidth > 0 && totalHeight > 0) {
      this.appliedOnce = true
      this.lastAppliedIds = this.groupSignature()
    }
    return totalWidth > 0 && totalHeight > 0
  }

  /**
   * header 可见性策略（卡片标题栏/把手——方案 A）：
   * - 所有卡片 header 显示（group.model.header.hidden = false）——header
   *   是卡片标题栏 + 拖动把手（触屏拖动的入口；主卡 tab 栏空白区兼把手）
   * - 主卡（含 agent tab 的 group）常驻全高（tab 功能区 + 连接状态绿点）
   * - 非主卡默认收起（6px 细条把手，CSS .dv-header-collapsed——点击展开
   *   显示卡片名；expandedHeaders 集合记录显式展开态）
   * group.model.header.hidden 是 dockview 官方路径（toJSON 持久化 hideHeader）；
   * 收起态走 group.element class（LayoutEngine 集中管理）。
   */
  private applyGroupHeaderPolicy(): void {
    const api = this.api
    if (!api) return
    for (const group of api.groups) {
      group.model.header.hidden = false
      const collapsed = !isMasterGroup(group) && !this.expandedHeaders.has(group.id)
      group.element.classList.toggle('dv-header-collapsed', collapsed)
    }
  }

  /**
   * 切换非主卡 header 的收起/展开（点击 header 空白区触发，见 bindApi 的
   * click 委托）。主卡 no-op（tab 栏是功能区，常驻全高不收起）。
   */
  toggleHeader(groupId: string): void {
    const api = this.api
    if (!api) return
    const group = api.groups.find((g) => g.id === groupId)
    if (!group || isMasterGroup(group)) return
    if (this.expandedHeaders.has(groupId)) this.expandedHeaders.delete(groupId)
    else this.expandedHeaders.add(groupId)
    group.element.classList.toggle('dv-header-collapsed', !this.expandedHeaders.has(groupId))
  }
}
