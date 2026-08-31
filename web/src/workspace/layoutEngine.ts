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

/**
 * group 是否为 Tab 卡（卡片分类制）：panels[0].params.type ≠ 'panel'——
 * TabManager.openTab 创建的 tab 类型卡片（agent/file/terminal/background/
 * plugin/diff），tab 栏常驻（Header 融合）+ Tab 可互相拖入。
 * 与之相对：type = 'panel'（PanelManager.addPanel 创建的 sidebar 卡：
 * 会话/文件/搜索等）无 tab 栏组件，locked 禁止 Tab 拖入。
 * 主卡（isMasterGroup，含 agent tab）是 Tab 卡的子集（用于 master/stack
 * 尺寸计算，与 header 分类正交）。
 */
export function isTabGroup(group: DockviewGroupPanel): boolean {
  const first = group.panels[0]?.params as Record<string, unknown> | undefined
  return first?.type !== 'panel'
}

/**
 * 显式拖动把手图标（GripVertical 风格：两列 6 点阵）。原生 DOM 注入
 * （buildDragHandle），不经 React——group 生命周期由 dockview 管理。
 */
const GRIP_SVG =
  '<svg width="10" height="14" viewBox="0 0 10 14" fill="currentColor" aria-hidden="true">' +
  '<circle cx="3" cy="2" r="1.3"/><circle cx="7" cy="2" r="1.3"/>' +
  '<circle cx="3" cy="7" r="1.3"/><circle cx="7" cy="7" r="1.3"/>' +
  '<circle cx="3" cy="12" r="1.3"/><circle cx="7" cy="12" r="1.3"/></svg>'

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
    // group 内 tab 增删/移动 → 只重算 header 策略（不影响尺寸分配）
    this.disposables.push(api.onDidAddPanel(() => this.applyGroupHeaderPolicy()))
    this.disposables.push(api.onDidRemovePanel(() => this.applyGroupHeaderPolicy()))
    // 容器尺寸就绪（首次）/ 结构变化补偿；纯 sash 拖拽不覆盖用户手动比例
    this.disposables.push(
      api.onDidLayoutChange(() => {
        if (!this.appliedOnce || this.structureChanged()) this.relayout()
      }),
    )
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
   * header 可见性策略（卡片分类制）+ 显式拖动把手注入：
   * - Tab 卡（isTabGroup：panels[0].params.type ≠ 'panel'，如主卡 agent/
   *   file/terminal 等 tab 类型卡片）：tab 栏常驻显示（Header 与 Tab 列表
   *   融合），Tab 可互相拖入（locked = false）+ **tab 栏左端显式 grip 把手**
   * - 非 Tab 卡（type = 'panel'，PanelManager.addPanel 创建的 sidebar 卡：
   *   会话/文件/搜索等）：无 tab 栏组件（hidden = true，功能条由面板内容
   *   自带——如会话卡的渠道/分组下拉就在内容顶部，即视觉上的卡片 Header），
   *   locked = 'no-drop-target' 禁止 Tab 拖入 + **顶部 24px 显式把手条**
   *     （grip + 拖动区——用户要求把手可见不隐藏；原生 DOM 注入
   *   group.element 顶部，LayoutEngine 集中管理，幂等）
   * group.model.header.hidden 是 dockview 官方路径（toJSON 持久化 hideHeader）。
   */
  private applyGroupHeaderPolicy(): void {
    const api = this.api
    if (!api) return
    for (const group of api.groups) {
      const tabGroup = isTabGroup(group)
      group.model.header.hidden = !tabGroup
      group.locked = tabGroup ? false : 'no-drop-target'
      this.ensureDragHandle(group, tabGroup)
    }
  }

  /**
   * 显式拖动把手（用户要求：可见把手，不再依赖隐藏的空白区/Ctrl）：
   * - Tab 卡：tab 栏（.dv-tabs-and-actions-container）左端 grip 图标
   * - 非 Tab 卡：group.element 顶部 24px 把手条（grip + 空白拖动区）
   * 原生 DOM 注入（不走 React——group 生命周期由 dockview 管理，DOM 随
   * element 移除；LayoutEngine 幂等注入，分类切换时清理旧形态）。
   * isHeaderGrab 识别 .card-drag-handle（无 Ctrl 直接拖动，触屏可用）。
   */
  private ensureDragHandle(group: DockviewGroupPanel, tabGroup: boolean): void {
    const el = group.element
    if (tabGroup) {
      // 非 Tab 卡残留的顶部把手条清理（分类切换场景）
      el.querySelector(':scope > .card-drag-handle')?.remove()
      const tabs = el.querySelector<HTMLElement>(':scope > .dv-tabs-and-actions-container')
      if (tabs && !tabs.querySelector(':scope > .card-drag-handle')) {
        tabs.prepend(this.buildDragHandle('tab'))
      }
    } else {
      // Tab 卡残留的 tab 栏内 grip 清理
      el.querySelector('.dv-tabs-and-actions-container .card-drag-handle')?.remove()
      if (!el.querySelector(':scope > .card-drag-handle')) {
        el.prepend(this.buildDragHandle('bar'))
      }
    }
  }

  /** 构建把手 DOM：'tab' = tab 栏内左端 grip 图标；'bar' = 24px 把手条 */
  private buildDragHandle(kind: 'tab' | 'bar'): HTMLElement {
    const handle = document.createElement('div')
    handle.className = `card-drag-handle card-drag-handle-${kind}`
    handle.title = '拖动卡片'
    handle.innerHTML = GRIP_SVG
    return handle
  }
}
