/**
 * SessionSidebar — the left session panel (Spec 3 §3.1).
 *
 * Replaces Spec 2's empty left-sidebar body for the "sessions" view.
 * Wires useSessionStore to the search box, category switcher, the list, and
 * the new-session dialog. Pure presentational composition on top of the store.
 */
import { useCallback, useMemo, useState } from 'react'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import { groupSessions, isSubAgentSession, parseAgentChatID, sameSession, sortSessions } from '@/lib/session-grouping'
import type { SessionCategory, SessionInfo, SessionSelector } from '@/types/shared'
import type { TabManager } from '@/hooks/useTabManager'
import { SessionSearch } from './SessionSearch'
import { SessionList } from './SessionList'
import { NewSessionDialog } from './NewSessionDialog'

const CATEGORIES = ['time', 'status', 'path'] as const

interface SessionSidebarProps {
  /** Tab manager for opening SubAgent conversation tabs (Child 5). */
  tabManager: TabManager
}

export function SessionSidebar({ tabManager }: SessionSidebarProps) {
  const { t } = useI18n()
  const store = useSessionStore()
  const [search, setSearch] = useState('')
  const [newOpen, setNewOpen] = useState(false)

  // Channel-filtered sessions
  const filteredSessions = useMemo(() => {
    if (!store.activeChannel) return store.sessions
    return store.sessions.filter((s) =>
      s.channel === store.activeChannel ||
      s.parentChannel === store.activeChannel ||
      (s.children || []).some((c) => c.parentChannel === store.activeChannel)
    )
  }, [store.sessions, store.activeChannel])

  // Re-derive groups and sortedSessions for filtered sessions
  const filteredGroups = useMemo(
    () => groupSessions(filteredSessions, store.category, store.starredIds),
    [filteredSessions, store.category, store.starredIds],
  )
  const filteredSorted = useMemo(
    () => sortSessions(filteredSessions, store.starredIds),
    [filteredSessions, store.starredIds],
  )

  // Unified select handler: SubAgent clicks open a new Agent tab; main session
  // clicks switch the active chatroom as before.
  const handleSelect = useCallback(
    (id: string, channel: string) => {
      const selector = { channel: channel || 'web', chatID: id }
      const matched = findSessionInTree(store.sessions, selector) ?? store.subAgents.find((sa) => sameSession(sa, selector))
      if (matched && isSubAgentSession(matched)) {
        const subAgent = withParsedAgentFields(matched)
        tabManager.openTab({
          type: 'agent',
          title: subAgentTitle(subAgent),
          icon: 'bot',
          closable: true,
          data: {
            subAgentRole: subAgent.role,
            subAgentInstance: subAgent.instance,
            parentChatID: subAgent.parentChatID,
            parentChannel: subAgent.parentChannel,
            agentChatID: subAgent.fullKey || subAgent.agentChatID,
          },
        })
      } else {
        void store.switchSession(id, channel)
      }
    },
    [store.sessions, store.subAgents, store.switchSession, tabManager],
  )

  return (
    <div className="flex h-full w-full flex-col bg-bg-secondary">
      {/* Header: title + new-session button */}
      <header
        className="flex h-9 shrink-0 items-center justify-between px-2"
        style={{ borderBottom: '1px solid var(--border)' }}
      >
        <span className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
          {store.activeChannel
            ? `${t(`channel.${store.activeChannel}`)} · ${t('sidebar.sessions')}`
            : t('sidebar.sessions')}
        </span>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t('session.newSession')}
              onClick={() => setNewOpen(true)}
            >
              <Plus />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{t('session.newSession')}</TooltipContent>
        </Tooltip>
      </header>

      {/* Category switcher */}
      <div
        className="flex shrink-0 items-center gap-0.5 px-2 py-1"
        style={{ borderBottom: '1px solid var(--border)' }}
      >
        {CATEGORIES.map((c) => {
          const active = store.category === c
          return (
            <button
              key={c}
              type="button"
              onClick={() => store.setCategory(c)}
              className="flex-1 rounded px-2 py-1 text-[11px] font-medium transition-colors"
              style={{
                backgroundColor: active ? 'var(--bg-tertiary)' : 'transparent',
                color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
              }}
            >
              {labelForCategory(c, t)}
            </button>
          )
        })}
      </div>

      {/* Search */}
      <div className="shrink-0">
        <SessionSearch value={search} onChange={setSearch} />
      </div>

      {/* List */}
      <div className="min-h-0 flex-1">
        {filteredSessions.length === 0 && store.activeChannel ? (
          <div className="flex h-full items-center justify-center px-4 text-center text-xs text-text-muted">
            {t('session.noSessionsForChannel', { channel: store.activeChannel })}
          </div>
        ) : (
        <SessionList
          sessions={filteredSessions}
          groups={filteredGroups}
          sortedSessions={filteredSorted}
          category={store.category}
          starredIds={store.starredIds}
          unreadIds={store.unreadIds}
          activeSession={store.activeSession}
          search={search}
          subAgents={store.subAgents}
          onSelect={handleSelect}
          onToggleStar={store.toggleStar}
          onRename={store.renameSession}
          onDelete={store.deleteSession}
        />
        )}
      </div>

      <NewSessionDialog
        open={newOpen}
        onOpenChange={setNewOpen}
        onCreate={store.createSession}
      />
    </div>
  )
}

function findSessionInTree(sessions: SessionInfo[], selector: SessionSelector): SessionInfo | null {
  for (const session of sessions) {
    if (sameSession(session, selector)) return session
    const child = findSessionInTree(session.children || [], selector)
    if (child) return child
  }
  return null
}

function withParsedAgentFields(session: SessionInfo): SessionInfo {
  const fullKey = session.fullKey || session.agentChatID || session.chatID
  const parsed = parseAgentChatID(fullKey)
  if (!parsed) return session
  return {
    ...session,
    role: parsed.role || session.role,
    instance: parsed.instance || session.instance,
    parentChatID: parsed.parentChatID || session.parentChatID,
    parentChannel: parsed.parentChannel || session.parentChannel,
    fullKey,
    agentChatID: session.agentChatID || fullKey,
  }
}

function subAgentTitle(session: SessionInfo): string {
  if (session.role) return session.instance ? `${session.role}/${session.instance}` : session.role
  const raw = (session.label || '').trim()
  if (raw && raw !== 'default' && raw !== '默认会话') return session.label
  const parsed = parseAgentChatID(session.fullKey || session.agentChatID || session.chatID)
  if (parsed?.role) return parsed.instance ? `${parsed.role}/${parsed.instance}` : parsed.role
  return session.agentChatID || session.fullKey || session.chatID || 'SubAgent'
}

function labelForCategory(
  c: SessionCategory,
  t: (k: string) => string,
): string {
  switch (c) {
    case 'time':
      return t('session.byTime')
    case 'status':
      return t('session.byStatus')
    case 'path':
      return t('session.byPath')
  }
}
