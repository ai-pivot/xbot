/**
 * ActivityBar — the leftmost 48px icon column (Spec 2 §3.2, VSCode-style).
 *
 * Icons: sessions (left sidebar), settings (opens SettingsDialog Sheet).
 * Theme switching is handled in Settings → Markdown Theme.
 *
 * Pure presentational — AppShell owns which view is active and passes setters.
 */
import {
  MessageSquare,
  Settings,
} from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

export type SidebarView = 'sessions'

interface ActivityBarProps {
  /** Currently active view (null = no left sidebar open). */
  activeView: SidebarView | null
  /** Toggle a view's sidebar; same view again collapses it. */
  onToggleView: (view: SidebarView) => void
  /** Open the global settings dialog (Sheet). */
  onOpenSettings: () => void
}

const VIEWS: { view: SidebarView; icon: IconComponent }[] = [
  { view: 'sessions', icon: MessageSquare },
]

export function ActivityBar({ activeView, onToggleView, onOpenSettings }: ActivityBarProps) {
  const { t } = useI18n()

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center justify-between border-r bg-bg-secondary py-2">
      <nav className="flex flex-col items-center gap-1">
        {VIEWS.map(({ view, icon: Icon }) => {
          const active = activeView === view
          return (
            <Tooltip key={view}>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  aria-label={labelFor(view, t)}
                  aria-pressed={active}
                  onClick={() => onToggleView(view)}
                  className="group relative flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
                  style={{ color: active ? 'var(--text-primary)' : 'var(--text-secondary)' }}
                >
                  {/* active accent bar (left edge) */}
                  <span
                    className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r"
                    style={{ backgroundColor: active ? 'var(--accent)' : 'transparent' }}
                  />
                  <Icon className="size-5" />
                </button>
              </TooltipTrigger>
              <TooltipContent side="right">{labelFor(view, t)}</TooltipContent>
            </Tooltip>
          )
        })}
      </nav>

      <div className="flex flex-col items-center gap-1">
        {/* Settings — opens SettingsDialog Sheet (not a sidebar view). */}
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t('settings.appearance')}
              aria-pressed={false}
              onClick={onOpenSettings}
              className="flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
              style={{ color: 'var(--text-secondary)' }}
            >
              <Settings className="size-5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">{t('settings.appearance')}</TooltipContent>
        </Tooltip>
      </div>
    </div>
  )
}

function labelFor(view: SidebarView, t: (k: string) => string): string {
  switch (view) {
    case 'sessions':
      return t('sidebar.sessions')
  }
}
