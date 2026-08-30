/**
 * ActivityBar — the leftmost 48px icon column (Spec 2 §3.2, VSCode-style).
 *
 * Layout (top to bottom):
 *   Aggregate channel icon (Globe — shows all channels)
 *   Web channel icon (when other identities exist)
 *   Per-channel identity icons (CLI, Feishu, QQ, etc.)
 *   (flex spacer)
 *   Settings (bottom)
 *
 * Channel icons are fetched from POST /api/channels/list.
 * Active channel is persisted to localStorage["xbot:active-channel"].
 * Clicking a channel icon also ensures the session sidebar is open.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Globe,
  Terminal,
  MessageCircle,
  MessageSquare,
  Bot,
  Server,
  Settings,
  LayoutGrid,
  Plug,
  PanelLeft,
} from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useSessionStore } from '@/hooks/useSessionStore'
import { postAPI } from '@/lib/api'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

export type SidebarView = 'sessions'

interface ChannelsResponse {
  channels?: string[]
}

interface ActivityBarProps {
  /** Open the global settings dialog (Sheet). */
  onOpenSettings: () => void
  /** Increments when settings dialog closes — triggers identity refresh. */
  settingsVersion?: number
  /** Whether the session sidebar is collapsed. */
  sidebarCollapsed?: boolean
  /** Toggle session sidebar visibility. */
  onToggleSidebar?: () => void
}

const CHANNEL_ICONS: Record<string, IconComponent> = {
  web: Globe,
  cli: Terminal,
  feishu: MessageCircle,
  qq: MessageSquare,
  napcat: Bot,
  system: Server,
  github: Plug,
  gitlab: Plug,
}

export function ActivityBar({ onOpenSettings, settingsVersion = 0, sidebarCollapsed = false, onToggleSidebar }: ActivityBarProps) {
  const { t } = useI18n()
  const { activeChannel, setActiveChannel } = useSessionStore()
  const [discoveredChannels, setDiscoveredChannels] = useState<string[]>([])

  const fetchChannels = useCallback(async () => {
    try {
      const data = await postAPI<ChannelsResponse>('/api/channels/list')
      setDiscoveredChannels(data.channels || [])
    } catch {
      // Degraded: show only aggregate + web
    }
  }, [])

  useEffect(() => {
    fetchChannels()
  }, [fetchChannels])

  // Re-fetch channels when settings dialog closes (plugin channels may have
  // been activated since)
  useEffect(() => {
    if (settingsVersion > 0) fetchChannels()
  }, [settingsVersion, fetchChannels])

  // Build the set of channels to show: channels discovered from the DB
  // (includes plugin channels like github/gitlab that have sessions).
  const channelIdentities = useMemo(() => {
    const byChannel = new Map<string, null>()
    for (const ch of discoveredChannels) {
      if (ch === 'web' || ch === 'agent') continue
      byChannel.set(ch, null)
    }
    return Array.from(byChannel.entries()).map(([channel]) => ({
      channel,
    }))
  }, [discoveredChannels])

  // Determine if we should merge aggregate and web icon (only web identity, no linked)
  const mergeAggregate = channelIdentities.length === 0

  return (
    <div className="flex h-full w-12 shrink-0 flex-col items-center justify-between border-r bg-bg-secondary py-2">
      <nav className="flex flex-col items-center gap-1">
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
              <LayoutGrid className="size-5" />
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

        {/* Per-channel icons (includes plugin channels from DB) */}
        {channelIdentities.map(({ channel }) => {
          const Icon = CHANNEL_ICONS[channel] || Globe
          const label = t(`channel.${channel}`) || channel
          const isActive = activeChannel === channel
          return (
            <ChannelIcon
              key={channel}
              channel={channel}
              icon={Icon}
              label={label}
              active={isActive}
              onClick={() => setActiveChannel(channel)}
            />
          )
        })}
      </nav>

      <div className="flex flex-col items-center gap-1">
        {/* Sidebar toggle */}
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
