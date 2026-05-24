import { useState } from 'react'
import { useTranslation } from '../i18n'
import { IconCopy, IconRefresh, IconTrash, IconReply, IconCheck, IconBookmark } from './Icons'

interface MessageActionsProps {
  onCopy: () => void
  onDelete?: () => void
  onRegenerate?: () => void
  onReply?: () => void
  onSnapshot?: () => void
  copied: boolean
}

export default function MessageActions({ onCopy, onDelete, onRegenerate, onReply, onSnapshot, copied }: MessageActionsProps) {
  const { t } = useTranslation()
  const [menuOpen, setMenuOpen] = useState(false)

  return (
    <div className="message-actions-bar">
      {/* Copy */}
      <button
        onClick={onCopy}
        className="message-action-btn"
        title={t('copyContent')}
        data-testid="copy-btn"
      >
        {copied ? <IconCheck /> : <IconCopy />}
      </button>

      {/* Reply */}
      {onReply && (
        <button
          onClick={onReply}
          className="message-action-btn"
          title={t('replyMessage')}
          data-testid="reply-btn"
        >
          <IconReply />
        </button>
      )}

      {/* Regenerate */}
      {onRegenerate && (
        <button
          onClick={onRegenerate}
          className="message-action-btn"
          title={t('regenerate')}
          data-testid="regenerate-btn"
        >
          <IconRefresh />
        </button>
      )}

      {/* More actions (snapshot, delete) */}
      {(onSnapshot || onDelete) && (
        <div className="relative">
          <button
            onClick={() => setMenuOpen(!menuOpen)}
            className="message-action-btn"
            title="More"
            data-testid="more-actions-btn"
            aria-expanded={menuOpen}
            aria-haspopup="menu"
          >
            ⋯
          </button>
          {menuOpen && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setMenuOpen(false)} />
              <div className="message-actions-popup" role="menu" onKeyDown={(e) => { if (e.key === 'Escape') setMenuOpen(false) }}>
                 {onSnapshot && (
                  <button
                   onClick={() => { onSnapshot(); setMenuOpen(false) }}
                   className="message-actions-popup-item"
                   role="menuitem"
                   data-testid="snapshot-btn"
                  >
                   <IconBookmark className="inline" /> {t('takeSnapshot')}
                  </button>
                 )}
                 {onDelete && (
                  <button
                   onClick={() => { onDelete(); setMenuOpen(false) }}
                   className="message-actions-popup-item message-actions-popup-danger"
                   role="menuitem"
                   data-testid="delete-btn"
                  >
                   <IconTrash className="inline" /> {t('deleteMessage')}
                  </button>
                 )}
                </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}
