import { useEffect, useRef } from 'react'
import { useTranslation } from '../i18n'

interface ConfirmDialogProps {
  open: boolean
  message: string
  confirmLabel?: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
}

export default function ConfirmDialog({
  open,
  message,
  confirmLabel,
  cancelLabel,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const confirmBtnRef = useRef<HTMLButtonElement>(null)
  const { t } = useTranslation()

  useEffect(() => {
    if (open) confirmBtnRef.current?.focus()
  }, [open])

  if (!open) return null

  return (
    <div
      className="confirm-dialog-backdrop"
      onClick={onCancel}
      role="alertdialog"
      aria-modal="true"
      aria-label={message}
    >
      <div className="confirm-dialog-panel" onClick={e => e.stopPropagation()}>
        <p className="confirm-dialog-message">{message}</p>
        <div className="confirm-dialog-actions">
          <button
            className="confirm-dialog-btn confirm-dialog-btn-cancel" aria-label={t('cancel')}
            onClick={onCancel}
          >
            {cancelLabel || t('cancel')}
          </button>
          <button
            ref={confirmBtnRef}
            className="confirm-dialog-btn confirm-dialog-btn-confirm" aria-label={t('confirm')}
            onClick={onConfirm}
          >
            {confirmLabel || t('confirm')}
          </button>
        </div>
      </div>
    </div>
  )
}
