/**
 * UserMessage — renders one committed user message (Spec 4 §3.5, Spec C §2).
 *
 * Right-aligned bubble. Content is rendered through the same MarkdownRenderer
 * as Agent output (no pre-wrap wrapper), ensuring identical markdown rendering.
 *
 * Rewind is now inline-edit mode (Spec C §2):
 *   - Pencil icon instead of RotateCcw
 *   - Click → message becomes an editable textarea (full-width, like MessageInput)
 *   - Confirm (Check) → calls onRewind with edited content
 *   - Cancel (X) → restores original message
 *   - Only one message can be edited at a time (editingMessageId prop)
 *   - Edit container inherits the display height as min-height to prevent jitter
 */
import { memo, useEffect, useRef, useState } from 'react'
import { Check, Loader2, Pencil, X } from 'lucide-react'

import { MarkdownRenderer } from './MarkdownRenderer'
import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { useSendKeyMode, isSendKey } from '@/hooks/useSendKeyMode'
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
  /** True while the message is being sent (shows a spinner). */
  sending?: boolean
}

export const UserMessage = memo(function UserMessage({
  content,
  onRewind,
  isEditing = false,
  onStartEdit,
  onEndEdit,
  editDisabled = false,
  sending = false,
}: UserMessageProps) {
  const { t } = useI18n()
  const { mode: sendKeyMode } = useSendKeyMode()
  const [editValue, setEditValue] = useState(content)
  const editRef = useRef<HTMLTextAreaElement>(null)
  const displayRef = useRef<HTMLDivElement>(null)
  const [editMinHeight, setEditMinHeight] = useState<number | null>(null)

  // Capture display height before entering edit mode to prevent height jitter
  const handleStartEdit = () => {
    const el = displayRef.current
    if (el) {
      setEditMinHeight(el.offsetHeight)
    }
    onStartEdit?.()
  }

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
    if (!edited) {
      onEndEdit?.()
      return
    }
    // Rewind even when content is unchanged — the user confirmed the rewind
    // action by clicking Check, not Cancel (X).
    onRewind?.(edited)
  }

  const handleCancel = () => {
    setEditValue(content)
    onEndEdit?.()
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (isSendKey(e, sendKeyMode)) {
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
      <div className="px-1">
        <div className="flex w-full flex-col items-end gap-1">
          <div
            className="w-full rounded-lg border border-accent bg-bg-secondary px-3 py-2"
            style={{ minHeight: editMinHeight ?? undefined }}
          >
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
        <div
          ref={displayRef}
          className="rounded-2xl rounded-br-sm bg-accent/15 px-3.5 py-2 text-text-primary"
        >
          <MarkdownRenderer content={content || ' '} />
          {sending && (
            <div className="mt-1.5 flex items-center gap-1.5 text-xs text-text-muted">
              <Loader2 className="size-3 animate-spin" />
              <span>{t('agent.sending')}</span>
            </div>
          )}
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
            onClick={handleStartEdit}
          >
            <Pencil className="size-3.5" />
          </Button>
        )}
      </div>
    </div>
  )
})
