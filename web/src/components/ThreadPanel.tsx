import { useState, useRef, useEffect } from 'react'
import { useTranslation } from '../i18n'
import type { ThreadMessage, Message } from '../types'

interface ThreadPanelProps {
  open: boolean
  onClose: () => void
  parentMessage: Message | null
  threadMessages: ThreadMessage[]
  onSendReply: (content: string) => void
  onDeleteThread?: () => void
}

export default function ThreadPanel({ open, onClose, parentMessage, threadMessages, onSendReply, onDeleteThread }: ThreadPanelProps) {
  const [input, setInput] = useState('')
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const { t } = useTranslation()

  // Auto-scroll to bottom when new messages arrive
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [threadMessages.length])

  // Focus input when panel opens
  useEffect(() => {
    if (open) setTimeout(() => inputRef.current?.focus(), 100)
  }, [open])

  if (!open || !parentMessage) return null

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!input.trim()) return
    onSendReply(input.trim())
    setInput('')
  }

  const participants = new Set(threadMessages.map(m => m.author || m.type)).size
  const replyCount = threadMessages.length

  return (
    <div className="thread-panel" data-testid="thread-panel" role="dialog" aria-label={t('thread')}>
      {/* Header */}
      <div className="thread-header">
        <div className="thread-header-info">
          <h3 className="thread-title">{t('thread')}</h3>
          <span className="thread-meta">
            {t('threadReplies', { count: replyCount })} · {t('threadParticipants', { count: participants })}
          </span>
        </div>
        <div className="thread-header-actions">
          {onDeleteThread && (
            <button onClick={onDeleteThread} className="thread-delete-btn" title={t('deleteThread')} data-testid="thread-delete-btn">
              🗑️
            </button>
          )}
          <button onClick={onClose} className="thread-close-btn" title={t('closeThread')} data-testid="thread-close-btn">
            ✕
          </button>
        </div>
      </div>

      {/* Parent message */}
      <div className="thread-parent">
        <div className="thread-parent-author">
          {parentMessage.type === 'user' ? '👤' : '🤖'}
        </div>
        <div className="thread-parent-content">
          {parentMessage.content.slice(0, 200)}
          {parentMessage.content.length > 200 ? '...' : ''}
        </div>
      </div>

      {/* Thread messages */}
      <div className="thread-messages">
        {threadMessages.length === 0 && (
          <div className="thread-empty">{t('threadEmpty')}</div>
        )}
        {threadMessages.map(msg => (
          <div key={msg.id} className="thread-message" data-testid={`thread-msg-${msg.id}`}>
            <div className="thread-message-author">
              {msg.type === 'user' ? '👤' : '🤖'} {msg.author || (msg.type === 'user' ? 'You' : 'AI')}
            </div>
            <div className="thread-message-content">{msg.content}</div>
            <div className="thread-message-time">
              {new Date(msg.ts).toLocaleTimeString()}
            </div>
          </div>
        ))}
        <div ref={messagesEndRef} />
      </div>

      {/* Input */}
      <form className="thread-input-form" onSubmit={handleSubmit}>
        <input
          ref={inputRef}
          type="text"
          value={input}
          onChange={e => setInput(e.target.value)}
          placeholder={t('replyInThread')}
          className="thread-input"
          data-testid="thread-input"
        />
        <button type="submit" className="thread-send-btn" disabled={!input.trim()} data-testid="thread-send-btn">
          {t('send')}
        </button>
      </form>
    </div>
  )
}
