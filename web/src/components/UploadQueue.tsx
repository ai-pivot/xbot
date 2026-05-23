import { useTranslation } from '../i18n'
import { IconRefresh, IconCheck, IconX } from './Icons'
import {
  type UploadQueueItem,
  getFileIcon,
  formatFileSize,
} from './FileUpload'

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface UploadQueueProps {
  queue: UploadQueueItem[]
  onRemove: (id: string) => void
  onRetry: (id: string) => void
  onMove: (id: string, direction: 'up' | 'down') => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export default function UploadQueue({ queue, onRemove, onRetry, onMove }: UploadQueueProps) {
  const { t } = useTranslation()

  if (queue.length === 0) return null

  return (
    <div className="upload-queue" data-testid="upload-queue">
      {queue.map((item, index) => {
        const icon = getFileIcon(item.file.type, item.file.name)
        const isError = item.status === 'error'
        const isUploading = item.status === 'uploading'
        const isDone = item.status === 'done'

        // Status label: use existing i18n keys where possible
        let statusText: React.ReactNode
        if (isUploading) {
          statusText = `${item.progress}%`
        } else if (isDone) {
          statusText = <IconCheck className="inline" />
        } else if (isError) {
          statusText = <IconX className="inline" />
        } else {
          statusText = '…'
        }

        return (
          <div
            key={item.id}
            className={`upload-queue-item upload-queue-item--${item.status}`}
            data-testid="upload-queue-item"
          >
            {/* Icon + file info */}
            <span className="upload-queue-icon">{icon}</span>
            <div className="upload-queue-info">
              <span className="upload-queue-name" title={item.file.name}>
                {item.file.name}
              </span>
              <span className="upload-queue-size">{formatFileSize(item.file.size)}</span>
            </div>

            {/* Progress bar (visible when uploading) */}
            {isUploading && (
              <div className="upload-queue-progress-track">
                <div
                  className="upload-queue-progress-bar"
                  style={{ width: `${item.progress}%` }}
                />
              </div>
            )}

            {/* Status label */}
            <span className={`upload-queue-status upload-queue-status--${item.status}`}>
              {statusText}
            </span>

            {/* Action buttons */}
            <div className="upload-queue-actions">
              {isError && (
                <button
                  className="upload-queue-action-btn upload-queue-retry-btn"
                  onClick={() => onRetry(item.id)}
                  title={t('retryUpload') as string}
                  data-testid="upload-queue-retry"
                >
                  <IconRefresh />
                </button>
              )}
              <button
                className="upload-queue-action-btn upload-queue-move-btn"
                onClick={() => onMove(item.id, 'up')}
                disabled={index === 0}
                title={t('moveUp') as string}
                data-testid="upload-queue-move-up"
              >
                ▲
              </button>
              <button
                className="upload-queue-action-btn upload-queue-move-btn"
                onClick={() => onMove(item.id, 'down')}
                disabled={index === queue.length - 1}
                title={t('moveDown') as string}
                data-testid="upload-queue-move-down"
              >
                ▼
              </button>
              <button
                className="upload-queue-action-btn upload-queue-remove-btn"
                onClick={() => onRemove(item.id)}
                title={t('remove') as string}
                data-testid="upload-queue-remove"
              >
                <IconX className="inline" />
              </button>
            </div>
          </div>
        )
      })}
    </div>
  )
}
