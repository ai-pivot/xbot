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
    padding: '0 10px',
    height: '35px',
    cursor: 'pointer',
    // VSCode: active tab has accent top border, inactive has transparent
    borderTop: isActive ? '1px solid var(--accent)' : '1px solid transparent',
    borderRight: '1px solid var(--border)',
    // Active tab matches content area; inactive matches tab bar
    backgroundColor: isActive
      ? 'var(--bg-primary)'
      : 'var(--bg-secondary)',
    color: 'var(--text-primary)',
    opacity: isActive ? 1 : 0.7,
    transition: 'opacity 0.12s, background-color 0.12s',
    userSelect: 'none',
    whiteSpace: 'nowrap',
    position: 'relative',
    maxWidth: '200px',
    minWidth: '40px',
  }

  const iconStyle: CSSProperties = {
    color: 'var(--text-secondary)',
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
    lineHeight: '35px',
  }

  const closeBtnStyle: CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    width: '18px',
    height: '18px',
    flexShrink: 0,
    border: 'none',
    borderRadius: '4px',
    background: 'transparent',
    cursor: 'pointer',
    // Active tab: always show close button. Inactive: show on hover.
    opacity: isActive ? 0.6 : 0,
    transition: 'opacity 0.12s, background-color 0.12s',
    color: 'var(--text-secondary)',
    marginLeft: '2px',
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
        // Hover: lighten bg for inactive tabs, show close button
        if (!isActive) {
          e.currentTarget.style.backgroundColor = 'var(--bg-tertiary)'
          e.currentTarget.style.opacity = '0.9'
        }
        if (params.closable) {
          const btn = (e.currentTarget as HTMLElement).querySelector('[data-close-btn]')
          if (btn) (btn as HTMLElement).style.opacity = '1'
        }
      }}
      onMouseLeave={(e) => {
        if (!isActive) {
          e.currentTarget.style.backgroundColor = 'var(--bg-secondary)'
          e.currentTarget.style.opacity = '0.7'
        }
        if (params.closable && !isActive) {
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
          onMouseEnter={(e) => {
            e.currentTarget.style.backgroundColor = 'color-mix(in srgb, var(--accent) 20%, transparent)'
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.backgroundColor = 'transparent'
          }}
          onFocus={(e) => { (e.currentTarget as HTMLElement).style.opacity = '1' }}
          onBlur={(e) => {
            if (!isActive) (e.currentTarget as HTMLElement).style.opacity = '0'
          }}
        >
          <X size={12} />
        </button>
      )}
    </div>
  )
}
