/**
 * SidebarSectionStack —— VSCode 式左侧边栏 section 堆叠。
 *
 * 多个 section（会话列表、插件 view…）垂直堆叠；每个 section 有可折叠
 * header；相邻 section 之间有拖拽分隔条（拖动调整上方 section 高度，像素级）；
 * 高度与折叠状态纯前端持久化（localStorage），清缓存后回到默认自动 layout。
 *
 * VSCode 式拖拽重排（slotId 提供时启用）：拖 section header 到另一个
 * section 上方/下方，显示插入线（before/after 按指针相对 header 中线判定），
 * 松手重排并经 layoutRegistry.setSlotOrder 持久化（localStorage + 后端
 * web:ui:layout-order）。宽度/高度/折叠等细节不进后端。
 *
 * 高度模型（自动 layout 与手动拖拽共存）：
 * - 用户拖过分隔条的 section：固定 px（localStorage 记忆）
 * - 未拖过但有 defaultHeight 的 section：固定 defaultHeight
 * - 两者皆无的 section：flex 平分剩余空间（自动 layout）
 * - 折叠的 section：只渲染 header（高度 auto）
 *
 * 每个 section 有确定的高度约束 + overflow-hidden —— 根治「会话列表自然
 * 高度溢出覆盖下方插件区」的重叠 bug（旧结构的外层容器不是 flex 容器也
 * 没有 overflow-hidden，CollapsibleGroup 高度由内容决定）。
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
    // 一次性迁移：sessions section id 从 'sessions' 改为布局项 id。
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
  // 重排拖拽状态（HTML5 DnD；dragOver 里读不到 dataTransfer，用 state 记源 id）。
  const [reorderSrc, setReorderSrc] = useState<string | null>(null)
  const [dropHint, setDropHint] = useState<{ targetId: string; before: boolean } | null>(null)

  useEffect(() => {
    try {
      localStorage.setItem(HEIGHTS_KEY, JSON.stringify(heights))
    } catch {
      /* storage unavailable — layout still works, just not remembered */
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
      // 展开一个之前无固定高度的 section 时保持自动 layout；折叠不需要高度。
      const next = { ...prev, [id]: !cur }
      return next
    })
  }, [])

  // ── VSCode 式重排（HTML5 DnD，仅 header 是拖拽源和放置目标）──
  const canReorder = Boolean(slotId) && sections.length > 1

  const onHeaderDragStart = useCallback(
    (id: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      if (!canReorder) return
      setReorderSrc(id)
      e.dataTransfer.setData('text/plain', id)
      e.dataTransfer.effectAllowed = 'move'
    },
    [canReorder],
  )

  const onHeaderDragOver = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      if (!canReorder || !reorderSrc || reorderSrc === targetId) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      const next = computeReorder(sections.map((s) => s.id), reorderSrc, targetId, before)
      setDropHint(next ? { targetId, before } : null)
    },
    [canReorder, reorderSrc, sections],
  )

  const onHeaderDrop = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      e.preventDefault()
      const src = reorderSrc
      setReorderSrc(null)
      setDropHint(null)
      if (!slotId || !src || src === targetId) return
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      const next = computeReorder(sections.map((s) => s.id), src, targetId, before)
      if (next) layoutRegistry.setSlotOrder(slotId, next)
    },
    [reorderSrc, slotId, sections],
  )

  const onHeaderDragEnd = useCallback(() => {
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
        /* pointer capture unsupported (jsdom) — listeners still work on the handle */
      }
      const startY = e.clientY
      // 起始高度：已记忆高度，否则用 DOM 实测高度（自动 layout 下的当前值）。
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
            >
              <button
                type="button"
                onClick={() => toggle(sec.id)}
                draggable={canReorder}
                onDragStart={onHeaderDragStart(sec.id)}
                onDragOver={onHeaderDragOver(sec.id)}
                onDrop={onHeaderDrop(sec.id)}
                onDragEnd={onHeaderDragEnd}
                onDragLeave={() => setDropHint((h) => (h?.targetId === sec.id ? null : h))}
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
