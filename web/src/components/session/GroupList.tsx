/**
 * GroupList — the "群组" tab of the sessions sidebar: which agents belong to
 * which group, and the place to change that.
 *
 * A group (peer group) is a durable named set of SESSIONS that may message each
 * other (`SendMessage(to="peer:<id>")`), so a member always corresponds to a row
 * in this very sidebar — that is why editing belongs here and not in a settings
 * page. Members whose session no longer exists are shown as stale instead of
 * being hidden: they are exactly what a user wants to clean up.
 */
import { useMemo, useState } from 'react'
import { Bot, Loader2, Plus, Trash2, Users, X } from 'lucide-react'

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
import { useGroups, PEER_GROUP_ID_PATTERN } from '@/hooks/useGroups'
import { useSessionStore } from '@/hooks/useSessionStore'
import { isSubAgentSession, sessionKey } from '@/lib/session-grouping'
import { flattenSubAgentTree } from '@/components/session/session-tree'
import { useI18n } from '@/providers/i18n'
import type { PeerGroup, SessionInfo } from '@/types/shared'

/** Split a peer-group session key ("channel:chatID") — chatID may contain ':'. */
export function parseSessionKey(key: string): { channel: string; chatID: string } | null {
  const sep = key.indexOf(':')
  if (sep <= 0 || sep === key.length - 1) return null
  return { channel: key.slice(0, sep), chatID: key.slice(sep + 1) }
}

/** Display name for a session: SubAgents read as role/instance. */
function sessionLabel(s: SessionInfo): string {
  if (isSubAgentSession(s)) {
    const role = s.role || s.label
    return s.instance ? `${role}/${s.instance}` : role
  }
  return s.label || s.chatID
}

export function GroupList() {
  const { t } = useI18n()
  const session = useSessionStore()
  const groups = useGroups()
  const [creating, setCreating] = useState(false)
  const [draftName, setDraftName] = useState('')
  const [nameError, setNameError] = useState<string | null>(null)
  const [pendingDelete, setPendingDelete] = useState<PeerGroup | null>(null)
  const [addingTo, setAddingTo] = useState<string | null>(null)
  const [pickerFilter, setPickerFilter] = useState('')

  // Every session the user can see (mains + SubAgents), keyed the same way the
  // group store keys members — so the picker offers exactly what can be added.
  const allSessions = useMemo(() => {
    const mains = session.sessions
    return [...mains, ...flattenSubAgentTree(mains)]
  }, [session.sessions])

  const byKey = useMemo(() => {
    const map = new Map<string, SessionInfo>()
    for (const s of allSessions) map.set(sessionKey(s), s)
    return map
  }, [allSessions])

  const submitCreate = async () => {
    const id = draftName.trim()
    if (!PEER_GROUP_ID_PATTERN.test(id)) {
      setNameError(t('session.groupNameInvalid'))
      return
    }
    await groups.createGroup(id)
    setDraftName('')
    setNameError(null)
    setCreating(false)
  }

  // Candidate members are TOP-LEVEL sessions — deliberately, not an oversight:
  // a peer group is addressed by session key ("channel:chatID") and the
  // notification pipeline only wakes a session that owns a chat worker. A
  // SubAgent inherits its parent's channel/chatID (so the backend never stores an
  // agent-key as a member) and is driven by its parent, so offering one here
  // would create a member nobody can reach.
  const candidates = (group: PeerGroup) => {
    const memberKeys = new Set(group.members.map((m) => m.session_key))
    const needle = pickerFilter.trim().toLowerCase()
    return session.sessions
      .filter((s) => !memberKeys.has(sessionKey(s)))
      .filter((s) => (needle ? sessionLabel(s).toLowerCase().includes(needle) || sessionKey(s).toLowerCase().includes(needle) : true))
  }

  return (
    <div className="flex flex-col gap-2 px-2 py-2 text-sm" data-testid="group-list">
      <div className="flex items-center gap-2">
        <span className="flex items-center gap-1 text-[11px] text-text-secondary">
          <Users className="size-3.5" />
          {t('session.groupCount', { count: groups.groups.length })}
        </span>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 px-2 text-xs"
          onClick={() => {
            setCreating((v) => !v)
            setNameError(null)
          }}
        >
          <Plus className="size-3.5" />
          {t('session.groupNew')}
        </Button>
      </div>

      {creating && (
        <div className="flex flex-col gap-1">
          <div className="flex gap-1">
            <Input
              autoFocus
              className="h-7 min-w-0 flex-1 text-xs"
              placeholder={t('session.groupNamePlaceholder')}
              value={draftName}
              onChange={(e) => setDraftName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void submitCreate()
                if (e.key === 'Escape') setCreating(false)
              }}
            />
            <Button size="sm" className="h-7 px-2 text-xs" onClick={() => void submitCreate()}>
              {t('session.groupCreate')}
            </Button>
          </div>
          {nameError && <p className="text-[11px] text-destructive">{nameError}</p>}
        </div>
      )}

      {groups.error && (
        <p className="rounded bg-destructive/10 px-2 py-1 text-[11px] text-destructive">{groups.error}</p>
      )}

      {groups.loading && groups.groups.length === 0 && (
        <div className="flex justify-center py-2">
          <Loader2 className="size-4 animate-spin text-text-muted" />
        </div>
      )}

      {!groups.loading && groups.groups.length === 0 && (
        <p className="px-1 py-2 text-xs text-text-muted">{t('session.groupEmpty')}</p>
      )}

      {groups.groups.map((group) => (
        <div key={group.id} data-testid="group-card" className="flex flex-col gap-1 rounded-md bg-bg-tertiary px-2 py-1.5">
          <div className="flex min-w-0 items-center gap-2">
            <span className="min-w-0 flex-1 truncate text-xs font-medium text-text-primary">{group.id}</span>
            <span className="shrink-0 text-[10px] text-text-muted">
              {t('session.groupMembers', { count: group.members.length })}
            </span>
            <button
              type="button"
              aria-label={t('session.groupDelete')}
              title={t('session.groupDelete')}
              className="flex size-5 shrink-0 items-center justify-center rounded text-text-muted transition-colors hover:bg-destructive/10 hover:text-destructive"
              onClick={() => setPendingDelete(group)}
            >
              <Trash2 className="size-3" />
            </button>
          </div>

          {group.members.length === 0 && <p className="text-[11px] text-text-muted">{t('session.groupNoMembers')}</p>}

          {group.members.map((member) => {
            const row = byKey.get(member.session_key)
            const stale = !row
            return (
              <div
                key={member.session_key}
                data-testid="group-member"
                data-stale={stale ? 'true' : undefined}
                className="flex min-w-0 items-center gap-1.5 rounded bg-bg-secondary/60 px-1.5 py-1"
              >
                <Bot className={'size-3.5 shrink-0 ' + (stale ? 'text-text-muted' : 'text-text-secondary')} />
                <div className="min-w-0 flex-1">
                  <p className={'truncate text-[11px] ' + (stale ? 'text-text-muted line-through' : 'text-text-primary')}>
                    {row ? sessionLabel(row) : member.name || member.session_key}
                  </p>
                  <p className="truncate font-mono text-[10px] text-text-muted">
                    {stale ? t('session.groupStale') : member.session_key}
                  </p>
                </div>
                <button
                  type="button"
                  aria-label={t('session.groupRemoveMember')}
                  title={t('session.groupRemoveMember')}
                  data-testid="group-member-remove"
                  className="flex size-5 shrink-0 items-center justify-center rounded text-text-muted transition-colors hover:bg-bg-hover hover:text-text-primary"
                  onClick={() => void groups.removeMember(group.id, member.session_key)}
                >
                  <X className="size-3" />
                </button>
              </div>
            )
          })}

          <div className="flex flex-col gap-1">
            <button
              type="button"
              data-testid="group-add-member"
              className="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left text-[11px] text-text-secondary transition-colors hover:bg-bg-hover hover:text-text-primary"
              onClick={() => {
                setPickerFilter('')
                setAddingTo(addingTo === group.id ? null : group.id)
              }}
            >
              <Plus className="size-3" />
              {t('session.groupAddMember')}
            </button>

            {addingTo === group.id && (
              <div className="flex flex-col gap-1 rounded bg-bg-secondary/70 p-1">
                <Input
                  className="h-6 text-[11px]"
                  placeholder={t('session.groupSearchAgent')}
                  value={pickerFilter}
                  onChange={(e) => setPickerFilter(e.target.value)}
                />
                <div className="max-h-40 overflow-y-auto overscroll-contain">
                  {candidates(group).length === 0 && (
                    <p className="px-1 py-1 text-[11px] text-text-muted">{t('session.groupNoCandidates')}</p>
                  )}
                  {candidates(group).map((candidate) => (
                    <button
                      key={sessionKey(candidate)}
                      type="button"
                      data-testid="group-candidate"
                      className="flex w-full min-w-0 flex-col rounded px-1 py-0.5 text-left transition-colors hover:bg-bg-hover"
                      onClick={() => void groups.addMember(group.id, sessionKey(candidate), sessionLabel(candidate))}
                    >
                      <span className="truncate text-[11px] text-text-primary">{sessionLabel(candidate)}</span>
                      <span className="truncate font-mono text-[10px] text-text-muted">{sessionKey(candidate)}</span>
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      ))}

      <p className="px-1 pt-1 text-[10px] leading-relaxed text-text-muted">{t('session.groupHint')}</p>

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('session.groupDeleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('session.groupDeleteMessage', { id: pendingDelete?.id ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('session.groupCancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const target = pendingDelete
                setPendingDelete(null)
                if (target) void groups.deleteGroup(target.id)
              }}
            >
              {t('session.groupDelete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
