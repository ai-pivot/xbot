import { useState, useRef, useEffect } from 'react'
import { useTranslation } from '../i18n'
import { useSnapshot } from '../hooks/useSnapshot'
import { IconBookmark, IconCheck } from './Icons'
import type { Message } from '../types'

interface SnapshotShareProps {
  message: Message
  onDone?: (success: boolean) => void
}

export default function SnapshotShare({ message, onDone }: SnapshotShareProps) {
  const { snapshotting, snapshotError, takeSnapshot } = useSnapshot()
  const [showPreview, setShowPreview] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const { t } = useTranslation()

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  const handleTakeSnapshot = async () => {
    const success = await takeSnapshot(message)
    if (onDone) onDone(success)
    if (success) {
      setShowPreview(true)
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => setShowPreview(false), 2000)
    }
  }

  return (
    <div className="snapshot-share" data-testid="snapshot-share">
      <button
        className="snapshot-btn"
        onClick={handleTakeSnapshot}
        disabled={snapshotting}
        title={t('takeSnapshot')}
        data-testid="snapshot-btn"
      >
        <IconBookmark className="inline" />
      </button>
      {snapshotError && (
        <div className="snapshot-error" data-testid="snapshot-error">{snapshotError}</div>
      )}
      {showPreview && (
        <div className="snapshot-success" data-testid="snapshot-success">
          <IconCheck className="inline" /> {t('snapshotCopied')}
        </div>
      )}
    </div>
  )
}
