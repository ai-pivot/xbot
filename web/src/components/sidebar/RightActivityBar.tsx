/**
 * RightActivityBar — the icon column that toggles the right sidebar panels.
 *
 * Panels = 内置面板（files/search/info/tasks/terminal）+ 插件 view 贡献点。
 * 插件 view 的 id/title/icon 由 usePluginViewPanels('right_sidebar') 动态提供——
 * 插件声明一次，桌面 + 移动两端自动出现对应 tab，无需分别硬编码。
 */
import { Files, Search, Info, ListChecks, SquareTerminal } from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

interface RightActivityBarProps {
  activePanel: SidebarPanel | null
  onTogglePanel: (panel: SidebarPanel) => void
}

const BUILTIN_PANELS: { panel: SidebarPanel; icon: IconComponent; labelKey: string }[] = [
  { panel: 'files', icon: Files, labelKey: 'sidebar.files' },
  { panel: 'search', icon: Search, labelKey: 'sidebar.search' },
  { panel: 'info', icon: Info, labelKey: 'sidebar.info' },
  { panel: 'tasks', icon: ListChecks, labelKey: 'sidebar.tasks' },
  { panel: 'terminal', icon: SquareTerminal, labelKey: 'sidebar.terminal' },
]

export function RightActivityBar({ activePanel, onTogglePanel }: RightActivityBarProps) {
  const { t } = useI18n()
  const pluginPanels = usePluginViewPanels('right_sidebar')

  // 合并：内置面板 tab + 插件 view tab（动态，插件声明即出现）。
  const tabs: { panel: SidebarPanel; icon: IconComponent; label: string }[] = [
    ...BUILTIN_PANELS.map((p) => ({ panel: p.panel, icon: p.icon, label: t(p.labelKey) })),
    ...pluginPanels.map((p) => ({ panel: p.id, icon: pluginIcon(p.view.icon), label: p.title })),
  ]

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-l bg-bg-secondary py-2">
      {tabs.map(({ panel, icon: Icon, label }) => {
        const active = activePanel === panel
        return (
          <Tooltip key={panel}>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-label={label}
                aria-pressed={active}
                onClick={() => onTogglePanel(panel)}
                className="group relative flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
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
        )
      })}
    </div>
  )
}
