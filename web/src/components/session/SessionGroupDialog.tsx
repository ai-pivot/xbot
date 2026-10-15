import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useI18n } from '@/providers/i18n'
import { useSessionGroups } from '@/hooks/useSessionGroups'
import type { SessionCustomGroup } from '@/lib/session-custom-groups'
import type { SessionSelector } from '@/types/shared'

export interface GroupDialogTarget { group?: SessionCustomGroup; path?: string; sessions?: SessionSelector[] }
export function SessionGroupDialog({ target, onClose }: { target: GroupDialogTarget | null; onClose: () => void }) {
  const { t } = useI18n()
  const groups = useSessionGroups()
  const [name, setName] = useState('')
  const [dir, setDir] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  useEffect(() => {
    if (!target) return
    setName(target.group?.name ?? target.path?.split(/[\\/]/).filter(Boolean).pop() ?? '')
    setDir(target.group?.defaultWorkDir ?? target.path ?? '')
    setConfirmDelete(false)
  }, [target])
  const save = async () => {
    if (!target || !name.trim()) return
    setBusy(true)
    try {
      const result = target.group
        ? await groups.updateGroup(target.group.id, name.trim(), dir.trim())
        : target.sessions ? await groups.convertGroup(name.trim(), dir.trim(), target.sessions.map(({ channel, chatID }) => ({ channel, chatID }))) : await groups.createGroup(name.trim(), dir.trim())
      if (result) onClose()
    } finally { setBusy(false) }
  }
  const remove = async () => {
    if (!target?.group) return
    setBusy(true)
    try { if (await groups.deleteGroup(target.group.id)) onClose() } finally { setBusy(false) }
  }
  const disabled = busy || groups.loading || groups.busy || !!groups.error
  const reorder = async (delta: number) => {
    if (!target?.group || disabled) return
    const ids = groups.state.groups.map(group => group.id)
    const from = ids.indexOf(target.group.id); const to = from + delta
    if (from < 0 || to < 0 || to >= ids.length) return
    ids.splice(from, 1); ids.splice(to, 0, target.group.id)
    setBusy(true)
    try { await groups.reorderGroups(ids) } finally { setBusy(false) }
  }
  return <Dialog open={!!target} onOpenChange={open => { if (!open && !busy) onClose() }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader><DialogTitle>{t(target?.group ? 'session.editGroup' : target?.sessions ? 'session.convertGroup' : 'session.createGroup')}</DialogTitle><DialogDescription>{t('session.groupDirectoryHint')}</DialogDescription></DialogHeader>
      <Label htmlFor="group-name">{t('session.groupName')}</Label><Input id="group-name" value={name} disabled={disabled} onChange={e => setName(e.target.value)} />
      <Label htmlFor="group-dir">{t('session.groupDirectory')}</Label><Input id="group-dir" value={dir} disabled={disabled} onChange={e => setDir(e.target.value)} />
      {target?.group && <div className="flex gap-2">
        <Button variant="outline" disabled={disabled || groups.state.groups[0]?.id === target.group.id} onClick={() => void reorder(-1)}>{t('session.groupUp')}</Button>
        <Button variant="outline" disabled={disabled || groups.state.groups.at(-1)?.id === target.group.id} onClick={() => void reorder(1)}>{t('session.groupDown')}</Button>
      </div>}
      {groups.error && <p role="alert" className="text-sm text-red-500">{t('session.groupError')} <button type="button" disabled={busy || groups.loading || groups.busy} className="underline" onClick={() => void groups.refresh()}>{t('session.retryGroups')}</button></p>}
      {confirmDelete && <p role="alert" className="text-sm">{t('session.deleteGroupConfirm')}</p>}
      <DialogFooter>
        {target?.group && <Button variant="destructive" disabled={disabled} onClick={() => confirmDelete ? void remove() : setConfirmDelete(true)}>{t(confirmDelete ? 'common.confirm' : 'session.deleteGroup')}</Button>}
        <Button variant="outline" disabled={busy} onClick={onClose}>{t('common.cancel')}</Button>
        <Button disabled={disabled || !name.trim()} onClick={() => void save()}>{t('common.save')}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
