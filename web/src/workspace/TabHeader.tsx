/**
 * TabHeader — custom VSCode-style tab header rendered entirely with inline
 * styles (no CSS class dependencies).
 *
 * Styling:
 *   - Active tab: 2px bottom accent bar in theme color, full-opacity text,
 *     background matching the content area (--bg-primary).
 *   - Inactive tab: 2px transparent bottom bar, dimmer text (opacity 0.6),
 *     transparent background.
 *   - Close button only when closable=true, on hover/focus.
 *   - No CSS rules needed in index.css — all visual properties are inline.
 */
import type { CSSProperties, ComponentType, SVGProps } from 'react'
import { X, Bot, FileText, SquareTerminal, ListVideo } from 'lucide-react'
import type { DockviewPanelApi } from 'dockview'
import type { PanelParams } from '@/types/tab'

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
  const Icon = (params.icon ? ICONS[params.icon] : null) ?? TYPE_ICONS[params.type]

  const tabStyle: CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    gap: '6px',
    padding: '4px 12px',
    height: '35px',
    cursor: 'pointer',
    borderBottom: isActive ? '2px solid var(--accent)' : '2px solid var(--border)',
    borderInlineEnd: '1px solid var(--border)',
    backgroundColor: isActive ? 'var(--bg-primary)' : 'color-mix(in srgb, var(--accent) 8%, var(--bg-secondary))',
    opacity: isActive ? 1 : 0.82,
    transition: 'opacity 0.15s, border-color 0.15s, background-color 0.15s',
    userSelect: 'none',
    whiteSpace: 'nowrap',
    position: 'relative',
  }

  const iconStyle: CSSProperties = {
    color: 'var(--text-primary)',
    flexShrink: 0,
    width: '14px',
    height: '14px',
  }

  const titleStyle: CSSProperties = {
    color: 'var(--text-primary)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    fontSize: '13px',
  }

  const closeBtnStyle: CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    width: '16px',
    height: '16px',
    flexShrink: 0,
    border: 'none',
    background: 'transparent',
    cursor: 'pointer',
    opacity: 0,
    transition: 'opacity 0.15s',
    color: 'var(--text-secondary)',
    marginLeft: '4px',
  }

  return (
    <div
      style={tabStyle}
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
      onMouseEnter={(e) => {
        if (params.closable) {
          const btn = (e.currentTarget as HTMLElement).querySelector('[data-close-btn]')
          if (btn) (btn as HTMLElement).style.opacity = '1'
        }
      }}
      onMouseLeave={(e) => {
        if (params.closable) {
          const btn = (e.currentTarget as HTMLElement).querySelector('[data-close-btn]')
          if (btn) (btn as HTMLElement).style.opacity = '0'
        }
      }}
      role="tab"
      aria-selected={isActive}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onActivate()
        }
      }}
    >
      <Icon style={iconStyle} size={14} />
      <span style={titleStyle}>{params.title}</span>
      {params.closable && (
        <button
          type="button"
          aria-label="Close tab"
          data-close-btn
          style={closeBtnStyle}
          onClick={(e) => {
            e.stopPropagation()
            api.close()
          }}
          onFocus={(e) => { (e.currentTarget as HTMLElement).style.opacity = '1' }}
          onBlur={(e) => { (e.currentTarget as HTMLElement).style.opacity = '0' }}
        >
          <X size={12} />
        </button>
      )}
    </div>
  )
}
