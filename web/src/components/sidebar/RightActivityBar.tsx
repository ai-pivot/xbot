/**
 * RightActivityBar — the icon column that toggles the right sidebar panels.
 *
 * Panels = 内置面板（files/search/info/tasks/terminal）+ 插件 view 贡献点。
 * 插件 view 的 id/title/icon 由 usePluginViewPanels('right_sidebar') 动态提供——
 * 插件声明一次，桌面 + 移动两端自动出现对应 tab，无需分别硬编码。
 *
 * VSCode 式拖拽：
 * - 同 slot 重排：拖图标到另一图标上/下方，插入线指示
 * - 跨 slot 拖入：左栏 section 拖到右栏图标上 → moveItemTo 跨 slot 移动
 * - drop 判定放宽：整个图标按钮区域可放置
 */
import { Files, Search, Info, ListChecks, SquareTerminal } from 'lucide-react'
import {
  useCallback,
  useState,
  type ComponentType,
  type DragEvent as ReactDragEvent,
  type SVGProps,
} from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import type { PluginViewPanel } from '@/plugin-runtime/usePluginViewPanels'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { layoutRegistry, useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS } from '@/plugin-runtime/layoutTypes'
import { computeReorder } from '@/lib/reorder'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

/** 拖拽协议：dataTransfer 里存 itemId + 来源 slot。 */
const DRAG_TYPE = 'application/x-xbot-layout-item'
const DRAG_SLOT_TYPE = 'application/x-xbot-layout-slot'
const RIGHT_SLOT = 'desktop.sidebar' as const

interface RightActivityBarProps {
  activePanel: SidebarPanel | null
  onTogglePanel: (panel: SidebarPanel) => void
  /** 打开 container='main' 的插件主视图 tab（VSCode editor 语义）。 */
  onOpenMainView: (view: PluginViewPanel) => void
}

const BUILTIN_PANELS: { panel: SidebarPanel; icon: IconComponent; labelKey: string }[] = [
  { panel: 'files', icon: Files, labelKey: 'sidebar.files' },
  { panel: 'search', icon: Search, labelKey: 'sidebar.search' },
  { panel: 'info', icon: Info, labelKey: 'sidebar.info' },
  { panel: 'tasks', icon: ListChecks, labelKey: 'sidebar.tasks' },
  { panel: 'terminal', icon: SquareTerminal, labelKey: 'sidebar.terminal' },
]

const BUILTIN_PANEL_TO_LAYOUT: Record<string, string> = {
  files: BUILTIN_LAYOUT_ITEMS.desktopFiles,
  search: BUILTIN_LAYOUT_ITEMS.desktopSearch,
  info: BUILTIN_LAYOUT_ITEMS.desktopInfo,
  tasks: BUILTIN_LAYOUT_ITEMS.desktopTasks,
  terminal: BUILTIN_LAYOUT_ITEMS.desktopTerminal,
}

export function RightActivityBar({ activePanel, onTogglePanel, onOpenMainView }: RightActivityBarProps) {
  const { t } = useI18n()
  const pluginPanels = usePluginViewPanels('right_sidebar')
  const mainViews = usePluginViewPanels('main')
  const layoutItems = useLayoutItems(RIGHT_SLOT)
  const pluginPanelMap = new Map(pluginPanels.map((p) => [p.id, p]))

  const tabs: { layoutId: string; panel: SidebarPanel; icon: IconComponent; label: string }[] = []
  for (const item of layoutItems) {
    const builtin = BUILTIN_PANELS.find((p) => BUILTIN_PANEL_TO_LAYOUT[p.panel] === item.id)
    if (builtin) {
      tabs.push({ layoutId: item.id, panel: builtin.panel, icon: builtin.icon, label: t(builtin.labelKey) })
      continue
    }
    const plugin = pluginPanelMap.get(item.id)
    if (plugin) {
      tabs.push({
        layoutId: item.id,
        panel: plugin.id,
        icon: pluginIcon(plugin.view.icon) as IconComponent,
        label: plugin.title,
      })
    }
  }

  // ── VSCode 式拖拽（HTML5 DnD）──
  const [reorderSrc, setReorderSrc] = useState<string | null>(null)
  const [dropHint, setDropHint] = useState<{ targetId: string; before: boolean } | null>(null)
  const canReorder = tabs.length > 1

  // 判断拖拽来源：同 slot 重排（reorderSrc）还是跨 slot 拖入（dataTransfer）。
  const getDragSource = useCallback(
    (e: ReactDragEvent): string | null => {
      if (reorderSrc) return reorderSrc
      try {
        const id = e.dataTransfer.getData(DRAG_TYPE)
        if (id && id !== '') return id
      } catch {
        /* dragOver 阶段 getData 可能抛错 */
      }
      return null
    },
    [reorderSrc],
  )

  const isCrossSlot = useCallback((e: ReactDragEvent): boolean => {
    try {
      const srcSlot = e.dataTransfer.getData(DRAG_SLOT_TYPE)
      return srcSlot !== '' && srcSlot !== RIGHT_SLOT
    } catch {
      return false
    }
  }, [])

  const onIconDragStart = useCallback(
    (layoutId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      if (!canReorder) return
      setReorderSrc(layoutId)
      e.dataTransfer.setData(DRAG_TYPE, layoutId)
      e.dataTransfer.setData(DRAG_SLOT_TYPE, RIGHT_SLOT)
      e.dataTransfer.effectAllowed = 'move'
    },
    [canReorder],
  )

  const onIconDragOver = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      const src = getDragSource(e)
      if (!src || src === targetId) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      if (!isCrossSlot(e)) {
        const next = computeReorder(tabs.map((x) => x.layoutId), src, targetId, before)
        setDropHint(next ? { targetId, before } : null)
      } else {
        setDropHint({ targetId, before })
      }
    },
    [getDragSource, isCrossSlot, tabs],
  )

  const onIconDrop = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      e.preventDefault()
      const src = getDragSource(e)
      setReorderSrc(null)
      setDropHint(null)
      if (!src || src === targetId) return
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      if (isCrossSlot(e)) {
        layoutRegistry.moveItemTo(src, RIGHT_SLOT, { beforeId: before ? targetId : undefined })
      } else {
        const next = computeReorder(tabs.map((x) => x.layoutId), src, targetId, before)
        if (next) layoutRegistry.setSlotOrder(RIGHT_SLOT, next)
      }
    },
    [getDragSource, isCrossSlot, tabs],
  )

  const onIconDragEnd = useCallback(() => {
    setReorderSrc(null)
    setDropHint(null)
  }, [])

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-l bg-bg-secondary py-2">
      {tabs.map(({ layoutId, panel, icon: Icon, label }) => {
        const active = activePanel === panel
        const showLine = dropHint?.targetId === layoutId
        return (
          <div key={layoutId} className="flex w-full flex-col items-center">
            {showLine && dropHint.before && (
              <div data-testid="insertion-line" className="mb-0.5 h-0.5 w-6 shrink-0 rounded-full bg-app-accent" />
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={label}
                  aria-pressed={active}
                  onClick={() => onTogglePanel(panel)}
                  draggable={canReorder}
                  onDragStart={onIconDragStart(layoutId)}
                  onDragOver={onIconDragOver(layoutId)}
                  onDrop={onIconDrop(layoutId)}
                  onDragEnd={onIconDragEnd}
                  onDragLeave={() => setDropHint((h) => (h?.targetId === layoutId ? null : h))}
                  className="group relative flex size-9 shrink-0 select-none items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
                  style={{ color: active ? 'var(--text-primary)' : 'var(--text-secondary)' }}
                >
                  <span
                    className="absolute right-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-l"
                    style={{ backgroundColor: active ? 'var(--accent)' : 'transparent' }}
                  />
                  <Icon className="size-5" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="left">{label}</TooltipContent>
            </Tooltip>
            {showLine && !dropHint.before && (
              <div data-testid="insertion-line" className="mt-0.5 h-0.5 w-6 shrink-0 rounded-full bg-app-accent" />
            )}
          </div>
        )
      })}
      {mainViews.length > 0 && (
        <>
          <div className="my-1 h-px w-6 shrink-0 bg-border" />
          {mainViews.map((v) => (
            <Tooltip key={v.id}>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={v.title}
                  onClick={() => onOpenMainView(v)}
                  className="group relative flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
                  style={{ color: 'var(--text-secondary)' }}
                >
                  {(() => {
                    const Icon = pluginIcon(v.view.icon)
                    return <Icon className="size-5" />
                  })()}
                </button>
              </TooltipTrigger>
              <TooltipContent side="left">{v.title}</TooltipContent>
            </Tooltip>
          ))}
        </>
      )}
    </div>
  )
}
