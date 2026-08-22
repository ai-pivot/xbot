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

const HEIGHTS_KEY = 'xbot:leftbar:section-heights'
const COLLAPSED_KEY = 'xbot:leftbar:section-collapsed'
const MIN_SECTION_H = 80

/** 拖拽协议：dataTransfer 里存 itemId，types 里标记来源 slot。 */
const DRAG_TYPE = 'application/x-xbot-layout-item'
const DRAG_SLOT_TYPE = 'application/x-xbot-layout-slot'

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
  const [reorderSrc, setReorderSrc] = useState<string | null>(null)
  const [dropHint, setDropHint] = useState<{ targetId: string; before: boolean } | null>(null)

  useEffect(() => {
    try {
      localStorage.setItem(HEIGHTS_KEY, JSON.stringify(heights))
    } catch {
      /* storage unavailable */
    }
  }, [heights])

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
  // 同 slot 重排 + 跨 slot 拖入（右栏图标拖到左栏）。
  // drop 判定放宽：整个 section 区域可放置（不只 header）。
  const canReorder = Boolean(slotId) && sections.length > 1

  const onSectionDragStart = useCallback(
    (id: string) => (e: ReactDragEvent<HTMLElement>) => {
      if (!canReorder) return
      setReorderSrc(id)
      e.dataTransfer.setData(DRAG_TYPE, id)
      if (slotId) e.dataTransfer.setData(DRAG_SLOT_TYPE, slotId)
      e.dataTransfer.effectAllowed = 'move'
    },
    [canReorder, slotId],
  )

  // 判断拖拽来源：同 slot 重排（reorderSrc 有值）还是跨 slot 拖入（dataTransfer）。
  const getDragSource = useCallback(
    (e: ReactDragEvent): string | null => {
      // 同 slot：reorderSrc（dragOver 里读不到 dataTransfer，用 state）。
      if (reorderSrc) return reorderSrc
      // 跨 slot：从 dataTransfer 读（drop 时可读）。
      try {
        const id = e.dataTransfer.getData(DRAG_TYPE)
        if (id && id !== '') return id
      } catch {
        /* dragOver 阶段 getData 可能抛错（部分浏览器）—— drop 时一定能读 */
      }
      return null
    },
    [reorderSrc],
  )

  const isCrossSlot = useCallback(
    (e: ReactDragEvent): boolean => {
      try {
        const srcSlot = e.dataTransfer.getData(DRAG_SLOT_TYPE)
        return srcSlot !== '' && srcSlot !== slotId
      } catch {
        return false
      }
    },
    [slotId],
  )

  const onSectionDragOver = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLElement>) => {
      if (!slotId) return
      const src = getDragSource(e)
      if (!src || src === targetId) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      // 同 slot：computeReorder 判 no-op；跨 slot：总是有效（moveItemTo）。
      if (!isCrossSlot(e)) {
        const next = computeReorder(sections.map((s) => s.id), src, targetId, before)
        setDropHint(next ? { targetId, before } : null)
      } else {
        setDropHint({ targetId, before })
      }
    },
    [slotId, getDragSource, isCrossSlot, sections],
  )

  const onSectionDrop = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLElement>) => {
      e.preventDefault()
      const src = getDragSource(e)
      setReorderSrc(null)
      setDropHint(null)
      if (!slotId || !src || src === targetId) return
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      if (isCrossSlot(e)) {
        // 跨 slot：moveItemTo 把项从原 slot 移到本 slot 的指定位置。
        layoutRegistry.moveItemTo(src, slotId, { beforeId: before ? targetId : undefined })
      } else {
        // 同 slot：setSlotOrder 重排。
        const next = computeReorder(sections.map((s) => s.id), src, targetId, before)
        if (next) layoutRegistry.setSlotOrder(slotId, next)
      }
    },
    [slotId, getDragSource, isCrossSlot, sections],
  )

  const onSectionDragEnd = useCallback(() => {
    setReorderSrc(null)
    setDropHint(null)
  }, [])

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
      {sections.map((sec, i) => {
        const isCollapsed = collapsed[sec.id] ?? false
        const fixedH = heights[sec.id] ?? sec.defaultHeight
        const style: CSSProperties = isCollapsed
          ? { flex: '0 0 auto' }
          : fixedH != null
            ? { height: fixedH, flex: '0 0 auto' }
            : { flex: '1 1 0%' }
        const showLine = dropHint?.targetId === sec.id
        return (
          <div key={sec.id} className="contents">
            {showLine && dropHint.before && (
              <div data-testid="insertion-line" className="h-0.5 shrink-0 bg-app-accent" />
            )}
            <section
              data-section-id={sec.id}
              className="flex min-h-0 flex-col overflow-hidden"
              style={style}
              onDragOver={onSectionDragOver(sec.id)}
              onDrop={onSectionDrop(sec.id)}
              onDragLeave={() => setDropHint((h) => (h?.targetId === sec.id ? null : h))}
            >
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
            </section>
            {showLine && !dropHint.before && (
              <div data-testid="insertion-line" className="h-0.5 shrink-0 bg-app-accent" />
            )}
            {i < sections.length - 1 && (
              <div
                role="separator"
                aria-orientation="horizontal"
                aria-label={`Resize ${sections[i].title}`}
                onPointerDown={startResize(sec.id)}
                className={`h-1 shrink-0 cursor-row-resize transition-colors hover:bg-app-accent/40 ${
                  draggingId === sec.id ? 'bg-app-accent/40' : 'bg-transparent'
                }`}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}
