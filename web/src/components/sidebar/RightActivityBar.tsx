/**
 * RightActivityBar — the icon column that toggles the right sidebar panels.
 *
 * Panels: files / search / info / tasks. Clicking a panel toggles the
 * sidebar (collapses if already open). Pure presentational — AppShell owns the
 * active state and passes a setter.
 */
import { Files, Search, Info, ListChecks } from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

interface RightActivityBarProps {
  activePanel: SidebarPanel | null
  onTogglePanel: (panel: SidebarPanel) => void
}

const PANELS: { panel: SidebarPanel; icon: IconComponent; labelKey: string }[] = [
  { panel: 'files', icon: Files, labelKey: 'sidebar.files' },
  { panel: 'search', icon: Search, labelKey: 'sidebar.search' },
  { panel: 'info', icon: Info, labelKey: 'sidebar.info' },
  { panel: 'tasks', icon: ListChecks, labelKey: 'sidebar.tasks' },
]

export function RightActivityBar({ activePanel, onTogglePanel }: RightActivityBarProps) {
  const { t } = useI18n()
  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-l bg-bg-secondary py-2">
      {PANELS.map(({ panel, icon: Icon, labelKey }) => {
        const active = activePanel === panel
        const label = t(labelKey)
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
