import { useEffect, useState } from 'react'
import { useTranslation } from '../i18n'
import { IconBookmark, IconTrash, IconInbox, IconX } from './Icons'
import type { Bookmark } from '../hooks/useBookmarks'

/* -------------------------------------------------------------------------- */
/*  Types                                                                     */
/* -------------------------------------------------------------------------- */

export interface BookmarkPanelProps {
  bookmarks: Bookmark[]
  onRemove: (messageId: string) => void
  onClearAll: () => void
  onJump: (messageId: string) => void
}

/* -------------------------------------------------------------------------- */
/*  Component                                                                 */
/* -------------------------------------------------------------------------- */

export default function BookmarkPanel({ bookmarks, onRemove, onClearAll, onJump }: BookmarkPanelProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  // Listen for custom toggle event
  useEffect(() => {
    const handler = () => setOpen(prev => !prev)
    window.addEventListener('toggle-bookmark-panel', handler)
    return () => window.removeEventListener('toggle-bookmark-panel', handler)
  }, [])

  // Close on Escape
  useEffect(() => {
    if (!open) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [open])

  if (!open) return null

  return (
    <div className="bookmark-backdrop" onClick={() => setOpen(false)}>
      <div
        className="bookmark-panel"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={t('bookmarks')}
      >
        {/* Header */}
        <div className="bookmark-header">
          <h2 className="bookmark-title"><IconBookmark className="inline" /> {t('bookmarks')}</h2>
          <div className="bookmark-header-actions">
            {bookmarks.length > 0 && (
              <button className="bookmark-btn bookmark-btn-danger" onClick={onClearAll}>
                <IconTrash className="inline" /> {t('delete')}
              </button>
            )}
            <button className="bookmark-close" onClick={() => setOpen(false)} aria-label={t('closeSettings')}>
              <IconX className="inline" />
            </button>
          </div>
        </div>

        {/* List */}
        <div className="bookmark-list">
          {bookmarks.length === 0 ? (
            <div className="bookmark-empty">
              <span className="bookmark-empty-icon"><IconInbox className="inline" style={{width:28,height:28}} /></span>
              <p>{t('noBookmarks')}</p>
            </div>
          ) : (
            bookmarks.map(bm => (
              <div key={bm.messageId} className="bookmark-card">
                <div className="bookmark-card-content">
                  <p className="bookmark-card-text">
                    {bm.content.slice(0, 50) || '—'}
                    {bm.content.length > 50 ? '…' : ''}
                  </p>
                  <span className="bookmark-card-time">
                    {new Date(bm.timestamp).toLocaleString()}
                  </span>
                </div>
                <div className="bookmark-card-actions">
                  <button
                    className="bookmark-btn"
                    onClick={() => {
                      onJump(bm.messageId)
                      setOpen(false)
                    }}
                    title={t('jumpToBookmark')}
                  >
                    ↗
                  </button>
                  <button
                    className="bookmark-btn bookmark-btn-remove"
                    onClick={() => onRemove(bm.messageId)}
                    title={t('removeBookmark')}
                  >
                    <IconX className="inline" />
                  </button>
                </div>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  )
}
