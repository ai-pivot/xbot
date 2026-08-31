/**
 * 集中式布局引擎（平铺式窗口管理器语义，master/stack 布局）。
 *
 * 布局计算是一条集中式管线：任何结构更改（卡片增删、tab 拆分/合并）发生后、
 * paint 前，引擎按卡片优先级统一重新计算所有卡片的尺寸，再把计算结果应用到
 * gridview。不存在"先渲染默认布局再事后 setSize 修正"的路径。
 *
 * 规则：
 * - master 卡片（含 agent tab 的 group）合计占 masterRatio（默认 80%）宽度，
 *   多个 master（用户拖拽 agent tab 拆分出的卡片）平分该区域
 * - secondary 卡片（sidebar panels）平分剩余宽度
 * - 两类卡片都有最小宽度下限；容器不足时 secondary 先压缩到下限
 *
 * 触发管线（统一入口，不散落在 Manager 里）：
 * - `onDidAddGroup` / `onDidRemoveGroup`：结构变化。这两个事件在
 *   addPanel/removePanel/moveTo 调用栈内同步触发（paint 前），重算结果
 *   与结构变更在同一次 DOM 更新里落地，用户看不到中间态
 * - `onDidLayoutChange`：容器尺寸就绪（初次播种时 `api.width === 0`，
 *   引擎保持 pending，等 autoResize 的 ResizeObserver 触发首次 layout）
 *   或结构变化未被处理时补偿重算；纯 sash 拖拽（结构未变）不覆盖用户
 *   手动调整的比例
 *
 * 误差吸收：splitview 的 view size 是绝对像素，`setSize` 的 delta 由同级
 * view 吸收。计划中省略最后一个 group，由它吸收舍入误差。
 */
import type { DockviewApi, DockviewGroupPanel } from 'dockview-core'

// ── 配置 ─────────────────────────────────────────────────────────────────────

export interface LayoutEngineOptions {
  /** master 卡片（agent 主卡片）合计宽度占比，0-1 */
  masterRatio: number
  /** secondary 卡片最小宽度（px） */
  minSecondaryWidth: number
  /** master 卡片最小宽度（px） */
  minMasterWidth: number
}

export const DEFAULT_LAYOUT_OPTIONS: LayoutEngineOptions = {
  masterRatio: 0.8,
  minSecondaryWidth: 200,
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

/**
 * 集中计算：给定卡片集合与容器宽度，输出每个卡片的目标宽度。
 *
 * 返回 null = 不干预（卡片少于 2 个、无 master、无 secondary、或容器宽度
 * 未就绪）。结果中省略最后一个卡片（由它吸收 splitview 的舍入误差）。
 */
export function computeWidths(
  groups: LayoutGroupInfo[],
  totalWidth: number,
  options: LayoutEngineOptions = DEFAULT_LAYOUT_OPTIONS,
): Map<string, number> | null {
  if (groups.length < 2 || totalWidth <= 0) return null
  const masters = groups.filter((g) => g.isMaster)
  const secondaries = groups.filter((g) => !g.isMaster)
  if (masters.length === 0 || secondaries.length === 0) return null

  // secondary 理想宽度：剩余区域均分，且不低于下限。
  // Math.round 消除浮点误差（1200 × (1-0.8) = 239.999...）
  const secBudget = Math.round(totalWidth * (1 - options.masterRatio))
  let secEach = Math.floor(secBudget / secondaries.length)
  secEach = Math.max(secEach, options.minSecondaryWidth)

  // master 保底：master 合计不得低于 minMasterWidth × 数量，必要时压缩 secondary
  const masterFloor = options.minMasterWidth * masters.length
  if (totalWidth - secEach * secondaries.length < masterFloor) {
    const secTotal = Math.max(
      totalWidth - masterFloor,
      options.minSecondaryWidth * secondaries.length,
    )
    secEach = Math.floor(secTotal / secondaries.length)
  }

  const masterEach = Math.floor((totalWidth - secEach * secondaries.length) / masters.length)

  const plan = new Map<string, number>()
  for (const g of secondaries) plan.set(g.id, secEach)
  for (const g of masters) plan.set(g.id, masterEach)
  // 最后一个卡片省略：splitview resizeView 的 delta 由同级吸收，
  // 留一个不设值可让所有已设值精确落地、余数归到它
  plan.delete(groups[groups.length - 1].id)
  return plan
}

// ── 引擎 ─────────────────────────────────────────────────────────────────────

/**
 * 集中式布局引擎实例。bindApi 订阅结构变化事件，所有布局更改统一走
 * relayout() 计算 → 应用管线。
 */
export class LayoutEngine {
  private api: DockviewApi | null = null
  private disposables: Array<{ dispose(): void }> = []
  private appliedOnce = false
  private lastAppliedIds = ''
  private readonly options: LayoutEngineOptions

  constructor(options: Partial<LayoutEngineOptions> = {}) {
    this.options = { ...DEFAULT_LAYOUT_OPTIONS, ...options }
  }

  /** 绑定/解绑 DockviewApi（DockviewContainer onReady / dispose 时调用） */
  bindApi(api: DockviewApi | null): void {
    this.teardown()
    this.api = api
    this.appliedOnce = false
    this.lastAppliedIds = ''
    if (!api) return
    // 结构变化 → 立即重算（事件在 addPanel/removePanel 调用栈内同步触发，paint 前）
    this.disposables.push(api.onDidAddGroup(() => this.relayout()))
    this.disposables.push(api.onDidRemoveGroup(() => this.relayout()))
    // 容器尺寸就绪（首次）/ 结构变化补偿；纯 sash 拖拽不覆盖用户手动比例
    this.disposables.push(
      api.onDidLayoutChange(() => {
        if (!this.appliedOnce || this.structureChanged()) this.relayout()
      }),
    )
    // bindApi 时布局可能已存在（恢复持久化布局 / 热重建）—— 主动尝试一次。
    // width 未就绪时无害（pending，等 onDidLayoutChange 重试）。
    this.relayout()
  }

  dispose(): void {
    this.teardown()
  }

  private teardown(): void {
    for (const d of this.disposables) d.dispose()
    this.disposables = []
  }

  /** 当前卡片集合签名是否与上次应用时不同（sash 拖拽不改变签名） */
  private structureChanged(): boolean {
    return this.groupSignature() !== this.lastAppliedIds
  }

  private groupSignature(): string {
    return this.api?.groups.map((g) => g.id).join(',') ?? ''
  }

  /**
   * 集中计算并应用布局。
   *
   * 返回是否完成（false = 容器宽度未就绪，引擎保持 pending，
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

    const totalWidth = api.width
    const infos: LayoutGroupInfo[] = groups.map((g) => ({
      id: g.id,
      isMaster: isMasterGroup(g),
    }))
    const plan = computeWidths(infos, totalWidth, this.options)

    if (plan) {
      const byId = new Map(groups.map((g) => [g.id, g]))
      for (const [id, width] of plan) {
        const group = byId.get(id)
        group?.api.setSize({ width })
      }
    }
    // 宽度未就绪（computeWidths 返回 null 且 totalWidth<=0）→ 保持 pending；
    // 否则记录签名（含"不干预但已处理"的场景：<2 卡片、无 master 等）
    if (totalWidth > 0) {
      this.appliedOnce = true
      this.lastAppliedIds = this.groupSignature()
    }
    return totalWidth > 0
  }
}
