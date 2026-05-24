import { useState } from 'react'
import { useTranslation } from '../i18n'
import type { Reaction } from '../types'

interface MessageReactionsProps {
  reactions: Reaction[]
  onToggle: (emoji: string) => void
}

const REACTION_EMOJIS = ['👍', '❤️', '😂', '😮', '😢', '😠']

export default function MessageReactions({ reactions, onToggle }: MessageReactionsProps) {
  const [pickerOpen, setPickerOpen] = useState(false)
  const { t } = useTranslation()

  return (
    <div className="message-reactions" data-testid="message-reactions">
      {/* Existing reactions */}
      {reactions.map(r => (
        <button
          key={r.id}
          className={`reaction-chip${r.byMe ? ' reaction-chip-active' : ''}`}
          onClick={() => onToggle(r.emoji)}
          title={t('reactionTooltip', { emoji: r.emoji, count: r.users.length })}
          data-testid={`reaction-${r.emoji}`}
        >
          <span className="reaction-emoji">{r.emoji}</span>
          {r.users.length > 1 && <span className="reaction-count">{r.users.length}</span>}
        </button>
      ))}

      {/* Add reaction button + picker */}
      <div className="reaction-picker-container">
        <button
          className="reaction-add-btn"
          onClick={() => setPickerOpen(!pickerOpen)}
          title={t('addReaction')}
          data-testid="reaction-add-btn"
        >
          😊+
        </button>
        {pickerOpen && (
          <>
            <div className="fixed inset-0 z-40" onClick={() => setPickerOpen(false)} />
            <div className="reaction-picker" role="listbox" data-testid="reaction-picker">
              {REACTION_EMOJIS.map(emoji => (
                <button
                  key={emoji}
                  className="reaction-picker-item"
                  onClick={() => { onToggle(emoji); setPickerOpen(false) }}
                  role="option"
                  data-testid={`pick-${emoji}`}
                >
                  {emoji}
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
