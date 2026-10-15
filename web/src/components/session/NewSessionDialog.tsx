/**
 * NewSessionDialog — create a new chatroom with an optional work path (Spec 3 §3.4).
 *
 * Flow on submit:
 *   1. POST /api/chats {label}       → chatID   (useSessionStore.createSession)
 *   2. WS RPC set_cwd {chat_id, dir}  → set working directory (if provided)
 *   3. active Agent panel switches its SSE target after createSession updates state
 * The dialog closes on success and reports via toast.
 */
import { useEffect, useState } from 'react'
import { X } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PathPicker } from '@/components/session/PathPicker'
import { toast } from 'sonner'
import { useI18n } from '@/providers/i18n'
import { loadRecentWorkDirs, removeRecentWorkDir } from '@/lib/recent-workdirs'

import { useSessionGroups } from '@/hooks/useSessionGroups'

interface NewSessionDialogProps {
  open: boolean
  initialWorkPath?: string
  initialGroupId?: string
  onOpenChange: (open: boolean) => void
  onCreate: (label?: string, workPath?: string, groupId?: string) => Promise<string | null>
}

export function NewSessionDialog({ open, initialWorkPath, initialGroupId, onOpenChange, onCreate }: NewSessionDialogProps) {
  const { t } = useI18n()
  const groups = useSessionGroups()
  const [groupId, setGroupId] = useState('')
  const [label, setLabel] = useState('')
  const [workPath, setWorkPath] = useState('')
  const [recentWorkDirs, setRecentWorkDirs] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  // Project entrypoints override recents; global creation keeps the latest applied path.
  useEffect(() => {
    if (open) {
      const recent = loadRecentWorkDirs()
      setLabel('')
      setRecentWorkDirs(recent)
      setGroupId(initialGroupId ?? '')
      const initialGroup = groups.state.groups.find(group => group.id === initialGroupId)
      setWorkPath(initialGroup?.defaultWorkDir || initialWorkPath || recent[0] || '/')
      setBusy(false)
    }
  }, [open, initialWorkPath, initialGroupId])

  const submit = async () => {
    if (busy || (groups.loading || groups.busy) || groups.error) return
    setBusy(true)
    const id = groupId
      ? await onCreate(label.trim() || undefined, workPath.trim() || undefined, groupId)
      : await onCreate(label.trim() || undefined, workPath.trim() || undefined)
    setBusy(false)
    if (id) {
      toast.success(t('session.created'))
      onOpenChange(false)
    } else {
      toast.error(t('session.createFailed'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('session.newSession')}</DialogTitle>
          <DialogDescription>{t('session.newSessionDesc')}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3 py-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="new-session-label">{t('session.nameLabel')}</Label>
            <Input
              id="new-session-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('session.namePlaceholder')}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="new-session-group">{t('session.group')}</Label>
          <select id="new-session-group" data-testid="new-session-group" disabled={busy || (groups.loading || groups.busy)} value={groupId} className="mb-3 w-full rounded-md border border-border bg-bg-primary p-2 text-sm" onChange={event => {
            const id = event.target.value
            setGroupId(id)
            const group = groups.state.groups.find(item => item.id === id)
            setWorkPath(group?.defaultWorkDir || recentWorkDirs[0] || '/')
          }}>
            <option value="">{t('session.automaticGroup')}</option>
            {groups.state.groups.map(group => <option key={group.id} value={group.id}>{group.name}</option>)}
          </select>
          {groups.loading && <p role="status" className="text-sm text-text-secondary">{t('session.groupsLoading')}</p>}
          {groups.error && <p role="alert" className="text-sm text-red-500">{t('session.groupError')} <button type="button" disabled={groups.loading || groups.busy} className="underline" onClick={() => void groups.refresh()}>{t('session.retryGroups')}</button></p>}
          <Label htmlFor="new-session-path">{t('session.workPath')}</Label>
            <PathPicker
              value={workPath}
              onChange={setWorkPath}
              compact
              onKeyDown={(e) => {
                if (e.key === 'Enter') void submit()
              }}
            />
            {recentWorkDirs.length > 0 ? (
              <div className="flex flex-wrap gap-1" aria-label={t('session.recentWorkPaths')}>
                {recentWorkDirs.map((path) => (
                  <div key={path} className="flex min-w-0 max-w-full items-center rounded-sm border border-border bg-bg-tertiary text-[11px]">
                    <button
                      type="button"
                      className="min-w-0 truncate px-1.5 py-0.5 text-left text-text-secondary hover:text-text-primary"
                      title={path}
                      onClick={() => setWorkPath(path)}
                    >
                      {path}
                    </button>
                    <button
                      type="button"
                      className="shrink-0 p-0.5 text-text-muted hover:text-text-primary"
                      aria-label={t('session.removeRecentPath', { path })}
                      onClick={() => setRecentWorkDirs(removeRecentWorkDir(path))}
                    >
                      <X className="size-3" />
                    </button>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        </div>
        <DialogFooter className="relative z-[60] bg-bg-primary">
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void submit()} disabled={busy || (groups.loading || groups.busy) || !!groups.error}>
            {busy ? t('common.loading') : t('common.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
