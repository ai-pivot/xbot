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

export type DropZone = 'left' | 'right' | 'top' | 'bottom' | 'center'

/** 目标卡片矩形（viewport 坐标，getBoundingClientRect 提取） */
export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

/** 边缘带宽度比例（目标卡片宽/高的 25% 区域判定四向 drop） */
const EDGE = 0.25
/** 位移阈值（px）：超过才进入拖动（区分 Ctrl+click） */
const DRAG_THRESHOLD_PX = 6

/** 按住 Ctrl 时 host 的 armed class（CSS `cursor: grab` 提示卡片可拖动） */
const DRAG_ARMED_CLASS = 'ctrl-drag-armed'

/**
 * drop 方位判定（纯函数）：pointer 相对目标矩形的位置 → 四向边缘带或中心。
 * 角部（两轴同时命中边缘带）归深度比例更近的边。
 */
export function hitZone(rect: Rect, px: number, py: number): DropZone {
  const ex = rect.w * EDGE
  const ey = rect.h * EDGE
  const inLeft = px < rect.x + ex
  const inRight = px > rect.x + rect.w - ex
  const inTop = py < rect.y + ey
  const inBottom = py > rect.y + rect.h - ey
  if (!inLeft && !inRight && !inTop && !inBottom) return 'center'
  if ((inLeft || inRight) && (inTop || inBottom)) {
    const dx = inLeft ? px - rect.x : rect.x + rect.w - px
    const dy = inTop ? py - rect.y : rect.y + rect.h - py
    return dx / ex <= dy / ey ? (inLeft ? 'left' : 'right') : (inTop ? 'top' : 'bottom')
  }
  if (inLeft) return 'left'
  if (inRight) return 'right'
  return inTop ? 'top' : 'bottom'
}

export interface CardDragOptions {
  /** 落子后回调（LayoutEngine.relayout —— group move 不保证触发引擎事件兜底） */
  onDrop?: () => void
}

interface DragState {
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

  const groupAt = (el: EventTarget | null): DockviewGroupPanel | null => {
    const target = el as HTMLElement | null
    if (!target?.closest) return null
    const groupEl = target.closest('.dv-groupview') as HTMLElement | null
    if (!groupEl) return null
    const group = api.groups.find((g) => g.element === groupEl)
    if (!group || group.api.location.type !== 'grid') return null
    return group
  }

  /** 拖动目标：pointer 下的其他 grid 卡片（跳过源自身与 floating） */
  const targetAt = (x: number, y: number): { group: DockviewGroupPanel; rect: Rect } | null => {
    for (const group of api.groups) {
      if (group === state?.source || group.api.location.type !== 'grid') continue
      const r = group.element.getBoundingClientRect()
      if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) {
        return { group, rect: { x: r.left, y: r.top, w: r.width, h: r.height } }
      }
    }
    return null
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
    // 半区指示：left/right = 左右半张卡片，top/bottom = 上下半张，center 整卡
    const half = {
      left: { x: rect.x, y: rect.y, w: rect.w / 2, h: rect.h },
      right: { x: rect.x + rect.w / 2, y: rect.y, w: rect.w / 2, h: rect.h },
      top: { x: rect.x, y: rect.y, w: rect.w, h: rect.h / 2 },
      bottom: { x: rect.x, y: rect.y + rect.h / 2, w: rect.w, h: rect.h / 2 },
      center: { x: rect.x, y: rect.y, w: rect.w, h: rect.h },
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
    window.removeEventListener('pointermove', onPointerMove)
    window.removeEventListener('pointerup', onPointerUp)
    window.removeEventListener('pointercancel', onPointerCancel)
    window.removeEventListener('keydown', onKeyDown, true)
  }

  const onPointerMove = (e: PointerEvent) => {
    if (!state || e.pointerId !== state.pointerId) return
    if (!state.active) {
      if (Math.hypot(e.clientX - state.startX, e.clientY - state.startY) < DRAG_THRESHOLD_PX) return
      state.active = true
      beginDragVisual()
    }
    e.preventDefault()
    const hit = targetAt(e.clientX, e.clientY)
    setOverlay(hit ? hitZone(hit.rect, e.clientX, e.clientY) : null, hit?.rect ?? null)
  }

  const onPointerUp = (e: PointerEvent) => {
    if (!state || e.pointerId !== state.pointerId) return
    if (!state.active) {
      cleanup(false) // 未过阈值 = click 语义，正常放行
      return
    }
    e.preventDefault()
    const hit = targetAt(e.clientX, e.clientY)
    if (hit) {
      const zone = hitZone(hit.rect, e.clientX, e.clientY)
      state.source.api.moveTo({
        group: hit.group,
        // center 省略 position（moveTo 默认并入目标卡片为 tab）
        position: zone === 'center' ? undefined : zone,
      })
      options.onDrop?.()
    }
    cleanup(true)
  }

  const onPointerCancel = (e: PointerEvent) => {
    if (!state || e.pointerId !== state.pointerId) return
    cleanup(false)
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

  /** 手势期间抑制原生拖动（图片/链接 dragstart） */
  const onDragStartCapture = (e: DragEvent) => {
    if (state) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  const onPointerDown = (e: PointerEvent) => {
    if (!e.ctrlKey || e.button !== 0 || state) return
    const source = groupAt(e.target)
    if (!source) return
    // Ctrl+左键 = 拖动专用手势：从按下起吞掉一切交互。preventDefault 阻止
    // 默认行为（输入框 focus/光标定位、文本选择起点、图片原生拖动）；
    // stopPropagation 阻断传播（dockview tab 拖动、React onMouseDown 等内部
    // 元素监听）。未过拖动阈值就松开时 click 也被吞（onClickCapture 的
    // state 窗口）——Ctrl+左键绝不触发卡片内任何交互。
    e.preventDefault()
    e.stopPropagation()
    state = {
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      source,
      active: false,
      overlay: null,
    }
    window.addEventListener('pointermove', onPointerMove, { passive: false })
    window.addEventListener('pointerup', onPointerUp)
    window.addEventListener('pointercancel', onPointerCancel)
    window.addEventListener('keydown', onKeyDown, true)
  }

  host.addEventListener('pointerdown', onPointerDown, { capture: true, passive: false })
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
    host.removeEventListener('pointerdown', onPointerDown, { capture: true } as EventListenerOptions)
    window.removeEventListener('click', onClickCapture, true)
    window.removeEventListener('dragstart', onDragStartCapture, true)
    window.removeEventListener('keydown', onKeyDownCtrl)
    window.removeEventListener('keyup', onKeyUpCtrl)
    window.removeEventListener('blur', onWindowBlur)
    host.classList.remove(DRAG_ARMED_CLASS)
  }
}
