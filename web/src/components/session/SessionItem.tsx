/**
 * SessionItem — a single chatroom row in the session list.
 *
 * Single-line layout: [status dot] + title + relative time.
 * No left decoration bar; active session uses background highlight.
 *
 * SubAgent mode (Child 5): when isSubAgent is true, the item is indented,
 * shows a Bot icon instead of the status dot, and hides the star/time.
 */
import { useCallback } from 'react'
import { Star, Pencil, Trash2, Bot, GitBranch, Loader2, ExternalLink } from 'lucide-react'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import { parseAgentChatID, sessionKey } from '@/lib/session-grouping'
import type { SessionInfo, SessionStatus } from '@/types/shared'

interface SessionItemProps {
  session: SessionInfo
  starred: boolean
  unread: boolean
  active: boolean
  /** True for SubAgent items (indented, bot icon, read-only). */
  isSubAgent?: boolean
  depth?: number
  onSelect: (id: string) => void
  onToggleStar: (id: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
}

const STATUS_COLOR: Record<SessionStatus, string> = {
  running: 'var(--status-running)',
  waiting_input: 'var(--status-waiting)',
  pending: 'var(--status-waiting)',
  idle: 'var(--status-idle)',
  unread: 'var(--status-waiting)',
  error: 'var(--status-error)',
}

export function SessionItem({
  session,
  starred,
  unread,
  active,
  isSubAgent,
  depth = isSubAgent ? 1 : 0,
  onSelect,
  onToggleStar,
  onRename,
  onDelete,
}: SessionItemProps) {
  const { t } = useI18n()
  const key = sessionKey(session)
  const title = isSubAgent ? subAgentTitle(session) : (session.label || session.chatID)

  const openInBrowserTab = useCallback(() => {
    const sessionParam = `${session.channel || 'web'}:${session.chatID}`
    const url = `${window.location.origin}/?session=${encodeURIComponent(sessionParam)}`
    window.open(url, '_blank')
  }, [session])

  const row = (
    <div
      role="button"
      tabIndex={0}
      onClick={() => {
        if (!session.synthetic) onSelect(session.chatID)
      }}
      onKeyDown={(e) => {
        if (session.synthetic) return
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect(session.chatID)
        }
      }}
      className={cn(
        'group flex items-center gap-2 rounded-md px-2 py-1.5 text-left transition-all',
        active ? 'bg-bg-tertiary' : !session.synthetic && 'hover:bg-bg-tertiary/60 hover:shadow-md',
        session.synthetic && 'cursor-default opacity-80',
      )}
      style={{
        ...(isSubAgent ? { marginLeft: `${depth}rem` } : {}),
        ...(unread && !isSubAgent ? { borderLeft: '2px solid var(--accent)' } : {}),
      }}
    >
      {isSubAgent ? (
        /* SubAgent: Bot icon (colored by running state) */
        <Bot
          className="size-3.5 shrink-0"
          style={{ color: session.running ? 'var(--status-running)' : 'var(--text-muted)' }}
        />
      ) : session.synthetic ? (
        <GitBranch className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
      ) : session.status === 'running' ? (
        /* Running: spinning Loader2 icon */
        <Loader2 className="size-3.5 shrink-0 animate-spin" style={{ color: 'var(--status-running)' }} />
      ) : session.status === 'waiting_input' ? (
        /* Waiting for input: slow spinning Loader2 icon */
        <Loader2
          className="size-3.5 shrink-0 animate-spin"
          style={{ color: 'var(--status-waiting)', animationDuration: '2s' }}
        />
      ) : (
        /* Other statuses: static colored dot */
        <span
          className="size-2 shrink-0 rounded-full"
          style={{ backgroundColor: STATUS_COLOR[session.status] }}
          aria-hidden
        />
      )}

      {/* Star toggle (hover/starred) — hidden for SubAgents */}
      {!isSubAgent && !session.synthetic && (
        <button
          type="button"
          aria-label={starred ? t('session.unstar') : t('session.star')}
          onClick={(e) => {
            e.stopPropagation()
            onToggleStar(key)
          }}
          className={cn(
            'shrink-0 rounded p-0.5 transition-opacity',
            starred ? 'opacity-100' : 'opacity-0 group-hover:opacity-60',
          )}
          style={starred ? { color: '#e6a700' } : { color: 'var(--text-muted)' }}
        >
          <Star className="size-3.5" fill={starred ? 'currentColor' : 'none'} />
        </button>
      )}

      {/* Title */}
      <span
        className={cn('flex-1 truncate text-xs', unread && !isSubAgent ? 'font-semibold' : 'font-medium')}
        style={{
          color: isSubAgent || session.synthetic
            ? 'var(--text-secondary)'
            : unread
              ? 'var(--accent)'
              : 'var(--text-primary)',
        }}
        title={title}
      >
        {title}
      </span>

      {/* Running indicator for SubAgents */}
      {isSubAgent && session.running && (
        <span
          className="size-1.5 shrink-0 animate-pulse rounded-full"
          style={{ backgroundColor: 'var(--status-running)' }}
          aria-hidden
        />
      )}

      {/* Relative time — hidden for SubAgents */}
      {!isSubAgent && !session.synthetic && (
        <span className="shrink-0 text-[10px] tabular-nums" style={{ color: 'var(--text-muted)' }}>
          {relativeTime(session.lastActive, t)}
        </span>
      )}
    </div>
  )

  // SubAgent items: context menu with only "open in tab"
  if (isSubAgent || session.synthetic) {
    return (
     <ContextMenu>
       <ContextMenuTrigger asChild>{row}</ContextMenuTrigger>
       <ContextMenuContent className="data-[state=open]:animate-none data-[state=closed]:animate-none">
          <ContextMenuItem onClick={openInBrowserTab}>
            <ExternalLink className="size-4" />
            {t('session.openInTab')}
          </ContextMenuItem>
       </ContextMenuContent>
     </ContextMenu>
  )
  }

  return (
     <ContextMenu>
       <ContextMenuTrigger asChild>{row}</ContextMenuTrigger>
       <ContextMenuContent className="data-[state=open]:animate-none data-[state=closed]:animate-none">
          <ContextMenuItem onClick={openInBrowserTab}>
          <ExternalLink className="size-4" />
          {t('session.openInTab')}
        </ContextMenuItem>
        <ContextMenuItem onClick={() => onToggleStar(key)}>
          <Star
            className="size-4"
            fill={starred ? 'currentColor' : 'none'}
            style={starred ? { color: '#e6a700' } : undefined}
          />
          {starred ? t('session.unstar') : t('session.star')}
        </ContextMenuItem>
        <ContextMenuItem onClick={() => onRename(session)}>
          <Pencil className="size-4" />
          {t('common.rename')}
        </ContextMenuItem>
        <ContextMenuItem onClick={() => onDelete(session)} variant="destructive">
          <Trash2 className="size-4" />
          {t('common.delete')}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  )
}

function subAgentTitle(session: SessionInfo): string {
  if (session.role) return session.instance ? `${session.role}/${session.instance}` : session.role
  const raw = (session.label || '').trim()
  if (raw && raw !== 'default' && raw !== '默认会话') return session.label
  const parsed = parseAgentChatID(session.fullKey || session.agentChatID || session.chatID)
  if (parsed?.role) return parsed.instance ? `${parsed.role}/${parsed.instance}` : parsed.role
  return session.agentChatID || session.fullKey || session.chatID || 'SubAgent'
}

function relativeTime(
  lastActive: string,
  t: (k: string, params?: Record<string, string | number>) => string,
): string {
  const ts = Date.parse(lastActive)
  if (Number.isNaN(ts)) return ''
  const diff = Date.now() - ts
  const min = Math.floor(diff / 60_000)
  if (min < 1) return t('session.justNow')
  if (min < 60) return t('session.minutesAgo', { n: min })
  const hr = Math.floor(min / 60)
  if (hr < 24) return t('session.hoursAgo', { n: hr })
  const day = Math.floor(hr / 24)
  if (day < 30) return t('session.daysAgo', { n: day })
  return new Date(ts).toLocaleDateString()
}
