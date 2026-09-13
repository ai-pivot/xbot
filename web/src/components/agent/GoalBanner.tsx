/**
 * GoalBanner — displays the active goal above the message input.
 *
 * Features (显示层与 master 一致：单行 + 截断，不换行完整渲染):
 * - 🎯 icon with pulse animation when goal is active
 * - Inline-editable goal text (click to edit, Enter to save, Esc to cancel)
 * - Status badge: 🔄 进行中 / ✅ 已完成
 * - Clear button (×) to remove the goal
 * - When completed: green accent only (no summary expansion)
 * - Compact, fancy design that stacks above the TODO toolbar
 *
 * 编辑交互（唯一改动点）：点击文本就地编辑；Enter 保存 / Esc 取消；
 * **失焦不保存**（回退原值）；IME 组合态不提交。
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
  const inputRef = useRef<HTMLTextAreaElement>(null)
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

        {/* Goal text (editable) —— 编辑交互与 todo 编辑同一套契约（用户 2026-09-13）：
            手机端走**底部弹出 textarea**（与 TodoPullOut 的 todo-edit-sheet 完全一致），
            桌面端就地自适应 textarea。 */}
        {editing && !isTouch ? (
          <textarea
            ref={inputRef}
            data-testid="goal-edit-input"
            rows={1}
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value)
              e.target.style.height = 'auto'
              e.target.style.height = `${Math.min(e.target.scrollHeight, 72)}px`
            }}
            onBlur={save}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault()
                save()
              } else if (e.key === 'Escape') {
                e.preventDefault()
                cancel()
              }
            }}
            placeholder={t('agent.goal.inputPlaceholder')}
            aria-label={t('agent.goal.edit')}
            className="min-w-0 flex-1 resize-none rounded border border-accent/60 bg-bg-primary px-1.5 py-0.5 text-xs leading-relaxed text-text-primary outline-none ring-2 ring-accent/25"
          />
        ) : (
          <button
            type="button"
            data-testid="goal-text"
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
            data-testid="goal-clear"
            onClick={onClear}
            className="shrink-0 text-text-muted hover:text-destructive"
            title={t('agent.goal.clear')}
          >
            <X className="size-3" />
          </button>
        )}
      </div>

      {/* 移动端：编辑走**底部弹出 textarea**（与 TodoPullOut 的 todo-edit-sheet 同一形态，
          用户 2026-09-13 要求「goal 编辑改成和 todo 编辑一样」）。fixed 定位，不影响卡片高度。 */}
      {editing && isTouch && (
        <div
          data-testid="goal-edit-sheet"
          className="fixed inset-x-0 bottom-0 z-50 border-t border-border bg-bg-primary p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] shadow-lg"
        >
          <div className="mb-2 text-xs font-medium text-text-secondary">{t('agent.goal.edit')}</div>
          <textarea
            autoFocus
            data-testid="goal-edit-input"
            rows={3}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && e.shiftKey) return
              if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                e.preventDefault()
                save()
              } else if (e.key === 'Escape') {
                e.preventDefault()
                cancel()
              }
            }}
            aria-label={t('agent.goal.edit')}
            className="max-h-[40dvh] w-full resize-none rounded border border-accent/60 bg-bg-primary px-2 py-1.5 text-base leading-relaxed text-text-primary outline-none ring-2 ring-accent/25"
          />
          <div className="mt-2 flex items-center justify-end gap-1.5">
            <button
              type="button"
              data-testid="goal-edit-cancel"
              onClick={cancel}
              className="rounded px-3 py-1.5 text-xs text-text-muted hover:bg-bg-secondary"
            >
              {t('agent.goal.cancel')}
            </button>
            <button
              type="button"
              data-testid="goal-edit-save"
              onClick={save}
              className="rounded bg-accent/15 px-3 py-1.5 text-xs font-medium text-accent hover:bg-accent/25"
            >
              {t('agent.goal.save')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
