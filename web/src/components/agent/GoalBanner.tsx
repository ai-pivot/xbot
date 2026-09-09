/**
 * GoalBanner — displays the active goal above the message input.
 *
 * Features:
 * - 🎯 icon with pulse animation when goal is active
 * - Inline-editable goal text (click to edit, Enter to save, Esc to cancel)
 * - Status badge: 🔄 进行中 / ✅ 已完成
 * - Clear button (×) to remove the goal
 * - When completed: green accent only (no summary expansion)
 * - Compact, fancy design that stacks above the TODO toolbar
 */
import { useEffect, useRef, useState } from 'react'
import { Check, Pencil, Target, X } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'
import type { GoalInfo } from '@/types/shared'

interface GoalBannerProps {
  goal: GoalInfo
  onEdit: (objective: string) => void
  onClear: () => void
}

export function GoalBanner({ goal, onEdit, onClear }: GoalBannerProps) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(goal.objective)
  const inputRef = useRef<HTMLInputElement>(null)
  const completed = goal.status === 'completed'

  // Focus input when entering edit mode
  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus()
      inputRef.current.select()
    }
  }, [editing])

  // Sync draft when goal changes externally
  useEffect(() => {
    if (!editing) setDraft(goal.objective)
  }, [goal.objective, editing])

  const save = () => {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== goal.objective) {
      onEdit(trimmed)
    }
    setEditing(false)
  }

  const cancel = () => {
    setDraft(goal.objective)
    setEditing(false)
  }

  return (
    <div
      className={cn(
        'mx-1.5 mb-1 overflow-hidden rounded-md border text-sm transition-colors md:mx-2 md:mb-1.5',
        completed
          ? 'border-green-500/30 bg-bg-primary/92'
          : 'border-accent/30 bg-bg-primary/92',
      )}
    >
      {/* Goal row */}
      <div className="flex items-center gap-2 px-2.5 py-1.5">
        {/* Icon */}
        <div className="relative shrink-0">
          {completed ? (
            <Check className="size-3.5 text-green-500" />
          ) : (
            <>
              <Target className="size-3.5 text-accent" />
              <span className="absolute inset-0 animate-ping rounded-full opacity-30 [animation-duration:2s]" />
            </>
          )}
        </div>

        {/* Goal text (editable) */}
        {editing ? (
          <input
            ref={inputRef}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                e.preventDefault()
                save()
              } else if (e.key === 'Escape') {
                e.preventDefault()
                cancel()
              }
            }}
            onBlur={save}
            className={cn(
              'min-w-0 flex-1 bg-transparent px-1 text-xs outline-none',
              'ring-1 ring-accent/40 rounded',
            )}
            placeholder={t('agent.goal.inputPlaceholder')}
          />
        ) : (
          <button
            type="button"
            onClick={() => !completed && setEditing(true)}
            className={cn(
              'min-w-0 flex-1 truncate text-left text-xs',
              completed ? 'text-text-muted line-through' : 'text-text-primary',
              !completed && 'cursor-text hover:text-accent',
            )}
            title={completed ? undefined : t('agent.goal.clickToEdit')}
          >
            {goal.objective}
          </button>
        )}

        {/* Status badge */}
        <span
          className={cn(
            'shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium',
            completed
              ? 'bg-green-500/15 text-green-500'
              : 'bg-accent/15 text-accent',
          )}
        >
          {completed ? t('agent.goal.completed') : t('agent.goal.inProgress')}
        </span>

        {/* Edit button (only when active and not editing) */}
        {!completed && !editing && (
          <button
            type="button"
            onClick={() => setEditing(true)}
            className={`shrink-0 text-text-muted transition-opacity hover:text-text-primary ${isTouch ? 'opacity-70' : 'opacity-0 group-hover:opacity-100'}`}
            title={t('agent.goal.edit')}
          >
            <Pencil className="size-3" />
          </button>
        )}

        {/* Clear button */}
        {!editing && (
          <button
            type="button"
            onClick={onClear}
            className="shrink-0 text-text-muted hover:text-destructive"
            title={t('agent.goal.clear')}
          >
            <X className="size-3" />
          </button>
        )}
      </div>
    </div>
  )
}
