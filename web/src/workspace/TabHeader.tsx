/**
 * TabHeader — dockview tab header, "rounded pill block" design (布局 v2):
 *   - Tab bar background is var(--bg-secondary) (owned by index.css `.dv-tab`
 *     is transparent, so the bar shows through); each tab is a rounded-lg block.
 *   - Active tab: bg = var(--bg-primary) + 1px var(--border) border,
 *     text = var(--text-primary), icon = var(--accent), close × always visible.
 *   - Inactive tab: transparent bg + var(--text-muted) text; close × appears
 *     on hover only (always visible on touch devices — no hover there).
 */
import type { ComponentType, SVGProps } from 'react'
import { X, Bot, FileText, SquareTerminal, ListVideo, Box, FileDiff, Folder } from 'lucide-react'
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
  plugin: Box,
  diff: FileDiff,
  panel: Folder,
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
        'rounded-lg border px-2.5 py-1 text-[13px] transition-colors duration-100',
        isActive
          ? 'border-border bg-bg-primary text-text-primary'
          : 'border-transparent bg-transparent text-text-muted',
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
      <Icon aria-hidden className={cn('size-3.5 shrink-0', isActive ? 'text-accent' : 'text-text-muted')} />
      <span className="min-w-0 flex-1 truncate leading-none">{params.title}</span>
      {params.closable && (
        <button
          type="button"
          aria-label="Close tab"
          className={cn(
            'ml-0.5 flex size-[18px] shrink-0 items-center justify-center rounded-sm text-text-secondary',
            'transition-[color,background-color,opacity] duration-100 hover:bg-bg-tertiary hover:text-text-primary',
            'focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent',
            isActive || isTouch ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
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
