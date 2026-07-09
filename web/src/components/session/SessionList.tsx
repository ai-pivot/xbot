/**
 * SessionList — the scrollable session list body (Spec 3 §3.2 / §3.6 / §3.7).
 *
 * Behavior:
 *   - No search query → render category groups (SessionGroup × N).
 *   - Search query → flat sorted result list, ignoring groups.
 *   - Empty sessions / no search match → SessionEmptyState.
 *   - Each SessionItem owns its own context menu; rename & delete open
 *     dialogs managed here so a single dialog instance serves every row.
 */
import { useMemo, useState } from 'react'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useI18n } from '@/providers/i18n'
import type { SessionCategory, SessionInfo, SessionSelector } from '@/types/shared'
import { SessionGroup } from './SessionGroup'
import { SessionItem } from './SessionItem'
import { SessionEmptyState } from './SessionEmptyState'
import { isSubAgentSession, sortSessions } from '@/lib/session-grouping'
import { sameSession, sessionKey } from '@/lib/session-grouping'
import { childrenForParent } from './session-tree'

interface SessionListProps {
  sessions: SessionInfo[]
  groups: { key: string; sessions: SessionInfo[] }[]
  sortedSessions: SessionInfo[]
  category: SessionCategory
  starredIds: string[]
  activeSession: SessionSelector | null
  search: string
  /** Deprecated compatibility prop. The canonical source is sessions[].children. */
  subAgents: SessionInfo[]
  onSelect: (id: string, channel: string) => void
  onToggleStar: (id: string) => void
  onRename: (id: string, channel: string, label: string) => Promise<boolean>
  onDelete: (id: string, channel: string) => Promise<boolean>
}

type DialogState = { id: string; channel: string; label: string } | null

export function SessionList({
  sessions,
  groups,
  sortedSessions,
  category,
  starredIds,
  activeSession,
  search,
  onSelect,
  onToggleStar,
  onRename,
  onDelete,
}: SessionListProps) {
  const { t } = useI18n()
  const [rename, setRename] = useState<DialogState>(null)
  const [del, setDelete] = useState<DialogState>(null)
  const [renameDraft, setRenameDraft] = useState('')
  const [busy, setBusy] = useState(false)

  const mainSessions = useMemo(() => sessions.filter((s) => !isSubAgentSession(s) && !s.synthetic), [sessions])
  const mainSortedSessions = useMemo(
    () => sortedSessions.filter((s) => !isSubAgentSession(s) && !s.synthetic),
    [sortedSessions],
  )
  const mainGroups = useMemo(
    () => groups.map((g) => ({ ...g, sessions: g.sessions.filter((s) => !isSubAgentSession(s) && !s.synthetic) }))
      .filter((g) => g.sessions.length > 0),
    [groups],
  )
  const query = search.trim().toLowerCase()
  const matchesQuery = (s: SessionInfo) =>
    s.label.toLowerCase().includes(query) || s.preview.toLowerCase().includes(query)

  // Search returns main sessions only. If a SubAgent matches, include its
  // parent main session so the SubAgent still renders under that parent.
  const searchResults = useMemo(() => {
    if (!query) return mainSortedSessions
    const main = mainSessions.filter((s) => matchesQuery(s) || matchesSelfOrDescendant(s, matchesQuery))
    return sortSessions(main, starredIds)
  }, [query, mainSortedSessions, mainSessions, starredIds])

  const searching = !!query
  const emptyList = mainSessions.length === 0
  const showEmpty = searching ? searchResults.length === 0 : emptyList

  const childrenForSearch = (parent: SessionInfo): SessionInfo[] => {
    const children = childrenForParent(parent).filter(isVisibleSubAgent)
    if (!searching) return children
    if (matchesQuery(parent)) return children
    return children.filter((child) => matchesSelfOrDescendant(child, matchesQuery))
  }

  const openRename = (s: SessionInfo) => {
    setRename({ id: s.chatID, channel: s.channel, label: s.label || s.chatID })
    setRenameDraft(s.label)
  }
  const openDelete = (s: SessionInfo) => setDelete({ id: s.chatID, channel: s.channel, label: s.label || s.chatID })

  const selectChannel = (s: SessionInfo) => onSelect(s.chatID, s.channel)

  const submitRename = async () => {
    if (!rename) return
    const label = renameDraft.trim()
    if (!label) return
    setBusy(true)
    const ok = await onRename(rename.id, rename.channel, label)
    setBusy(false)
    if (ok) setRename(null)
  }

  const submitDelete = async () => {
    if (!del) return
    setBusy(true)
    await onDelete(del.id, del.channel)
    setBusy(false)
    setDelete(null)
  }

  return (
    <div className="flex h-full flex-col">
      <ScrollArea className="min-h-0 flex-1">
        {showEmpty ? (
          <SessionEmptyState emptyList={emptyList} />
        ) : searching ? (
          <div className="flex min-w-64 flex-col gap-0.5 p-1">
            {searchResults.map((s) => (
              <div key={sessionKey(s)} className="flex flex-col gap-0.5">
                <SessionItem
                  session={s}
                  starred={starredIds.includes(sessionKey(s))}
                  active={sameSession(activeSession, s)}
                  onSelect={() => selectChannel(s)}
                  onToggleStar={onToggleStar}
                  onRename={openRename}
                  onDelete={openDelete}
                />
                {childrenForSearch(s).map((sa) => (
                  <SubAgentSearchItem
                    key={sessionKey(sa)}
                    session={sa}
                    activeSession={activeSession}
                    depth={1}
                    searching={searching}
                    matchesQuery={matchesQuery}
                    onSelect={onSelect}
                    onRename={openRename}
                    onDelete={openDelete}
                  />
                ))}
              </div>
            ))}
          </div>
        ) : (
          <div className="flex min-w-64 flex-col gap-1 p-1">
            {mainGroups.map((g) => (
              <SessionGroup
                key={g.key}
                groupKey={g.key}
                category={category}
                sessions={g.sessions}
                starredIds={starredIds}
                activeSession={activeSession}
                onSelect={onSelect}
                onToggleStar={onToggleStar}
                onRename={openRename}
                onDelete={openDelete}
              />
            ))}
          </div>
        )}
      </ScrollArea>

      {/* Rename dialog */}
      <Dialog open={rename !== null} onOpenChange={(o) => !o && setRename(null)}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('common.rename')}</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={renameDraft}
            onChange={(e) => setRenameDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void submitRename()
            }}
            aria-label={t('session.nameLabel')}
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRename(null)} disabled={busy}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void submitRename()} disabled={busy || !renameDraft.trim()}>
              {t('common.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <AlertDialog open={del !== null} onOpenChange={(o) => !o && setDelete(null)}>
        <AlertDialogContent className="sm:max-w-sm">
          <AlertDialogHeader>
            <AlertDialogTitle>{t('session.deleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('session.deleteConfirm', { name: del?.label ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                void submitDelete()
              }}
              disabled={busy}
            >
              {t('common.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function SubAgentSearchItem({
  session,
  activeSession,
  depth,
  searching,
  matchesQuery,
  onSelect,
  onRename,
  onDelete,
}: {
  session: SessionInfo
  activeSession: SessionSelector | null
  depth: number
  searching: boolean
  matchesQuery: (s: SessionInfo) => boolean
  onSelect: (id: string, channel: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
}) {
  const children = childrenForParent(session)
    .filter(isVisibleSubAgent)
    .filter((child) => !searching || matchesQuery(session) || matchesSelfOrDescendant(child, matchesQuery))
  return (
    <>
      <SessionItem
        session={session}
        starred={false}
        active={sameSession(activeSession, session)}
        isSubAgent
        depth={depth}
        onSelect={(id) => onSelect(id, session.channel)}
        onToggleStar={() => undefined}
        onRename={onRename}
        onDelete={onDelete}
      />
      {children.map((child) => (
        <SubAgentSearchItem
          key={sessionKey(child)}
          session={child}
          activeSession={activeSession}
          depth={depth + 1}
          searching={searching}
          matchesQuery={matchesQuery}
          onSelect={onSelect}
          onRename={onRename}
          onDelete={onDelete}
        />
      ))}
    </>
  )
}

function matchesSelfOrDescendant(
  session: SessionInfo,
  matches: (s: SessionInfo) => boolean,
  seen = new Set<string>(),
): boolean {
  const key = sessionKey(session)
  if (seen.has(key)) return false
  seen.add(key)
  if (matches(session)) return true
  return childrenForParent(session).filter(isVisibleSubAgent).some((child) => matchesSelfOrDescendant(child, matches, seen))
}

function isVisibleSubAgent(session: SessionInfo): boolean {
  return session.running === true || session.status === 'running' || session.status === 'waiting_input' || session.status === 'pending'
}
