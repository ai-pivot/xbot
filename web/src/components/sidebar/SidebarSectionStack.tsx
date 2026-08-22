/**
 * SidebarSectionStack —— VSCode 式左侧边栏 section 堆叠。
 *
 * 多个 section（会话列表、插件 view…）垂直堆叠；每个 section 有可折叠
 * header；相邻 section 之间有拖拽分隔条（拖动调整上方 section 高度，像素级）；
 * 高度与折叠状态纯前端持久化（localStorage），清缓存后回到默认自动 layout。
 *
 * VSCode 式拖拽（slotId 提供时启用）：
 * - 同 slot 重排：拖 section 到另一个 section 上方/下方，插入线指示
 * - 跨 slot 拖入：右栏图标拖到左栏 section 上 → moveItemTo 跨 slot 移动
 * - drop 判定放宽：整个 section 区域可放置（不只 header），指针在 section
 *   上半区=before、下半区=after
 * - 实时预览：拖拽时显示半透明 ghost 占位（松手前预览 layout 结果）
 *
 * 高度模型（自动 layout 与手动拖拽共存）：
 * - 用户拖过分隔条的 section：固定 px（localStorage 记忆）
 * - 未拖过但有 defaultHeight 的 section：固定 defaultHeight
 * - 两者皆无的 section：flex 平分剩余空间（自动 layout）
 * - 折叠的 section：只渲染 header（高度 auto）
 */
import { ChevronRight } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type DragEvent as ReactDragEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'

import { layoutRegistry } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS, type LayoutSlotId } from '@/plugin-runtime/layoutTypes'
import { computeReorder } from '@/lib/reorder'
import { DRAG_TYPE, DRAG_SLOT_TYPE, startDrag, getDrag, clearDrag, isOurDrag } from '@/lib/dragState'

const HEIGHTS_KEY = 'xbot:leftbar:section-heights'
const COLLAPSED_KEY = 'xbot:leftbar:section-collapsed'
const MIN_SECTION_H = 80

export interface SidebarSection {
  id: string
  title: string
  content: ReactNode
  /** 初始高度（px）。未设置且用户未拖过分隔条时该 section 平分剩余空间。 */
  defaultHeight?: number
}

interface SidebarSectionStackProps {
  sections: SidebarSection[]
  /** 提供时启用 header 拖拽重排（sections 的 id 必须是布局项 id）。 */
  slotId?: LayoutSlotId
}

function readJSON<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : fallback
  } catch {
    return fallback
  }
}

export function SidebarSectionStack({ sections, slotId }: SidebarSectionStackProps): ReactNode {
  const [heights, setHeights] = useState<Record<string, number>>(() => {
    const h = readJSON<Record<string, number>>(HEIGHTS_KEY, {})
    if (h.sessions !== undefined && h[BUILTIN_LAYOUT_ITEMS.desktopSessions] === undefined) {
      h[BUILTIN_LAYOUT_ITEMS.desktopSessions] = h.sessions
      delete h.sessions
    }
    return h
  })
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(() =>
    readJSON<Record<string, boolean>>(COLLAPSED_KEY, {}),
  )
  const containerRef = useRef<HTMLDivElement>(null)
  const [draggingId, setDraggingId] = useState('')
  // dropHint: 当前悬停目标 + before/after + 是否跨 slot（用于 ghost 预览）。
  const [dropHint, setDropHint] = useState<{ targetId: string; before: boolean; crossSlot: boolean } | null>(null)
  // 拖拽中的源 itemId（用于 ghost 半透明渲染）。
  const [dragSrcId, setDragSrcId] = useState<string | null>(null)

  useEffect(() => {
    try {
      localStorage.setItem(HEIGHTS_KEY, JSON.stringify(heights))
    } catch {
      /* storage unavailable */
    }
  }, [heights])

  // 清理过期高度：sections 变化时（拖走/拖入），移除不在当前列表里的 section
  // 的高度记录。否则残留的固定高度会导致所有 section 都是固定高度，没有
  // flex-1 填充剩余空间 → 黑色空区域。
  useEffect(() => {
    const currentIds = new Set(sections.map((s) => s.id))
    setHeights((prev) => {
      let changed = false
      const next: Record<string, number> = {}
      for (const [k, v] of Object.entries(prev)) {
        if (currentIds.has(k)) {
          next[k] = v
        } else {
          changed = true
        }
      }
      return changed ? next : prev
    })
  }, [sections])

  useEffect(() => {
    try {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify(collapsed))
    } catch {
      /* ignore */
    }
  }, [collapsed])

  const toggle = useCallback((id: string) => {
    setCollapsed((prev) => {
      const cur = prev[id] ?? false
      return { ...prev, [id]: !cur }
    })
  }, [])

  // ── VSCode 式拖拽（HTML5 DnD）──
  // 用模块级 dragState 跨组件共享源信息（dataTransfer.getData 在 dragOver
  // 阶段受限，跨组件时各自的 state 不可见）。
  const canReorder = Boolean(slotId) && sections.length > 1

  const onSectionDragStart = useCallback(
    (id: string) => (e: ReactDragEvent<HTMLElement>) => {
      if (!canReorder) return
      const srcSlot = slotId ?? ''
      startDrag({ itemId: id, sourceSlot: srcSlot })
      setDragSrcId(id)
      e.dataTransfer.setData(DRAG_TYPE, id)
      e.dataTransfer.setData(DRAG_SLOT_TYPE, srcSlot)
      e.dataTransfer.effectAllowed = 'move'
    },
    [canReorder, slotId],
  )

  const onSectionDragOver = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLElement>) => {
      if (!slotId) return
      // 用 types 判断（dragOver 阶段 getData 受限），用模块级 state 读源。
      if (!isOurDrag(e)) return
      const drag = getDrag()
      if (!drag || drag.itemId === targetId) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      const crossSlot = drag.sourceSlot !== slotId
      if (!crossSlot) {
        // 同 slot：computeReorder 判 no-op（拖回原位不显示插入线）。
        // 但即使 no-op 也设置 dropHint（显示插入线），让用户知道当前位置
        // 可放置——只是 drop 时 computeReorder 返回 null 不实际重排。
        const next = computeReorder(sections.map((s) => s.id), drag.itemId, targetId, before)
        setDropHint({ targetId, before, crossSlot: false })
        void next
      } else {
        setDropHint({ targetId, before, crossSlot: true })
      }
    },
    [slotId, sections],
  )

  const onSectionDrop = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLElement>) => {
      e.preventDefault()
      const drag = getDrag()
      setDropHint(null)
      setDragSrcId(null)
      clearDrag()
      if (!slotId || !drag || drag.itemId === targetId) return
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      if (drag.sourceSlot !== slotId) {
        // 跨 slot：moveItemTo 把项从原 slot 移到本 slot 的指定位置。
        layoutRegistry.moveItemTo(drag.itemId, slotId, { beforeId: before ? targetId : undefined })
      } else {
        // 同 slot：setSlotOrder 重排。
        const next = computeReorder(sections.map((s) => s.id), drag.itemId, targetId, before)
        if (next) layoutRegistry.setSlotOrder(slotId, next)
      }
    },
    [slotId, sections],
  )

  const onSectionDragEnd = useCallback(() => {
    setDropHint(null)
    setDragSrcId(null)
    clearDrag()
  }, [])

  // dragLeave 闪烁修复：拖拽期间不在 dragLeave 清除 dropHint。
  // 根因：同 slot 拖拽时，从一个 section 移到另一个 section，dragLeave 清了
  // dropHint，下一帧 dragOver 又设回来 → 闪烁。改为只在 dragEnd/drop 清除。
  const onSectionDragLeave = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLElement>) => {
      // 拖拽进行中：不清除（让 dragOver 在新 section 上更新 dropHint）。
      if (getDrag()) return
      // 非拖拽状态（如鼠标离开整个区域）：清除。
      const related = e.relatedTarget as Node | null
      if (related && e.currentTarget.contains(related)) return
      setDropHint((h) => (h?.targetId === targetId ? null : h))
    },
    [],
  )

  const startResize = useCallback(
    (sectionId: string) => (e: ReactPointerEvent<HTMLDivElement>) => {
      e.preventDefault()
      const handle = e.currentTarget
      try {
        handle.setPointerCapture(e.pointerId)
      } catch {
        /* pointer capture unsupported (jsdom) */
      }
      const startY = e.clientY
      const sectionEl = containerRef.current?.querySelector<HTMLElement>(
        `[data-section-id="${sectionId}"]`,
      )
      const startH = heights[sectionId] ?? sectionEl?.offsetHeight ?? MIN_SECTION_H
      const containerH = containerRef.current?.offsetHeight ?? 0
      const maxH = Math.max(MIN_SECTION_H, containerH - MIN_SECTION_H)
      setDraggingId(sectionId)

      const onMove = (ev: PointerEvent) => {
        const next = Math.min(maxH, Math.max(MIN_SECTION_H, startH + (ev.clientY - startY)))
        setHeights((prev) => ({ ...prev, [sectionId]: Math.round(next) }))
      }
      const onUp = () => {
        try {
          handle.releasePointerCapture(e.pointerId)
        } catch {
          /* pointer already released */
        }
        handle.removeEventListener('pointermove', onMove)
        handle.removeEventListener('pointerup', onUp)
        setDraggingId('')
      }
      handle.addEventListener('pointermove', onMove)
      handle.addEventListener('pointerup', onUp)
    },
    [heights],
  )

  return (
    <div ref={containerRef} className="flex min-h-0 flex-1 flex-col overflow-hidden">
      {(() => {
        // 拖拽实时预览：计算松手后的 section 列表（含跨 slot 拖入的 ghost 占位）。
        // 同 slot 重排：按 dropHint 位置重排现有 sections。
        // 跨 slot 拖入：在 dropHint 位置插入一个 ghost section（半透明占位）。
        const drag = getDrag()
        const isCrossSlot = drag != null && drag.sourceSlot !== slotId
        const previewSections = (() => {
          if (!dropHint || !drag) return sections
          if (isCrossSlot) {
            // 跨 slot：在目标位置插入 ghost 占位。
            const ghost: SidebarSection = {
              id: '__drag_ghost__',
              title: '（拖入预览）',
              content: null,
              defaultHeight: 240,
            }
            const idx = sections.findIndex((s) => s.id === dropHint.targetId)
            if (idx === -1) return [...sections, ghost]
            return dropHint.before
              ? [...sections.slice(0, idx), ghost, ...sections.slice(idx)]
              : [...sections.slice(0, idx + 1), ghost, ...sections.slice(idx + 1)]
          }
        // 同 slot：不重排预览（只显示插入线 + 源半透明）。
        // 重排预览会导致源 section 移动到新位置，用户松手在源自身上 →
        // onSectionDrop 检测到 drag.itemId === targetId → return → 不重排。
        // 插入线已足够指示目标位置。
        return sections
        })()

        return previewSections.map((sec, i) => {
          const isGhost = sec.id === '__drag_ghost__'
          const isCollapsed = isGhost ? false : (collapsed[sec.id] ?? false)
          const fixedH = isGhost ? sec.defaultHeight : (heights[sec.id] ?? sec.defaultHeight)
          const isLast = i === previewSections.length - 1
          const style: CSSProperties = isCollapsed
            ? { flex: '0 0 auto' }
            : fixedH != null && !isLast
              ? { height: fixedH, flex: '0 0 auto' }
              : { flex: '1 1 0%' }
          const showLine = !isGhost && dropHint?.targetId === sec.id
          const isDragSrc = !isGhost && dragSrcId === sec.id
          return (
            <div key={sec.id} className="contents">
              {showLine && dropHint!.before && (
                <div data-testid="insertion-line" className="h-0.5 shrink-0 bg-app-accent" />
              )}
              <section
                data-section-id={sec.id}
                className="flex min-h-0 flex-col overflow-hidden transition-opacity"
                style={{
                  ...style,
                  opacity: isGhost ? 0.3 : isDragSrc ? 0.4 : undefined,
                  border: isGhost ? '2px dashed var(--accent)' : undefined,
                }}
                onDragOver={isGhost ? (e) => { e.preventDefault(); e.dataTransfer.dropEffect = 'move' } : onSectionDragOver(sec.id)}
                onDrop={isGhost ? (e) => {
                  // ghost 的 onDrop：直接用 dropHint.before（dragOver 时已算好），
                  // 不从 e.currentTarget 重新算 before（e.currentTarget 是 ghost
                  // 元素，不是目标 section，rect 不对 → before 算错 → computeReorder
                  // 返回 null → 松手回去）。
                  e.preventDefault()
                  const drag = getDrag()
                  setDropHint(null)
                  setDragSrcId(null)
                  clearDrag()
                  if (!slotId || !drag || !dropHint) return
                  const targetId = dropHint.targetId
                  if (drag.itemId === targetId) return
                  if (drag.sourceSlot !== slotId) {
                    layoutRegistry.moveItemTo(drag.itemId, slotId, { beforeId: dropHint.before ? targetId : undefined })
                  } else {
                    const next = computeReorder(sections.map((s) => s.id), drag.itemId, targetId, dropHint.before)
                    if (next) layoutRegistry.setSlotOrder(slotId, next)
                  }
                } : onSectionDrop(sec.id)}
                onDragLeave={isGhost ? undefined : onSectionDragLeave(sec.id)}
              >
                {isGhost ? (
                  <div className="flex items-center justify-center py-4 text-xs text-text-muted">
                    {drag?.itemId ?? '拖入预览'}
                  </div>
                ) : (
                  <>
                    <button
                      type="button"
                      onClick={() => toggle(sec.id)}
                      draggable={canReorder}
                      onDragStart={onSectionDragStart(sec.id)}
                      onDragEnd={onSectionDragEnd}
                      title={isCollapsed ? `展开${sec.title}` : `收起${sec.title}`}
                      className={`flex shrink-0 select-none items-center gap-1.5 border-b border-[var(--border)] bg-[var(--bg-secondary)] px-2 py-1.5 text-xs font-medium text-text-muted transition-colors hover:text-text-secondary ${
                        canReorder ? 'cursor-grab active:cursor-grabbing' : ''
                      }`}
                    >
                      <ChevronRight
                        className={`size-3.5 shrink-0 transition-transform ${isCollapsed ? '' : 'rotate-90'}`}
                      />
                      <span className="truncate">{sec.title}</span>
                    </button>
                    {!isCollapsed && (
                      <div className="min-h-0 flex-1 overflow-hidden">{sec.content}</div>
                    )}
                  </>
                )}
              </section>
              {showLine && !dropHint!.before && (
                <div data-testid="insertion-line" className="h-0.5 shrink-0 bg-app-accent" />
              )}
              {i < previewSections.length - 1 && !isGhost && (
                <div
                  role="separator"
                  aria-orientation="horizontal"
                  aria-label={`Resize ${sec.title}`}
                  onPointerDown={startResize(sec.id)}
                  className={`h-1 shrink-0 cursor-row-resize transition-colors hover:bg-app-accent/40 ${
                    draggingId === sec.id ? 'bg-app-accent/40' : 'bg-transparent'
                  }`}
                />
              )}
            </div>
          )
        })
      })()}
    </div>
  )
}
