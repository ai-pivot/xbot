/**
 * TabHeader — VSCode-style tab header.
 *
 * Visual design (matching VSCode):
 *   - Active tab: bg = content area, top 1px accent bar, full-opacity text
 *   - Inactive tab: bg = tab bar, dimmer text, no top bar
 *   - Hover (inactive): bg lightens, close button appears
 *   - Close button: always visible on active tab, hover-only on inactive
 *   - Tab separator: 1px right border between tabs
 *   - No bottom border on tabs; the tab bar has a 1px bottom border
 */
import type { ComponentType, SVGProps } from 'react'
import { X, Bot, FileText, SquareTerminal, ListVideo } from 'lucide-react'
import type { DockviewPanelApi } from 'dockview'
import type { PanelParams } from '@/types/tab'
import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

const ICONS: Record<string, IconComponent> = {
  bot: Bot,
  file: FileText,
  terminal: SquareTerminal,
  background: ListVideo,
}

const TYPE_ICONS: Record<PanelParams['type'], IconComponent> = {
  agent: Bot,
  file: FileText,
  terminal: SquareTerminal,
  background: ListVideo,
}

export interface TabHeaderProps {
  params: PanelParams
  api: DockviewPanelApi
  isActive: boolean
  onActivate: () => void
}

export function TabHeader({ params, api, isActive, onActivate }: TabHeaderProps) {
  const isTouch = useIsTouch()
  const Icon = (params.icon ? ICONS[params.icon] : null) ?? TYPE_ICONS[params.type]
  const fullTitle = params.type === 'file' ? (params.filePath || params.title) : params.title

  return (
    <div
      className={cn(
        'group flex h-[35px] w-full min-w-0 cursor-pointer select-none items-center gap-1.5',
        'border-r border-t px-2.5 text-[13px] transition-colors duration-100',
        isActive
          ? 'border-r-border border-t-accent bg-bg-primary text-text-primary'
          : 'border-r-border border-t-transparent bg-bg-secondary text-text-secondary hover:bg-bg-tertiary hover:text-text-primary',
      )}
      title={fullTitle}
      role="tab"
      aria-selected={isActive}
      tabIndex={isActive ? 0 : -1}
      onMouseDown={(e) => {
        if (e.button === 1) {
          if (params.closable) {
            e.preventDefault()
            api.close()
          } else {
            e.preventDefault()
          }
        }
      }}
      onClick={(e) => {
        e.stopPropagation()
        onActivate()
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onActivate()
        }
      }}
    >
      <Icon aria-hidden className="size-3.5 shrink-0 text-text-secondary" />
      <span className="min-w-0 flex-1 truncate leading-none">{params.title}</span>
      {params.closable && (
        <button
          type="button"
          aria-label="Close tab"
          className={cn(
            'ml-0.5 flex size-[18px] shrink-0 items-center justify-center rounded-sm text-text-secondary',
            'transition-[color,background-color,opacity] duration-100 hover:bg-accent/15 hover:text-text-primary',
            'focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent',
            isActive || isTouch ? 'opacity-60 hover:opacity-100' : 'opacity-0 group-hover:opacity-60',
          )}
          onClick={(e) => {
            e.stopPropagation()
            api.close()
          }}
        >
          <X aria-hidden className="size-3" />
        </button>
      )}
    </div>
  )
}
