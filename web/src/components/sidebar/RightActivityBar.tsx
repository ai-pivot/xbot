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
 * - 实时预览：拖拽时源图标半透明 + 插入线
 */
import { Files, Search, Info, ListChecks, SquareTerminal } from 'lucide-react'
import {
  type ComponentType,
  type SVGProps,
} from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import type { PluginViewPanel } from '@/plugin-runtime/usePluginViewPanels'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS } from '@/plugin-runtime/layoutTypes'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

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

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-l bg-bg-secondary py-2">
      {tabs.map(({ layoutId, panel, icon: Icon, label }) => {
        const active = activePanel === panel
        return (
          <div key={layoutId} className="flex w-full flex-col items-center">
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={label}
                  aria-pressed={active}
                  onClick={() => onTogglePanel(panel)}
                  className="group relative flex size-9 shrink-0 select-none items-center justify-center rounded-md transition-opacity hover:bg-bg-tertiary"
                  style={{
                    color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
                  }}
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
