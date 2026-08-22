/**
 * RightActivityBar — the icon column that toggles the right sidebar panels.
 *
 * Panels = 内置面板（files/search/info/tasks/terminal）+ 插件 view 贡献点。
 * 插件 view 的 id/title/icon 由 usePluginViewPanels('right_sidebar') 动态提供——
 * 插件声明一次，桌面 + 移动两端自动出现对应 tab，无需分别硬编码。
 *
 * VSCode 式拖拽重排：图标按 layoutRegistry 的 desktop.sidebar 顺序渲染，
 * 拖动图标到另一图标上/下方显示插入线，松手重排（setSlotOrder 持久化到
 * localStorage + 后端 web:ui:layout-order）。被移到其他 slot 的项不显示。
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

// 内置面板 → 布局项 id 映射（布局注册表里的 desktop.sidebar 项）。
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
  // 布局配置：desktop.sidebar slot 里的项 = 用户希望显示的侧栏 tab（registry
  // 排序 = 用户拖拽顺序）。被用户移到其他 slot 的项不再显示（布局定制生效）。
  const layoutItems = useLayoutItems('desktop.sidebar')
  const pluginPanelMap = new Map(pluginPanels.map((p) => [p.id, p]))

  // 布局项顺序统一渲染：内置面板 + 插件 view 都按 layoutItems 顺序出现。
  const tabs: { layoutId: string; panel: SidebarPanel; icon: IconComponent; label: string }[] =
    []
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
    // 其他布局项（未解析的 id）：跳过 —— 前面 enabledIds 语义一致。
  }

  // ── VSCode 式重排（HTML5 DnD）──
  const [reorderSrc, setReorderSrc] = useState<string | null>(null)
  const [dropHint, setDropHint] = useState<{ targetId: string; before: boolean } | null>(null)
  const canReorder = tabs.length > 1

  const onIconDragStart = useCallback(
    (layoutId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      if (!canReorder) return
      setReorderSrc(layoutId)
      e.dataTransfer.setData('text/plain', layoutId)
      e.dataTransfer.effectAllowed = 'move'
    },
    [canReorder],
  )

  const onIconDragOver = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      if (!canReorder || !reorderSrc || reorderSrc === targetId) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      const next = computeReorder(tabs.map((x) => x.layoutId), reorderSrc, targetId, before)
      setDropHint(next ? { targetId, before } : null)
    },
    [canReorder, reorderSrc, tabs],
  )

  const onIconDrop = useCallback(
    (targetId: string) => (e: ReactDragEvent<HTMLButtonElement>) => {
      e.preventDefault()
      const src = reorderSrc
      setReorderSrc(null)
      setDropHint(null)
      if (!src || src === targetId) return
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY < rect.top + rect.height / 2
      const next = computeReorder(tabs.map((x) => x.layoutId), src, targetId, before)
      if (next) layoutRegistry.setSlotOrder('desktop.sidebar', next)
    },
    [reorderSrc, tabs],
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
                  {/* active accent bar (right edge) */}
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
