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
import { Star, Pencil, Trash2, Bot, GitBranch, Loader2, ExternalLink, Check, Download, GitFork } from 'lucide-react'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import i18n from '@/i18n'
import { useIsTouch } from '@/hooks/useIsMobile'
import { parseAgentChatID, sessionKey } from '@/lib/session-grouping'
import type { SessionInfo, SessionStatus } from '@/types/shared'
import type { ExportFormat } from '@/components/agent/api'

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
  /** Fork the session: copy its conversation context into a new session. */
  onFork?: (session: SessionInfo) => void
  /** Export the session in the given format. */
  onExport?: (session: SessionInfo, format: ExportFormat) => void
  /** Multi-select mode: show checkbox, click toggles selection. */
  multiSelectMode?: boolean
  /** Whether this item is currently selected in multi-select mode. */
  selected?: boolean
  /** Toggle selection (key, shiftKey) — shiftKey enables range select. */
  onToggleSelect?: (key: string, shiftKey: boolean) => void
  /** Drag-and-drop reorder: called when this item is dragged over another. */
  onDragStartItem?: (key: string) => void
  onDropItem?: (targetKey: string) => void
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
  onFork,
  onExport,
  multiSelectMode = false,
  selected = false,
  onToggleSelect,
  onDragStartItem,
  onDropItem,
}: SessionItemProps) {
  const { t } = useI18n()
  const isTouch = useIsTouch()
  const key = sessionKey(session)
  const title = isSubAgent ? subAgentTitle(session) : (session.label || session.chatID)
  const executing = session.running === true || session.status === 'running' || session.status === 'pending'

  const openInBrowserTab = useCallback(() => {
    const sessionParam = `${session.channel || 'web'}:${session.chatID}`
    const url = `${window.location.origin}/?session=${encodeURIComponent(sessionParam)}`
    window.open(url, '_blank')
  }, [session])

  const row = (
    <div
      role="button"
      tabIndex={0}
      draggable={!isSubAgent && !multiSelectMode && !session.synthetic && !!onDragStartItem}
      onDragStart={(e) => {
        if (!onDragStartItem) return
        onDragStartItem(key)
        e.dataTransfer.effectAllowed = 'move'
      }}
      onDragOver={(e) => {
        if (!onDropItem) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDrop={(e) => {
        if (!onDropItem) return
        e.preventDefault()
        onDropItem(key)
      }}
      onClick={(e) => {
        if (session.synthetic) return
        if (multiSelectMode && onToggleSelect && !isSubAgent) {
          onToggleSelect(key, e.shiftKey)
        } else {
          onSelect(session.chatID)
        }
      }}
      onKeyDown={(e) => {
        if (session.synthetic) return
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          if (multiSelectMode && onToggleSelect && !isSubAgent) {
            onToggleSelect(key, e.shiftKey)
          } else {
            onSelect(session.chatID)
          }
        }
      }}
      className={cn(
        'group flex w-full items-center gap-2 rounded-xl px-2.5 py-2.5 text-left transition-spring active:scale-[0.985] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50',
        active && !multiSelectMode ? '' : !session.synthetic && 'hover:bg-bg-tertiary/60',
        session.synthetic && 'cursor-default opacity-80',
        selected && 'bg-accent/15 ring-1 ring-accent/40',
      )}
      style={{
        ...(isSubAgent ? { marginLeft: `${depth}rem` } : {}),
        // 布局 v2（设计稿 1:1）：active 会话 = accent 14% 底 + accent 左条；
        // 非会话项常态带透明左条，避免选中时布局跳动。
        ...(active && !multiSelectMode
          ? {
              // 未来感：渐变底（左浓右淡）+ accent 内发光，而非纯色块
              background:
                'linear-gradient(90deg, color-mix(in srgb, var(--accent) 22%, transparent) 0%, color-mix(in srgb, var(--accent) 6%, transparent) 100%)',
              borderLeft: '2px solid var(--accent)',
              boxShadow: 'inset 0 0 18px -10px var(--accent)',
            }
          : { borderLeft: '2px solid transparent' }),
        ...(unread && !isSubAgent && !active ? {
          backgroundColor: 'color-mix(in srgb, var(--accent) 10%, transparent)',
          boxShadow: 'inset 2px 0 var(--accent), inset 0 0 12px -8px var(--accent)',
        } : {}),
      }}
    >
      {multiSelectMode && !isSubAgent && !session.synthetic ? (
        /* Multi-select checkbox */
        <span
          className={cn(
            'flex size-3.5 shrink-0 items-center justify-center rounded border transition-colors',
            selected
              ? 'border-accent bg-accent text-white'
              : 'border-border-muted bg-transparent',
          )}
          aria-hidden
        >
          {selected && <Check className="size-2.5" strokeWidth={3} />}
        </span>
      ) : executing ? (
        <Loader2
          className="size-2.5 shrink-0 animate-spin"
          style={{ color: isSubAgent ? 'var(--accent)' : 'var(--status-running)' }}
          aria-label={t(`session.status.${session.status === 'pending' ? 'pending' : 'running'}`)}
        />
      ) : isSubAgent ? (
        <Bot
          className="size-3.5 shrink-0"
          style={{ color: 'var(--text-muted)' }}
        />
      ) : session.synthetic ? (
        <GitBranch className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
      ) : (
        /* Other statuses: static colored dot（运行中加呼吸光晕——未来感） */
        <span
          className={`size-2 shrink-0 rounded-full ${session.status === 'running' ? 'animate-pulse' : ''}`}
          style={{
            backgroundColor: STATUS_COLOR[session.status],
            ...(session.status === 'running'
              ? { boxShadow: `0 0 8px 0 ${STATUS_COLOR[session.status]}` }
              : {}),
          }}
          title={t(`session.status.${session.status === 'waiting_input' ? 'waiting' : session.status}`)}
          aria-hidden
        />
      )}

      {/* Star toggle (hover/starred) — hidden for SubAgents and multi-select */}
      {!isSubAgent && !session.synthetic && !multiSelectMode && (
        <button
          type="button"
          aria-label={starred ? t('session.unstar') : t('session.star')}
          onClick={(e) => {
            e.stopPropagation()
            onToggleStar(key)
          }}
          className={cn(
            'shrink-0 rounded p-0.5 transition-spring',
            starred ? 'opacity-100' : isTouch ? 'opacity-60' : 'opacity-0 group-hover:opacity-100',
          )}
          style={starred ? { color: '#e6a700' } : { color: 'var(--text-muted)' }}
        >
          <Star className="size-3.5" fill={starred ? 'currentColor' : 'none'} />
        </button>
      )}

      {/* Title */}
      <span
        className={cn('min-w-0 flex-1 truncate text-xs', unread && !isSubAgent ? 'font-semibold' : 'font-medium')}
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

      {/* Relative time — hidden for SubAgents */}
      {!isSubAgent && !session.synthetic && (
        <span className="shrink-0 text-[10px] tabular-nums" style={{ color: 'var(--text-muted)' }}>
          {relativeTime(session.lastActive, t)}
        </span>
      )}
    </div>
  )

  // In multi-select mode, no context menu — just the row
  if (multiSelectMode) return row

  // SubAgent items: context menu with only "open in tab"
  if (isSubAgent || session.synthetic) {
    return (
     <ContextMenu>
       <ContextMenuTrigger asChild>{row}</ContextMenuTrigger>
       <ContextMenuContent className="data-[state=open]:animate-none data-[state=closed]:animate-none">
          <ContextMenuItem onSelect={openInBrowserTab}>
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
          <ContextMenuItem onSelect={openInBrowserTab}>
          <ExternalLink className="size-4" />
          {t('session.openInTab')}
        </ContextMenuItem>
        <ContextMenuItem onSelect={() => onToggleStar(key)}>
          <Star
            className="size-4"
            fill={starred ? 'currentColor' : 'none'}
            style={starred ? { color: '#e6a700' } : undefined}
          />
          {starred ? t('session.unstar') : t('session.star')}
        </ContextMenuItem>
        <ContextMenuItem onSelect={() => onRename(session)}>
          <Pencil className="size-4" />
          {t('common.rename')}
        </ContextMenuItem>
        {onFork && !isSubAgent && (
          <ContextMenuItem onSelect={() => onFork(session)}>
            <GitFork className="size-4" />
            {t('session.fork')}
          </ContextMenuItem>
        )}
        {onExport && (
          <ContextMenuSub>
            <ContextMenuSubTrigger>
              <Download className="size-4" />
              {t('session.export')}
            </ContextMenuSubTrigger>
            <ContextMenuSubContent>
              <ContextMenuItem onSelect={() => onExport(session, 'native')}>
                {t('session.exportNative')}
              </ContextMenuItem>
              <ContextMenuItem onSelect={() => onExport(session, 'openai')}>
                {t('session.exportOpenAI')}
              </ContextMenuItem>
              <ContextMenuItem onSelect={() => onExport(session, 'codex')}>
                {t('session.exportCodex')}
              </ContextMenuItem>
            </ContextMenuSubContent>
          </ContextMenuSub>
        )}
        <ContextMenuSeparator />
        <ContextMenuItem onSelect={() => onDelete(session)} variant="destructive">
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
  return new Date(ts).toLocaleDateString(i18n.language || 'zh-CN')
}
