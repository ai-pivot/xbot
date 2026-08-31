/**
 * cardDrag — 卡片 Ctrl 拖动（全卡片化平铺布局的卡片移动入口）。
 *
 * 单 tab 卡片的 tab 栏被 LayoutEngine 隐藏后，卡片的拖动入口消失；多 tab
 * 卡片也要求精确抓取 tab 栏。Ctrl + 左键按住卡片任意区域（含输入框）拖动
 * = 移动整张卡片（平铺 WM / VS Code 无标题栏窗口的修饰键拖动惯例）。
 *
 * 手势语义：Ctrl + 左键从按下起被完全征用为拖动手势 ——
 * - pointerdown 立即 preventDefault（吞默认行为：输入框 focus/光标定位、
 *   文本选择起点、图片原生拖动）+ stopPropagation（阻断传播：dockview
 *   tab 拖动、React onMouseDown 等内部元素监听）—— 不触发卡片内任何交互
 * - 手势期间（state 存在）click 全吞 + dragstart 全吞
 *
 * 机制：
 * - pointerdown（Ctrl + 主键）→ 记录源卡片（DOM `.dv-groupview` 反查
 *   api.groups），吞交互（见手势语义）
 * - 位移超阈值（6px）进入拖动：body grabbing 光标 + 禁选择，实时计算
 *   drop 目标（pointer 下的其他 grid 卡片）与方位（hitZone：25% 边缘带，
 *   角部归最近边）
 * - overlay 指示 drop 后的半区位置（VS Code dropzone 风格）
 * - pointerup 落子：`group.api.moveTo({group, position})`（center = 并入
 *   目标卡片为 tab；四向 = 在目标旁分屏）+ onDrop 回调（LayoutEngine
 *   重算——move 后 group 集合签名可能不变，engine 的事件兜底不保证触发，
 *   由这里显式重算）
 * - Esc 中断拖动（不落子）
 *
 * 布局计算仍由 LayoutEngine 集中管线负责（onDrop → relayout）——本模块
 * 只做交互（拾取/指示/落子），不触碰任何尺寸。
 */
import type { DockviewApi, DockviewGroupPanel } from 'dockview-core'

export type DropZone = 'left' | 'right' | 'top' | 'bottom'

/** 目标卡片矩形（viewport 坐标，getBoundingClientRect 提取） */
export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

/** 位移阈值（px）：超过才进入拖动（区分 Ctrl+click） */
const DRAG_THRESHOLD_PX = 6

/** 按住 Ctrl 时 host 的 armed class（CSS `cursor: grab` 提示卡片可拖动） */
const DRAG_ARMED_CLASS = 'ctrl-drag-armed'

/** 源卡片中央 no-op 容差（归一化 ±0.1 —— 拖动意图不明确时不落子） */
const CENTER_TOLERANCE = 0.1

/**
 * 拖动落点方位（相对**源卡片**四分，平铺 WM 语义：拖向哪个方向，源就放到
 * 最近邻居的那边）。
 *
 * 与「相对目标卡片判方位」（旧 hitZone）的关键差异：master/stack 两列布局
 * 下源卡片占屏大半，pointer 的自然落点（目标卡片近侧，如拖主卡停在
 * sidebar 右半）按目标方位判定恒为 'right' —— 源放目标右边 = 原位，
 * 落子 no-op（「主卡片拖不动」根因：手势/overlay 全正常，moveTo 执行了
 * 但位置不变）。改为相对源四分：pointer 在源左半（或更左）→ 'left'
 * （换边），右半 → 'right'，上/下 → 垂直分屏。
 *
 * 中央小区（±0.15）→ null（no-op —— 拖动意图不明确时不落子，防误操作）。
 */
export function quadrantZone(source: Rect, px: number, py: number): DropZone | null {
  const relX = (px - source.x) / source.w
  const relY = (py - source.y) / source.h
  const dx = Math.abs(relX - 0.5)
  const dy = Math.abs(relY - 0.5)
  if (dx < CENTER_TOLERANCE && dy < CENTER_TOLERANCE) return null
  return dx >= dy ? (relX < 0.5 ? 'left' : 'right') : (relY < 0.5 ? 'top' : 'bottom')
}

export interface CardDragOptions {
  /** 落子后回调（LayoutEngine.relayout —— group move 不保证触发引擎事件兜底） */
  onDrop?: () => void
}

/**
 * drop 目标选择（纯函数）：精确命中（pointer 在矩形内）优先；否则取距离
 * 最近的矩形（欧氏距离的平方，矩形外扩最近点）。
 *
 * fallback 是拖动可用性的关键：主卡片占屏 ~80%，拖动它时 pointer 几乎
 * 总落在源卡片自身上（精确命中跳过源 → 永无目标 → 无 overlay、松手无
 * 操作——「主卡片拖不动」的根因）。最近目标 fallback 让拖动全程都有
 * 确定的目标与 overlay 反馈。返回 index（-1 = 无候选）。
 */
export function nearestRectIndex(x: number, y: number, rects: Rect[]): number {
  let nearest = -1
  let nearestDist = Infinity
  for (let i = 0; i < rects.length; i++) {
    const r = rects[i]
    if (x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h) return i
    const dx = Math.max(r.x - x, 0, x - (r.x + r.w))
    const dy = Math.max(r.y - y, 0, y - (r.y + r.h))
    const d = dx * dx + dy * dy
    if (d < nearestDist) {
      nearestDist = d
      nearest = i
    }
  }
  return nearest
}

/**
 * header 把手判定（三类，均无 Ctrl 直接拖动——触屏拖动入口）：
 * 1. 显式拖动把手（.card-drag-handle——LayoutEngine ensureDragHandle 注入：
 *    Tab 卡 tab 栏左端 grip / 非 Tab 卡顶部把手条。用户要求可见把手不隐藏）
 * 2. 非 Tab 卡的功能条（.card-handle-zone——面板内容自带的功能按钮条，
 *    如会话卡的渠道/分组下拉行；卡片分类制下非 Tab 卡无 tab 栏，功能条
 *    即卡片 Header，兼拖动把手）
 * 3. Tab 卡的 tab 栏空白区（.dv-tabs-and-actions-container，非 .dv-tab
 *    pill 内——pill 点击是 tab 激活/原生 tab 拖动）
 */
export function isHeaderGrab(el: EventTarget | null): boolean {
  const target = el as HTMLElement | null
  if (!target?.closest) return false
  if (target.closest('.card-drag-handle')) return true
  if (target.closest('.card-handle-zone')) return true
  return !!target.closest('.dv-tabs-and-actions-container') && !target.closest('.dv-tab')
}

interface DragState {
  /** 手势来源流：鼠标（VS Code 模式 mousedown/mousemove/mouseup，无
   * pointercancel——真实浏览器 pointer 流拖 SVG 把手会触发 HTML5 drag
   * 切换 pointercancel 杀手势）/ 触屏（pointerdown + setPointerCapture） */
  pointer: 'mouse' | 'touch'
  /** 触屏 pointer id（鼠标为 0） */
  pointerId: number
  startX: number
  startY: number
  source: DockviewGroupPanel
  active: boolean
  overlay: HTMLDivElement | null
}

/**
 * 启用卡片 Ctrl 拖动。返回 dispose（DockviewContainer 卸载时调用）。
 */
export function enableCardDrag(
  api: DockviewApi,
  host: HTMLElement,
  options: CardDragOptions = {},
): () => void {
  let state: DragState | null = null
  let clickSuppress = 0

  /**
   * 源卡片反查（pointerdown 时）。两层路径：
   * 1. DOM 反查：target.closest('.dv-groupview')——tab 栏 / 渲染在 group 内的
   *    panel 内容
   * 2. 坐标反查兜底：pointer 坐标落在哪个 group 的 rect 内——active panel
   *    的内容渲染在 dv-render-overlay 层（dockview overlayRenderContainer，
   *    DOM 不在 .dv-groupview 子树），DOM 反查对它恒 null（「主卡片拖不动」
   *    的根因：主卡内容在 overlay 层，closest 永远失败）
   */
  const groupAt = (el: EventTarget | null, x: number, y: number): DockviewGroupPanel | null => {
    const target = el as HTMLElement | null
    if (target?.closest) {
      const groupEl = target.closest('.dv-groupview') as HTMLElement | null
      if (groupEl) {
        const group = api.groups.find((g) => g.element === groupEl)
        if (group && group.api.location.type === 'grid') return group
      }
    }
    // 坐标反查兜底（overlay 渲染的内容：按 pointer 落点找 group rect）
    for (const g of api.groups) {
      if (g.api.location.type !== 'grid') continue
      const r = g.element.getBoundingClientRect()
      if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return g
    }
    return null
  }

  /** 拖动目标：跳过源自身与 floating；精确命中优先，pointer 落在源卡片/
   *  间隙上时取最近候选（nearestRectIndex fallback——主卡片占屏 80%，
   *  拖动时 pointer 几乎总在源内，无 fallback 则永无目标「拖不动」） */
  const targetAt = (x: number, y: number): { group: DockviewGroupPanel; rect: Rect } | null => {
    const candidates = api.groups
      .filter((g) => g !== state?.source && g.api.location.type === 'grid')
      .map((g) => {
        const r = g.element.getBoundingClientRect()
        return { group: g, rect: { x: r.left, y: r.top, w: r.width, h: r.height } }
      })
    const idx = nearestRectIndex(
      x,
      y,
      candidates.map((c) => c.rect),
    )
    return idx >= 0 ? candidates[idx] : null
  }

  const setOverlay = (zone: DropZone | null, rect: Rect | null) => {
    if (!state) return
    if (!zone || !rect) {
      state.overlay?.remove()
      state.overlay = null
      return
    }
    if (!state.overlay) {
      const el = document.createElement('div')
      el.style.cssText =
        'position:fixed;z-index:9999;pointer-events:none;transition:all 80ms ease;' +
        'background:color-mix(in srgb, var(--accent) 14%, transparent);' +
        'border:1px solid color-mix(in srgb, var(--accent) 45%, transparent);' +
        'border-radius:10px;'
      document.body.appendChild(el)
      state.overlay = el
    }
    // 半区指示（显示在目标卡片上）：left/right = 左右半张，top/bottom = 上下
    // 半张——指示源卡片松手后将出现在目标的哪一侧
    const half = {
      left: { x: rect.x, y: rect.y, w: rect.w / 2, h: rect.h },
      right: { x: rect.x + rect.w / 2, y: rect.y, w: rect.w / 2, h: rect.h },
      top: { x: rect.x, y: rect.y, w: rect.w, h: rect.h / 2 },
      bottom: { x: rect.x, y: rect.y + rect.h / 2, w: rect.w, h: rect.h / 2 },
    }[zone]
    const s = state.overlay.style
    s.left = `${half.x}px`
    s.top = `${half.y}px`
    s.width = `${half.w}px`
    s.height = `${half.h}px`
  }

  const beginDragVisual = () => {
    document.body.style.userSelect = 'none'
    document.body.style.cursor = 'grabbing'
  }
  const endDragVisual = () => {
    document.body.style.userSelect = ''
    document.body.style.cursor = ''
  }

  const cleanup = (drop: boolean) => {
    if (!state) return
    if (drop && state.active) {
      clickSuppress = Date.now() + 100
    }
    setOverlay(null, null)
    endDragVisual()
    state = null
    // 双路径监听全移除（哪个启动的移哪个——未注册的移除是 no-op）
    window.removeEventListener('mousemove', onMouseMove, { passive: false } as EventListenerOptions)
    window.removeEventListener('mouseup', onMouseUp)
    window.removeEventListener('pointermove', onTouchPointerMove, { passive: false } as EventListenerOptions)
    window.removeEventListener('pointerup', onTouchPointerUp)
    window.removeEventListener('pointercancel', onTouchPointerCancel)
    window.removeEventListener('keydown', onKeyDown, true)
  }

  /** 源卡片矩形（拖动落点方位的判定基准 — quadrantZone 相对源四分） */
  const sourceRect = (): Rect => {
    const r = state!.source.element.getBoundingClientRect()
    return { x: r.left, y: r.top, w: r.width, h: r.height }
  }

  /** 手势 move 主体（鼠标/触屏共享）：阈值激活 + overlay 渲染 */
  const gestureMoveBody = (clientX: number, clientY: number): void => {
    if (!state) return
    if (!state.active) {
      if (Math.hypot(clientX - state.startX, clientY - state.startY) < DRAG_THRESHOLD_PX) return
      state.active = true
      beginDragVisual()
    }
    const hit = targetAt(clientX, clientY)
    // 落点方位相对源卡片（拖向哪边放哪边）；中央小区 null → 无 overlay
    const zone = quadrantZone(sourceRect(), clientX, clientY)
    setOverlay(zone, hit?.rect ?? null)
  }

  /** 手势 up 主体（鼠标/触屏共享）：落子或放行 click。返回是否已激活（激活=吞） */
  const gestureUpBody = (clientX: number, clientY: number): boolean => {
    if (!state) return false
    if (!state.active) {
      cleanup(false) // 未过阈值 = click 语义，正常放行
      return false
    }
    const hit = targetAt(clientX, clientY)
    const zone = quadrantZone(sourceRect(), clientX, clientY)
    if (hit && zone) {
      // zone = 拖动方向（相对源）：源放到最近邻居卡片的该侧
      state.source.api.moveTo({ group: hit.group, position: zone })
      options.onDrop?.()
    }
    cleanup(true)
    return true
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && state?.active) cleanup(false)
  }

  /** 手势期间/落子后抑制 click（state 存在期间 = 手势全程，未过阈值也吞） */
  const onClickCapture = (e: MouseEvent) => {
    if (state || Date.now() < clickSuppress) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  /** 手势期间抑制原生拖动（图片/链接/SVG dragstart）。
   * 鼠标流的关键防线：HTML5 drag 尝试（dragstart）被吞后 mousemove 继续
   * 派发（VS Code 验证的行为）——pointer 流的 pointercancel 在 drag 尝试
   * 时先杀（Chrome/Wayland 真实环境「overlay 闪一下就死」根因，setPointerCapture
   * 也拦不住），mouse 流无此问题。 */
  const onDragStartCapture = (e: DragEvent) => {
    if (state) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  // ── 鼠标流（VS Code 模式：mousedown/mousemove/mouseup，无 pointercancel）──
  // 真实浏览器（Chrome/Wayland）pointer 流拖动把手（SVG/文本）会触发
  // pointercancel（HTML5 drag 切换干预，E2E 合成输入不触发故不可复现）
  // —— 鼠标事件流没有 cancel 概念，dragstart 被吞后照常派发，手势稳。

  const onMouseMove = (e: MouseEvent) => {
    if (!state || state.pointer !== 'mouse') return
    e.preventDefault()
    gestureMoveBody(e.clientX, e.clientY)
  }

  const onMouseUp = (e: MouseEvent) => {
    if (!state || state.pointer !== 'mouse') return
    if (gestureUpBody(e.clientX, e.clientY)) e.preventDefault()
  }

  const onMouseDown = (e: MouseEvent) => {
    if (e.button !== 0 || state) return
    // 拖动入口（二选一）：① Ctrl+左键（内容区任意处，修饰键手势）；
    // ② 显式把手/功能条/tab 栏空白区（无 Ctrl）。pill（.dv-tab）内不启动。
    if (!e.ctrlKey && !isHeaderGrab(e.target)) return
    const source = groupAt(e.target, e.clientX, e.clientY)
    if (!source) return
    // 拖动专用手势：从按下起吞掉一切交互。preventDefault 阻止默认行为
    //（输入框 focus/光标定位、文本选择起点）；stopPropagation 阻断传播
    //（dockview tab 拖动、React onMouseDown 等内部元素监听）。
    e.preventDefault()
    e.stopPropagation()
    state = {
      pointer: 'mouse',
      pointerId: 0,
      startX: e.clientX,
      startY: e.clientY,
      source,
      active: false,
      overlay: null,
    }
    window.addEventListener('mousemove', onMouseMove, { passive: false })
    window.addEventListener('mouseup', onMouseUp)
    window.addEventListener('keydown', onKeyDown, true)
  }

  // ── 触屏 pointer 流（tap 无 HTML5 drag 切换；setPointerCapture 防滚动干预）──

  const onTouchPointerMove = (e: PointerEvent) => {
    if (!state || state.pointer !== 'touch' || e.pointerId !== state.pointerId) return
    e.preventDefault()
    gestureMoveBody(e.clientX, e.clientY)
  }

  const onTouchPointerUp = (e: PointerEvent) => {
    if (!state || state.pointer !== 'touch' || e.pointerId !== state.pointerId) return
    if (gestureUpBody(e.clientX, e.clientY)) e.preventDefault()
  }

  const onTouchPointerCancel = (e: PointerEvent) => {
    if (!state || state.pointer !== 'touch' || e.pointerId !== state.pointerId) return
    cleanup(false)
  }

  const onTouchPointerDown = (e: PointerEvent) => {
    // 只处理触屏（鼠标走 onMouseDown——CDP/浏览器鼠标同时派发 pointerdown+
    // mousedown，双路径会双启动；触屏 preventDefault 阻止合成 mouse 防双跳）
    if (e.pointerType !== 'touch' || e.button !== 0 || state) return
    if (!isHeaderGrab(e.target)) return // 触屏把手直接拖（无修饰键概念）
    const source = groupAt(e.target, e.clientX, e.clientY)
    if (!source) return
    e.preventDefault()
    e.stopPropagation()
    try { host.setPointerCapture(e.pointerId) } catch { /* 极端竞态，忽略 */ }
    state = {
      pointer: 'touch',
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      source,
      active: false,
      overlay: null,
    }
    window.addEventListener('pointermove', onTouchPointerMove, { passive: false })
    window.addEventListener('pointerup', onTouchPointerUp)
    window.addEventListener('pointercancel', onTouchPointerCancel)
    window.addEventListener('keydown', onKeyDown, true)
  }

  host.addEventListener('mousedown', onMouseDown, { capture: true, passive: false })
  host.addEventListener('pointerdown', onTouchPointerDown, { capture: true, passive: false })
  window.addEventListener('click', onClickCapture, true)
  window.addEventListener('dragstart', onDragStartCapture, true)
  // Ctrl 光标提示：按住 Ctrl 时 host 加 armed class（CSS 光标 grab，子树
  // !important 覆盖输入框 text 等元素光标）提示卡片可拖动；拖动激活后
  // body.style.cursor = grabbing（beginDragVisual）接管。窗口失焦强制复位
  //（Ctrl 状态可能已丢失）。
  const onKeyDownCtrl = (e: KeyboardEvent) => {
    if (e.key === 'Control') host.classList.add(DRAG_ARMED_CLASS)
  }
  const onKeyUpCtrl = (e: KeyboardEvent) => {
    if (e.key === 'Control') host.classList.remove(DRAG_ARMED_CLASS)
  }
  const onWindowBlur = () => host.classList.remove(DRAG_ARMED_CLASS)
  window.addEventListener('keydown', onKeyDownCtrl)
  window.addEventListener('keyup', onKeyUpCtrl)
  window.addEventListener('blur', onWindowBlur)
  return () => {
    cleanup(false)
    host.removeEventListener('mousedown', onMouseDown, { capture: true } as EventListenerOptions)
    host.removeEventListener('pointerdown', onTouchPointerDown, { capture: true } as EventListenerOptions)
    window.removeEventListener('click', onClickCapture, true)
    window.removeEventListener('dragstart', onDragStartCapture, true)
    window.removeEventListener('keydown', onKeyDownCtrl)
    window.removeEventListener('keyup', onKeyUpCtrl)
    window.removeEventListener('blur', onWindowBlur)
    host.classList.remove(DRAG_ARMED_CLASS)
  }
}
