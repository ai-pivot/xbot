/**
 * UserMessage — renders one committed user message (Spec 4 §3.5, Spec C §2).
 *
 * Right-aligned bubble. Content is plain text rendered as Markdown so line
 * breaks and inline code render faithfully.
 *
 * Rewind is now inline-edit mode (Spec C §2):
 *   - Pencil icon instead of RotateCcw
 *   - Click → message becomes an editable textarea
 *   - Confirm (Check) → calls onRewind with edited content
 *   - Cancel (X) → restores original message
 *   - Only one message can be edited at a time (editingMessageId prop)
 */
import { memo, useEffect, useRef, useState } from 'react'
import { Check, Pencil, X } from 'lucide-react'

import { MarkdownRenderer } from './MarkdownRenderer'
import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { cn } from '@/lib/utils'

interface UserMessageProps {
  content: string
  /** Rewind callback — now receives the edited content string. */
  onRewind?: (editedContent: string) => void
  /** Whether this message is currently being edited. */
  isEditing?: boolean
  /** Callback to enter edit mode (sets editingMessageId). */
  onStartEdit?: () => void
  /** Callback to exit edit mode (clears editingMessageId). */
  onEndEdit?: () => void
  /** Whether editing is disabled (another message is being edited). */
  editDisabled?: boolean
}

export const UserMessage = memo(function UserMessage({
  content,
  onRewind,
  isEditing = false,
  onStartEdit,
  onEndEdit,
  editDisabled = false,
}: UserMessageProps) {
  const { t } = useI18n()
  const [editValue, setEditValue] = useState(content)
  const editRef = useRef<HTMLTextAreaElement>(null)

  // Reset edit value when entering edit mode
  useEffect(() => {
    if (isEditing) {
      setEditValue(content)
      // Focus and move cursor to end
      requestAnimationFrame(() => {
        const el = editRef.current
        if (el) {
          el.focus()
          el.setSelectionRange(el.value.length, el.value.length)
        }
      })
    }
  }, [isEditing, content])

  // Auto-resize the edit textarea
  useEffect(() => {
    if (!isEditing) return
    const el = editRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 300)}px`
  }, [isEditing, editValue])

  const handleConfirm = () => {
    const edited = editValue.trim()
    if (!edited || edited === content) {
      onEndEdit?.()
      return
    }
    onRewind?.(edited)
  }

  const handleCancel = () => {
    setEditValue(content)
    onEndEdit?.()
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleConfirm()
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      handleCancel()
    }
  }

  if (isEditing) {
    return (
      <div className="flex justify-end px-1">
        <div className="flex max-w-[85%] flex-col items-end gap-1">
          <div className="w-full rounded-lg border border-accent bg-bg-secondary px-3 py-2">
            <textarea
              ref={editRef}
              value={editValue}
              onChange={(e) => setEditValue(e.target.value)}
              onKeyDown={handleKeyDown}
              className="w-full resize-none bg-transparent text-sm text-text-primary focus:outline-none"
              style={{ minHeight: '52px' }}
            />
          </div>
          <div className="flex items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={t('agent.confirmEdit')}
              onClick={handleConfirm}
              className="size-6 text-accent hover:bg-accent/10"
            >
              <Check className="size-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={t('agent.cancelEdit')}
              onClick={handleCancel}
              className="size-6 hover:bg-destructive/10 hover:text-destructive"
            >
              <X className="size-3.5" />
            </Button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex justify-end px-1">
      <div className="flex max-w-[85%] flex-col items-end gap-1">
        <div className="rounded-2xl rounded-br-sm bg-accent/15 px-3.5 py-2 text-text-primary" style={{ whiteSpace: 'pre-wrap' }}>
          <MarkdownRenderer content={content || ' '} />
        </div>
        {onRewind && onStartEdit && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={t('agent.editAndRewind')}
            title={t('agent.editAndRewind')}
            disabled={editDisabled}
            className={cn(
              'h-6 w-6',
              editDisabled
                ? 'opacity-20 cursor-not-allowed'
                : 'opacity-60 hover:opacity-100',
            )}
            onClick={onStartEdit}
          >
            <Pencil className="size-3.5" />
          </Button>
        )}
      </div>
    </div>
  )
})
