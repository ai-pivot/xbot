/**
 * SidebarSectionStack —— VSCode 式左侧边栏 section 堆叠。
 *
 * 多个 section（会话列表、插件 view…）垂直堆叠；每个 section 有可折叠
 * header；相邻 section 之间有拖拽分隔条（拖动调整上方 section 高度，像素级）；
 * 高度与折叠状态纯前端持久化（localStorage），清缓存后回到默认自动 layout。
 *
 * 高度模型（自动 layout 与手动拖拽共存）：
 * - 用户拖过分隔条的 section：固定 px（localStorage 记忆）
 * - 未拖过但有 defaultHeight 的 section：固定 defaultHeight
 * - 两者皆无的 section：flex 平分剩余空间（自动 layout）
 * - 折叠的 section：只渲染 header（高度 auto）
 */
import { BarChart3, Blocks, ChevronRight, GitBranch, MessageSquare, Sparkles, type LucideIcon } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'

import { BUILTIN_LAYOUT_ITEMS, type LayoutSlotId } from '@/plugin-runtime/layoutTypes'
import i18n from '@/i18n'

const HEIGHTS_KEY = 'xbot:leftbar:section-heights'
const COLLAPSED_KEY = 'xbot:leftbar:section-collapsed'
const MIN_SECTION_H = 80

/** section 图标映射（内置 section id → lucide 图标）——左侧栏可扫读性。 */
const SECTION_ICON: Record<string, LucideIcon> = {
  [BUILTIN_LAYOUT_ITEMS.desktopSessions]: MessageSquare,
  'xbot.session-stats': BarChart3,
  'xbot.plugin-manager': Blocks,
  'xbot.skill-manager': Sparkles,
  'xbot.git-fancy': GitBranch,
}

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

export function SidebarSectionStack({ sections, slotId: _slotId }: SidebarSectionStackProps): ReactNode {
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
        const isLast = i === sections.length - 1
        const style: CSSProperties = isCollapsed
          ? { flex: '0 0 auto' }
          : fixedH != null && !isLast
            ? { height: fixedH, flex: '0 0 auto' }
            : { flex: '1 1 0%' }
        return (
          <div key={sec.id} className="contents">
            <section
              data-section-id={sec.id}
              className="flex min-h-0 flex-col overflow-hidden"
              style={style}
            >
              <button
                type="button"
                onClick={() => toggle(sec.id)}
                title={isCollapsed
                  ? i18n.t('sidebar.sectionExpand', { title: sec.title, defaultValue: '展开 {{title}}' })
                  : i18n.t('sidebar.sectionCollapse', { title: sec.title, defaultValue: '收起 {{title}}' })}
                className="flex w-full shrink-0 select-none items-center gap-1.5 rounded-md px-2 pb-1.5 pt-2.5 text-left text-[10px] font-semibold uppercase tracking-wider text-text-muted transition-spring hover:bg-bg-tertiary/50 hover:text-text-secondary active:scale-[0.99]"
              >
                <ChevronRight
                  className={`size-3.5 shrink-0 transition-transform duration-200 ${isCollapsed ? '' : 'rotate-90'}`}
                />
                {(() => {
                  const SectionIcon = SECTION_ICON[sec.id]
                  return SectionIcon ? <SectionIcon className="size-3.5 shrink-0 opacity-70" /> : null
                })()}
                <span className="truncate normal-case">{sec.title}</span>
              </button>
              {/* 折叠常驻（display:none 而非条件 unmount——2026-08-30 侧边栏
                  展开卡顿修复）：条件渲染让每次展开都重新 mount 面板
                  （SessionList 数百行 render + 插件面板 RPC + DOM 构建，
                  100ms+ 掉帧）。display:none 保留 DOM/state/RPC 缓存，
                  展开零成本（与 PanelChrome 的折叠模式一致）；display:none
                  的子树不参与布局（折叠时 section 高度塌缩到 header 不变），
                  IntersectionObserver/ResizeObserver 也不触发（不可见）。 */}
              <div
                className={`min-h-0 flex-1 overflow-hidden ${isCollapsed ? '' : 'animate-section-in'}`}
                style={{ display: isCollapsed ? 'none' : undefined }}
              >
                {sec.content}
              </div>
            </section>
            {i < sections.length - 1 && (
              <div
                role="separator"
                aria-orientation="horizontal"
                aria-label={`Resize ${sec.title}`}
                onPointerDown={startResize(sec.id)}
                className={`h-2 shrink-0 cursor-row-resize transition-colors hover:bg-app-accent/40 ${
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
