/**
 * BlackboardPanel — the shared blackboard of the current session.
 *
 * The board is the cross-agent workspace: a main agent, its SubAgents and any
 * session that joined the same named board see (and claim) the same entries.
 * This panel is the human's window into it: it lists what is actionable
 * (ready), what is taken (with the lease countdown) and what is waiting on a
 * dependency — and it lets the operator write, close, unstick or delete an
 * entry with the same guarantees agents get (revision CAS, lease semantics).
 *
 * Rendering rules follow the project's UI contracts: state is carried by color
 * (yellow/red reserved for failure), long tokens wrap instead of stretching the
 * panel, and every countdown is computed locally from the lease expiry.
 */
import { useState } from 'react'

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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { useBlackboard } from '@/hooks/useBlackboard'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import type { BlackboardEntry } from '@/types/shared'

/** Lease countdown, computed from the expiry (never polled). */
function leaseRemaining(entry: BlackboardEntry, now: number): string {
  const ms = (entry.claim_expires_at ?? 0) - now
  if (ms <= 0) return '0s'
  const totalSeconds = Math.floor(ms / 1000)
  if (totalSeconds >= 60) {
    const minutes = Math.floor(totalSeconds / 60)
    return `${minutes}m${String(totalSeconds % 60).padStart(2, '0')}s`
  }
  return `${totalSeconds}s`
}

function updatedLabel(entry: BlackboardEntry, now: number, locale: string): string {
  const seconds = Math.max(0, Math.floor((now - entry.updated_at) / 1000))
  return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-seconds, 'second')
}

export function BlackboardPanel() {
  const { t, locale } = useI18n()
  const session = useSessionStore()
  const bb = useBlackboard(session.activeSession)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState({ key: '', kind: 'task', title: '', body: '' })
  const [pendingDelete, setPendingDelete] = useState<BlackboardEntry | null>(null)

  const ready = bb.entries.filter((e) => e.ready && !e.closed).length
  const claimed = bb.entries.filter((e) => !e.closed && e.claimed_by).length
  const blocked = bb.entries.filter((e) => !e.closed && e.blocked).length
  const open = bb.entries.filter((e) => !e.closed).length

  const submitDraft = async () => {
    if (!draft.key.trim() || !draft.title.trim()) return
    await bb.post({ key: draft.key.trim(), kind: draft.kind.trim(), title: draft.title.trim(), body: draft.body })
    setDraft({ key: '', kind: 'task', title: '', body: '' })
    setAdding(false)
  }

  const toggleBody = async (entry: BlackboardEntry) => {
    if (expanded === entry.key) {
      setExpanded(null)
      return
    }
    setExpanded(entry.key)
    if (bb.bodies[entry.key] === undefined) await bb.loadBody(entry.key)
  }

  return (
    <div className="h-full overflow-y-auto overflow-x-hidden overscroll-contain">
      <div className="flex flex-col gap-3 px-3 py-3 text-sm">
        {/* Board picker + derived counts */}
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label={t('blackboard.boardLabel')}
            className="h-7 min-w-0 flex-1 rounded-md border border-input bg-transparent px-2 text-xs"
            value={bb.selectedBoard}
            onChange={(e) => bb.selectBoard(e.target.value)}
          >
            <option value="">{bb.board || t('blackboard.sessionBoard')}</option>
            {bb.boards
              .filter((b) => b.board !== bb.board)
              .map((b) => (
                <option key={b.board} value={b.board}>
                  {b.board} ({b.open})
                </option>
              ))}
          </select>
          <Button size="sm" variant="outline" className="h-7 px-2 text-xs" onClick={bb.refresh}>
            {t('blackboard.refresh')}
          </Button>
          <Button size="sm" className="h-7 px-2 text-xs" onClick={() => setAdding((v) => !v)}>
            {adding ? t('blackboard.cancel') : t('blackboard.add')}
          </Button>
        </div>

        <div
          data-testid="blackboard-stats"
          className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-text-secondary"
        >
          <span>{t('blackboard.statReady', { count: ready })}</span>
          <span>{t('blackboard.statClaimed', { count: claimed })}</span>
          <span>{t('blackboard.statBlocked', { count: blocked })}</span>
          <span>{t('blackboard.statOpen', { count: open })}</span>
          <code className="ml-auto truncate text-[10px] opacity-70">{bb.board}</code>
        </div>

        {adding && (
          <div className="flex flex-col gap-2 rounded-md border border-border bg-bg-tertiary p-2">
            <div className="flex gap-2">
              <Input
                className="h-7 text-xs"
                placeholder={t('blackboard.fieldKey')}
                value={draft.key}
                onChange={(e) => setDraft({ ...draft, key: e.target.value })}
              />
              <Input
                className="h-7 w-24 text-xs"
                placeholder={t('blackboard.fieldKind')}
                value={draft.kind}
                onChange={(e) => setDraft({ ...draft, kind: e.target.value })}
              />
            </div>
            <Input
              className="h-7 text-xs"
              placeholder={t('blackboard.fieldTitle')}
              value={draft.title}
              onChange={(e) => setDraft({ ...draft, title: e.target.value })}
            />
            <Textarea
              className="min-h-[3rem] text-xs"
              placeholder={t('blackboard.fieldBody')}
              value={draft.body}
              onChange={(e) => setDraft({ ...draft, body: e.target.value })}
            />
            <div className="flex justify-end">
              <Button size="sm" className="h-7 px-3 text-xs" onClick={() => void submitDraft()}>
                {t('blackboard.submit')}
              </Button>
            </div>
          </div>
        )}

        {bb.error && <div className="rounded-md bg-destructive/10 px-2 py-1 text-xs text-destructive">{bb.error}</div>}

        {bb.entries.length === 0 && !bb.loading && (
          <p className="px-1 py-2 text-xs text-text-secondary">{t('blackboard.empty')}</p>
        )}

        {bb.entries.map((entry) => {
          const isExpanded = expanded === entry.key
          const state = entry.closed
            ? t('blackboard.stateClosed')
            : entry.blocked
              ? t('blackboard.stateBlocked')
              : entry.claimed_by
                ? t('blackboard.stateClaimed')
                : t('blackboard.stateReady')
          return (
            <div
              key={entry.key}
              data-testid="blackboard-entry"
              data-state={entry.closed ? 'closed' : entry.blocked ? 'blocked' : entry.claimed_by ? 'claimed' : 'ready'}
              className="flex min-w-0 flex-col gap-1 rounded-md bg-bg-tertiary px-2 py-1.5"
            >
              <div className="flex min-w-0 items-center gap-2">
                {/* 分类色只留在左侧状态条；成功/关闭一律中性前景色 */}
                <span
                  aria-hidden
                  className={
                    'h-4 w-[3px] shrink-0 rounded-sm ' +
                    (entry.closed
                      ? 'bg-muted-foreground/40'
                      : entry.blocked
                        ? 'bg-destructive/70'
                        : entry.claimed_by
                          ? 'bg-amber-500/70'
                          : 'bg-emerald-500/70')
                  }
                />
                <span className="shrink-0 text-[10px] uppercase text-text-secondary">{state}</span>
                {entry.kind && (
                  <Badge variant="secondary" className="shrink-0 px-1 py-0 text-[10px] font-normal">
                    {entry.kind}
                  </Badge>
                )}
                <span className="min-w-0 flex-1 truncate font-medium">{entry.title}</span>
                <span className="shrink-0 text-[10px] text-text-secondary">rev {entry.revision}</span>
              </div>

              <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-secondary">
                <code className="truncate opacity-80">{entry.key}</code>
                {entry.claimed_by && (
                  <span className="text-amber-600 dark:text-amber-400">
                    ◐ {entry.claimed_by} · {leaseRemaining(entry, bb.now)}
                  </span>
                )}
                {entry.blocked && entry.blocked_by?.length ? (
                  <span className="text-destructive">⛔ {entry.blocked_by.join(', ')}</span>
                ) : null}
                <span className="ml-auto shrink-0 opacity-70">{updatedLabel(entry, bb.now, locale)}</span>
              </div>

              {isExpanded && (
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-bg-secondary p-2 text-[11px]">
                  {bb.bodies[entry.key] === undefined ? t('blackboard.loading') : bb.bodies[entry.key] || t('blackboard.noBody')}
                </pre>
              )}

              <div className="flex flex-wrap items-center gap-1">
                <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]" onClick={() => void toggleBody(entry)}>
                  {isExpanded ? t('blackboard.collapse') : t('blackboard.expand')}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-6 px-2 text-[11px]"
                  onClick={() => void bb.setClosed(entry.key, !entry.closed)}
                >
                  {entry.closed ? t('blackboard.reopen') : t('blackboard.close')}
                </Button>
                {entry.claimed_by && (
                  <Button size="sm" variant="ghost" className="h-6 px-2 text-[11px]" onClick={() => void bb.release(entry.key)}>
                    {t('blackboard.release')}
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  className="ml-auto h-6 px-2 text-[11px] text-destructive"
                  onClick={() => setPendingDelete(entry)}
                >
                  {t('blackboard.delete')}
                </Button>
              </div>
            </div>
          )
        })}
      </div>

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('blackboard.deleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('blackboard.deleteMessage', { key: pendingDelete?.key ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('blackboard.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const target = pendingDelete
                setPendingDelete(null)
                if (target) void bb.remove(target.key)
              }}
            >
              {t('blackboard.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
