/**
 * ActivityBar — the leftmost 48px icon column (Spec 2 §3.2, VSCode-style).
 *
 * Layout (top to bottom):
 *   Sessions icon (always)
 *   Aggregate channel icon (Globe — shows all channels)
 *   Per-channel identity icons (CLI, Feishu, QQ, etc.)
 *   (flex spacer)
 *   Settings (bottom)
 *
 * Channel identity icons are fetched from GET /api/account/identities.
 * Active channel is persisted to localStorage["xbot:active-channel"].
 */
import { useCallback, useEffect, useState } from 'react'
import {
  Globe,
  Terminal,
  MessageCircle,
  MessageSquare,
  Bot,
  Server,
  Settings,
  MessageSquare as SessionsIcon,
} from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useSessionStore } from '@/hooks/useSessionStore'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

export type SidebarView = 'sessions'

interface IdentityEntry {
  id: number
  channel: string
  channel_user_id: string
}

interface ActivityBarProps {
  /** Currently active view (null = no left sidebar open). */
  activeView: SidebarView | null
  /** Toggle a view's sidebar; same view again collapses it. */
  onToggleView: (view: SidebarView) => void
  /** Open the global settings dialog (Sheet). */
  onOpenSettings: () => void
  /** Increments when settings dialog closes — triggers identity refresh. */
  settingsVersion?: number
}

const CHANNEL_ICONS: Record<string, IconComponent> = {
  web: Globe,
  cli: Terminal,
  feishu: MessageCircle,
  qq: MessageSquare,
  napcat: Bot,
  system: Server,
}

const VIEWS: { view: SidebarView; icon: IconComponent }[] = [
  { view: 'sessions', icon: SessionsIcon },
]

export function ActivityBar({ activeView, onToggleView, onOpenSettings, settingsVersion = 0 }: ActivityBarProps) {
  const { t } = useI18n()
  const { activeChannel, setActiveChannel } = useSessionStore()
  const [identities, setIdentities] = useState<IdentityEntry[]>([])

  const fetchIdentities = useCallback(async () => {
    try {
      const res = await fetch('/api/account/identities')
      if (!res.ok) return
      const data = await res.json()
      setIdentities(data.identities || [])
    } catch {
      // Degraded: show only aggregate + web
    }
  }, [])

  useEffect(() => {
    fetchIdentities()
  }, [fetchIdentities])

  // Re-fetch identities when settings dialog closes (user may have linked new identity)
  useEffect(() => {
    if (settingsVersion > 0) fetchIdentities()
  }, [settingsVersion, fetchIdentities])

  // Deduplicate: same channel may have multiple identities. Each identity
  // gets its own icon with its own badge.
  const channelIdentities = identities.filter((id) => id.channel !== 'web')

  // Determine if we should merge aggregate and web icon (only web identity, no linked)
  const mergeAggregate = channelIdentities.length === 0

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center justify-between border-r bg-bg-secondary py-2">
      <nav className="flex flex-col items-center gap-1">
        {/* Sessions view toggle */}
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

        {/* Divider */}
        <div className="my-1 h-px w-6" style={{ backgroundColor: 'var(--border)' }} />

        {/* Aggregate channel icon (shows all channels) */}
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t('channel.all')}
              aria-pressed={activeChannel === null}
              onClick={() => setActiveChannel(null)}
              className="group relative flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
              style={{ color: activeChannel === null ? 'var(--accent)' : 'var(--text-secondary)' }}
            >
              {/* active accent bar (left edge) */}
              <span
                className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r"
                style={{ backgroundColor: activeChannel === null ? 'var(--accent)' : 'transparent' }}
              />
              <Globe className="size-5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">{t('channel.all')}</TooltipContent>
        </Tooltip>

        {/* Web identity icon (hidden when merged with aggregate) */}
        {!mergeAggregate && (
          <ChannelIcon
            channel="web"
            badge={undefined}
            label={t('channel.web')}
            active={activeChannel === 'web'}
            onClick={() => setActiveChannel('web')}
          />
        )}

        {/* Per-channel identity icons */}
        {channelIdentities.map((id) => {
          const Icon = CHANNEL_ICONS[id.channel] || Globe
          const badge = id.channel_user_id?.charAt(0) || ''
          const label = t(`channel.${id.channel}`) || id.channel
          const isActive = activeChannel === id.channel
          return (
            <ChannelIcon
              key={`${id.channel}-${id.id}`}
              channel={id.channel}
              icon={Icon}
              badge={badge}
              label={label}
              active={isActive}
              onClick={() => setActiveChannel(id.channel)}
            />
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

/** A channel identity icon with optional badge. */
function ChannelIcon({
  channel,
  icon: IconProp,
  badge,
  label,
  active,
  onClick,
}: {
  channel: string
  icon?: IconComponent
  badge?: string
  label: string
  active: boolean
  onClick: () => void
}) {
  const Icon = IconProp || CHANNEL_ICONS[channel] || Globe
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label}
          aria-pressed={active}
          onClick={onClick}
          className="group relative flex size-9 items-center justify-center rounded-md transition-colors hover:bg-bg-tertiary"
          style={{
            color: active ? 'var(--accent)' : 'var(--text-secondary)',
            backgroundColor: active ? 'var(--accent-faint, rgba(99,102,241,0.12))' : undefined,
          }}
        >
          {/* active accent bar (left edge) */}
          <span
            className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r"
            style={{ backgroundColor: active ? 'var(--accent)' : 'transparent' }}
          />
          <Icon className="size-5" />
          {badge && (
            <span
              className="absolute -bottom-0.5 -right-0.5 flex size-3 items-center justify-center rounded-full border bg-bg-secondary"
              style={{ borderColor: 'var(--border)' }}
            >
              <span
                className="text-[8px] font-medium leading-none"
                style={{ color: 'var(--text-muted)' }}
              >
                {badge}
              </span>
            </span>
          )}
        </button>
      </TooltipTrigger>
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  )
}

function labelFor(view: SidebarView, t: (k: string) => string): string {
  switch (view) {
    case 'sessions':
      return t('sidebar.sessions')
  }
}
