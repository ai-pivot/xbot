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
    <div className="absolute top-2 right-2 opacity-0 group-hover:opacity-100 transition-opacity flex items-center gap-1">
      {/* Copy button */}
      <button
        onClick={onCopy}
        className="px-2 py-1 rounded text-xs bg-slate-700/60 hover:bg-slate-600/80 text-slate-300 hover:text-white backdrop-blur-sm"
        title={t('copyContent')}
        data-testid="copy-btn"
      >
        {copied ? <IconCheck className="inline" /> : <IconCopy />}
      </button>

      {/* More actions menu */}
      {(onDelete || onRegenerate || onReply || onSnapshot) && (
        <div className="relative">
          <button
            onClick={() => setMenuOpen(!menuOpen)}
            className="px-2 py-1 rounded text-xs bg-slate-700/60 hover:bg-slate-600/80 text-slate-300 hover:text-white backdrop-blur-sm"
            title="More actions"
            data-testid="more-actions-btn"
            aria-expanded={menuOpen}
            aria-haspopup="menu"
          >
            ⋯
          </button>
          {menuOpen && (
            <>
              <div className="fixed inset-0 z-40" onClick={() => setMenuOpen(false)} />
              <div className="absolute right-0 top-full mt-1 bg-slate-800 border border-slate-600 rounded-lg shadow-xl z-50 py-1 min-w-[140px]" role="menu" onKeyDown={(e) => { if (e.key === 'Escape') setMenuOpen(false) }}>
                 {onReply && (
                  <button
                    onClick={() => { onReply(); setMenuOpen(false) }}
                    className="w-full text-left px-3 py-2 text-sm text-slate-300 hover:bg-slate-700 hover:text-white transition-colors flex items-center gap-2"
                    role="menuitem"
                    data-testid="reply-btn"
                  >
                    <IconReply className="inline" /> {t('replyMessage')}
                  </button>
                )}
                {onSnapshot && (
                  <button
                    onClick={() => { onSnapshot(); setMenuOpen(false) }}
                    className="w-full text-left px-3 py-2 text-sm text-slate-300 hover:bg-slate-700 hover:text-white transition-colors flex items-center gap-2"
                    role="menuitem"
                    data-testid="snapshot-btn"
                  >
                    <IconBookmark className="inline" /> {t('takeSnapshot')}
                  </button>
                )}
                {onRegenerate && (
                  <button
                    onClick={() => { onRegenerate(); setMenuOpen(false) }}
                    className="w-full text-left px-3 py-2 text-sm text-slate-300 hover:bg-slate-700 hover:text-white transition-colors flex items-center gap-2"
                    role="menuitem"
                    data-testid="regenerate-btn"
                  >
                    <IconRefresh className="inline" /> {t('regenerate')}
                  </button>
                )}
                {onDelete && (
                  <button
                    onClick={() => { onDelete(); setMenuOpen(false) }}
                    className="w-full text-left px-3 py-2 text-sm text-red-400 hover:bg-slate-700 hover:text-red-300 transition-colors flex items-center gap-2"
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
