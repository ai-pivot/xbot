/**
 * 模块级共享拖拽状态 —— 跨组件可见（SidebarSectionStack ↔ RightActivityBar）。
 *
 * HTML5 DnD 的 dataTransfer.getData() 在 dragOver 阶段受限（部分浏览器返回空
 * 或抛错），跨组件拖拽时组件各自的 reorderSrc state 不可见。用模块级变量
 * 在 dragStart 时写入，dragOver/drop 时读取，绕过 dataTransfer 限制。
 *
 * dataTransfer.types 在 dragOver 阶段始终可读（只检查类型名，不读值），
 * 用于判断「这是不是我们的拖拽」。
 */

export const DRAG_TYPE = 'application/x-xbot-layout-item'
export const DRAG_SLOT_TYPE = 'application/x-xbot-layout-slot'

export interface DragInfo {
  /** 被拖拽的布局项 ID。 */
  itemId: string
  /** 来源 slot（'desktop.activity_bar' / 'desktop.sidebar'）。 */
  sourceSlot: string
}

let currentDrag: DragInfo | null = null

export function startDrag(info: DragInfo): void {
  currentDrag = info
}

export function getDrag(): DragInfo | null {
  return currentDrag
}

export function clearDrag(): void {
  currentDrag = null
}

/** dragOver 阶段判断是否是我们的拖拽（types 可读，data 不可读）。 */
export function isOurDrag(e: { dataTransfer: { types: readonly string[] } }): boolean {
  return e.dataTransfer.types.includes(DRAG_TYPE)
}
