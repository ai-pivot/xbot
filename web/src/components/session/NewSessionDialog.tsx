/**
 * NewSessionDialog — create a new chatroom with an optional work path (Spec 3 §3.4).
 *
 * Flow on submit:
 *   1. POST /api/chats {label}       → chatID   (useSessionStore.createSession)
 *   2. WS RPC set_cwd {chat_id, dir}  → set working directory (if provided)
 *   3. ws.subscribe(chatID) + switch  → handled by createSession
 * The dialog closes on success and reports via toast.
 */
import { useEffect, useState } from 'react'
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

interface NewSessionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreate: (label?: string, workPath?: string) => Promise<string | null>
}

export function NewSessionDialog({ open, onOpenChange, onCreate }: NewSessionDialogProps) {
  const { t } = useI18n()
  const [label, setLabel] = useState('')
  const [workPath, setWorkPath] = useState('')
  const [busy, setBusy] = useState(false)

  // Reset the form whenever the dialog opens; seed workPath with '/'.
  useEffect(() => {
    if (open) {
      setLabel('')
      setWorkPath('/')
      setBusy(false)
    }
  }, [open])

  const submit = async () => {
    setBusy(true)
    const id = await onCreate(label.trim() || undefined, workPath.trim() || undefined)
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
            <Label htmlFor="new-session-path">{t('session.workPath')}</Label>
            <PathPicker
              value={workPath}
              onChange={setWorkPath}
              compact
              onKeyDown={(e) => {
                if (e.key === 'Enter') void submit()
              }}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? t('common.loading') : t('common.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
