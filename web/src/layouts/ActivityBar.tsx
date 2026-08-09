/**
 * ActivityBar — the leftmost 48px icon column (VSCode-style).
 *
 * After removing channel filtering (which belongs to SessionSidebar),
 * the bar now only hosts the sidebar toggle (top) and settings (bottom).
 */
import { Settings, PanelLeft } from 'lucide-react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

interface ActivityBarProps {
  /** Open the global settings dialog (Sheet). */
  onOpenSettings: () => void
  /** Whether the session sidebar is collapsed. */
  sidebarCollapsed?: boolean
  /** Toggle session sidebar visibility. */
  onToggleSidebar?: () => void
}

export function ActivityBar({ onOpenSettings, sidebarCollapsed = false, onToggleSidebar }: ActivityBarProps) {
  const { t } = useI18n()

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center justify-between border-r bg-bg-secondary py-2">
      <nav className="flex flex-col items-center gap-1">
        {onToggleSidebar && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-label={t('sidebar.toggle')}
                aria-pressed={sidebarCollapsed}
                onClick={onToggleSidebar}
                className="flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
                style={{ color: sidebarCollapsed ? 'var(--accent)' : 'var(--text-secondary)' }}
              >
                <PanelLeft className="size-5" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">{t('sidebar.toggle')}</TooltipContent>
          </Tooltip>
        )}
      </nav>

      <div className="flex flex-col items-center gap-1">
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
