/**
 * SessionGroup — a titled bucket of sessions within a category (Spec 3 §3.2).
 *
 * Renders the translated group header (time / status) and its
 * sorted SessionItem children. Collapsible so long lists stay scannable.
 */
import { useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import { sameSession, sessionKey } from '@/lib/session-grouping'
import type { SessionCategory, SessionInfo, SessionSelector } from '@/types/shared'
import { SessionItem } from './SessionItem'
import { childrenForParent } from './session-tree'

interface SessionGroupProps {
  groupKey: string
  category: SessionCategory
  sessions: SessionInfo[]
  starredIds: string[]
  unreadIds: string[]
  activeSession: SessionSelector | null
  onSelect: (id: string, channel: string) => void
  onToggleStar: (id: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
}

export function SessionGroup({
  groupKey,
  category,
  sessions,
  starredIds,
  unreadIds,
  activeSession,
  onSelect,
  onToggleStar,
  onRename,
  onDelete,
}: SessionGroupProps) {
  const { t } = useI18n()
  const [open, setOpen] = useState(true)
  const title = groupTitle(groupKey, category, t)
  const starred = new Set(starredIds)
  const unreadSet = new Set(unreadIds)

  return (
    <section className="flex flex-col">
      {/* Group header — always shown for time/status/path categories */}
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1 px-2 py-1 text-[10px] font-semibold uppercase tracking-wide"
        style={{ color: 'var(--text-secondary)' }}
      >
        <ChevronRight className={cn('size-3 transition-transform', open && 'rotate-90')} />
        <span>{title}</span>
        <span className="font-normal" style={{ color: 'var(--text-muted)' }}>
          {sessions.length}
        </span>
      </button>
      {open && (
        <div className="flex flex-col gap-0.5">
          {sessions.map((s) => (
            <div key={sessionKey(s)} className="flex flex-col gap-0.5">
              <SessionItem
                session={s}
                starred={starred.has(sessionKey(s))}
                unread={unreadSet.has(sessionKey(s))}
                active={sameSession(activeSession, s)}
                onSelect={(id) => onSelect(id, s.channel)}
                onToggleStar={onToggleStar}
                onRename={onRename}
                onDelete={onDelete}
              />
              {/* Render SubAgent children (indented) for this parent session */}
              {childrenForParent(s).filter(isVisibleSubAgent).map((sa) => (
                <SubAgentTreeItem
                  key={sessionKey(sa)}
                  session={sa}
                  activeSession={activeSession}
                  depth={1}
                    onSelect={(id, channel) => onSelect(id, channel)}
                  onRename={onRename}
                  onDelete={onDelete}
                />
              ))}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

function SubAgentTreeItem({
  session,
  activeSession,
  depth,
  onSelect,
  onRename,
  onDelete,
}: {
  session: SessionInfo
  activeSession: SessionSelector | null
  depth: number
  onSelect: (id: string, channel: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
}) {
  return (
    <>
      <SessionItem
        session={session}
        starred={false}
        unread={false}
        active={sameSession(activeSession, session)}
        isSubAgent
        depth={depth}
        onSelect={(id) => onSelect(id, session.channel)}
        onToggleStar={() => undefined}
        onRename={onRename}
        onDelete={onDelete}
      />
      {childrenForParent(session).filter(isVisibleSubAgent).map((child) => (
        <SubAgentTreeItem
          key={sessionKey(child)}
          session={child}
          activeSession={activeSession}
          depth={depth + 1}
          onSelect={onSelect}
          onRename={onRename}
          onDelete={onDelete}
        />
      ))}
    </>
  )
}

function isVisibleSubAgent(session: SessionInfo): boolean {
  return session.running === true || session.status === 'running' || session.status === 'waiting_input' || session.status === 'pending'
}

function groupTitle(
  key: string,
  category: SessionCategory,
  t: (k: string, p?: Record<string, string | number>) => string,
): string {
  switch (category) {
    case 'time':
      return t(`time.${key}`)
    case 'status':
      return t(`session.status.${statusKey(key)}`)
    case 'path':
      return key === '__web__' ? t('session.webSessions') : key
  }
}

function statusKey(s: string): 'running' | 'waiting' | 'pending' | 'unread' | 'idle' | 'error' {
  if (s === 'waiting_input') return 'waiting'
  return s as 'running' | 'pending' | 'unread' | 'idle' | 'error'
}
