import { useEffect, useRef, useState } from 'react'
import { CheckSquare, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useI18n } from '@/providers/i18n'
import { sessionKey } from '@/lib/session-grouping'
import { downloadSessionsArchive, type ExportFormat } from '@/components/agent/api'
import type { SessionSelection } from '@/hooks/useSessionSelection'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

export function SessionMultiSelectButton({ selection }: { selection: SessionSelection }) {
  const { t } = useI18n()
  return <Button variant="ghost" size="icon-xs" aria-label={t('session.multiSelect')} title={t('session.multiSelect')}
    aria-pressed={selection.mode} disabled={selection.busy} onClick={selection.toggleMode}
    className={selection.mode ? 'bg-bg-tertiary text-accent' : ''}><CheckSquare /></Button>
}

/** Shared desktop/mobile actions. Every asynchronous operation captures the confirmed rows and scope. */
export function SessionBatchActions({ selection, store }: { selection: SessionSelection; store: SessionStore }) {
  const { t } = useI18n()
  const [dialog, setDialog] = useState<{ kind: 'delete' | 'export'; rows: SessionInfo[]; scope: string } | null>(null)
  const [format, setFormat] = useState<ExportFormat>('native')
  const [result, setResult] = useState<{ scope: string; success: number; failed: number } | null>(null)
  const pending = useRef(false)
  useEffect(() => {
    if (!pending.current && (!selection.mode || dialog?.scope !== selection.scope)) setDialog(null)
    if (!selection.mode || result?.scope !== selection.scope) setResult(null)
  }, [selection.mode, selection.scope])
  const run = async () => {
    if (!dialog || pending.current || dialog.scope !== selection.scope) return
    pending.current = true
    selection.lock(true)
    let succeeded: string[] = [], failed: string[] = []
    try {
      if (dialog.kind === 'export') ({ succeeded, failed } = await downloadSessionsArchive(dialog.rows, format))
      else {
        // Serial requests avoid triggering one refresh per session concurrently.
        for (const session of dialog.rows) {
          try {
            const ok = await store.deleteSession(session.chatID, session.channel || 'web')
            ;(ok ? succeeded : failed).push(sessionKey(session))
          } catch { failed.push(sessionKey(session)) }
        }
      }
    } catch { failed = dialog.rows.map(sessionKey) }
    finally {
      selection.keepOnly(failed, dialog.scope)
      setResult({ scope: dialog.scope, success: succeeded.length, failed: failed.length })
      setDialog(null)
      selection.lock(false)
      pending.current = false
    }
  }
  if (!selection.mode) return null
  const disabled = selection.busy || selection.selected.length === 0
  const currentDialog = dialog?.scope === selection.scope ? dialog : null
  const open = (kind: 'delete' | 'export') => { setFormat('native'); setDialog({ kind, rows: [...selection.selected], scope: selection.scope }) }
  const star = (value: boolean) => {
    selection.selected.forEach(s => { const key = sessionKey(s); if (store.starredIds.includes(key) !== value) store.toggleStar(key) })
  }
  return <div className="shrink-0 border-t border-border bg-bg-secondary px-2 py-2" data-testid="session-batch-actions" aria-busy={selection.busy}>
    <div className="flex flex-wrap items-center gap-1">
      <span className="mr-auto text-xs text-text-secondary" aria-live="polite">{t('session.selectedCount', { n: selection.ids.size })}</span>
      <Button size="sm" variant="ghost" disabled={selection.busy || !selection.rows.length} onClick={selection.all}>{t('session.selectAll')}</Button>
      <Button size="sm" variant="ghost" disabled={disabled} onClick={selection.clear}>{t('session.clearSelection')}</Button>
      <Button size="icon-xs" variant="ghost" aria-label={t('session.exitMultiSelect')} title={t('session.exitMultiSelect')} disabled={selection.busy} onClick={selection.exit}><X /></Button>
    </div>
    <p className="my-1 text-[11px] text-text-muted">{t(store.hasMore ? 'session.selectionLoadedOnly' : 'session.selectionVisibleOnly')}</p>
    <div className="flex flex-wrap gap-1">
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => star(true)}>{t('session.star')}</Button>
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => star(false)}>{t('session.unstar')}</Button>
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => selection.selected.forEach(s => store.markRead(sessionKey(s)))}>{t('session.markRead')}</Button>
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => open('export')}>{t('session.export')}</Button>
      <Button size="sm" variant="destructive" disabled={disabled} onClick={() => open('delete')}>{t('common.delete')}</Button>
    </div>
    {selection.busy && <p role="status" className="mt-1 text-xs text-text-secondary">{t('session.batchWorking')}</p>}
    {result?.scope === selection.scope && <p role="status" data-testid="session-batch-result" className="mt-1 text-xs text-text-secondary">{t('session.batchResult', { success: result.success, failed: result.failed })}</p>}
    <AlertDialog open={currentDialog?.kind === 'delete'} onOpenChange={value => { if (!value && !pending.current) setDialog(null) }}>
      <AlertDialogContent className="sm:max-w-sm">
        <AlertDialogHeader><AlertDialogTitle>{t('session.batchDeleteTitle')}</AlertDialogTitle><AlertDialogDescription>{t('session.batchDeleteConfirm', { n: currentDialog?.rows.length ?? 0 })}</AlertDialogDescription></AlertDialogHeader>
        <ul className="max-h-40 overflow-y-auto text-sm text-text-secondary">{currentDialog?.rows.map(s => <li key={sessionKey(s)} className="wrap-anywhere">{s.label || s.chatID} <span className="text-xs text-text-muted">({s.channel})</span></li>)}</ul>
        <AlertDialogFooter><AlertDialogCancel disabled={selection.busy}>{t('common.cancel')}</AlertDialogCancel>
          <AlertDialogAction disabled={selection.busy} onClick={e => { e.preventDefault(); void run() }}>{t('session.confirmBatchDelete')}</AlertDialogAction></AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    <Dialog open={currentDialog?.kind === 'export'} onOpenChange={value => { if (!value && !pending.current) setDialog(null) }}>
      <DialogContent className="sm:max-w-sm" onEscapeKeyDown={e => { if (pending.current) e.preventDefault() }} onPointerDownOutside={e => { if (pending.current) e.preventDefault() }}>
        <DialogHeader><DialogTitle>{t('session.batchExportTitle')}</DialogTitle><DialogDescription>{t('session.batchExportDescription')}</DialogDescription></DialogHeader>
        <label className="text-sm text-text-secondary">{t('session.exportFormat')}
          <select aria-label={t('session.exportFormat')} className="mt-2 block w-full rounded border border-border bg-bg-primary p-2" value={format} disabled={selection.busy} onChange={e => setFormat(e.target.value as ExportFormat)}>
            <option value="native">{t('session.exportNative')}</option><option value="openai">{t('session.exportOpenAI')}</option><option value="codex">{t('session.exportCodex')}</option>
          </select>
        </label>
        <DialogFooter><Button variant="ghost" disabled={selection.busy} onClick={() => setDialog(null)}>{t('common.cancel')}</Button><Button disabled={selection.busy} onClick={() => void run()}>{t('session.downloadZip')}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
}
