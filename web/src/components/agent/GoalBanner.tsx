/**
 * GoalBanner — displays the active goal above the message input.
 *
 * 交互重设计（用户 2026-09-13：goal 常超过一行，单行 input 不方便，手机尤其）：
 * - **显示**：不再省略号截断 —— 最多 2 行（line-clamp），超长给「展开/收起」；
 * - **编辑（桌面/非触屏）**：就地展开**自适应高度 textarea**（随内容增高，上限后内部滚动），
 *   Enter 保存 / Shift+Enter 换行 / Esc 取消，且有显式「保存 / 取消」按钮；
 *   **失焦不自动保存**（避免误改）；
 * - **编辑（触屏）**：**底部 Sheet** —— 键盘弹出不遮挡、safe-area 适配、16px 字号防 iOS 缩放；
 *   同一个 textarea（自适应高度）。
 *
 * 为什么不用单行 input：goal 是长文本，input 放不下 → 看不到全文、光标/滚动难操作；
 * 触屏上还叠加键盘遮挡与视口抖动。
 */
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Check, ChevronDown, ChevronUp, Pencil, Target, X } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'
import type { GoalInfo } from '@/types/shared'

interface GoalBannerProps {
  goal: GoalInfo
  onEdit: (objective: string) => void
  onClear: () => void
}

/** 编辑器最大高度：约 8 行后内部滚动（避免吃满屏幕）。 */
const EDITOR_MAX_HEIGHT = 160
/** 超过这个长度（或含换行）就给「展开/收起」。 */
const COLLAPSE_THRESHOLD = 60

/**
 * GoalEditor — 自适应高度的多行编辑器（goal / 长文本编辑的唯一形态）。
 * Enter 保存、Shift+Enter 换行、Esc 取消；IME 组合态不提交。
 */
function GoalEditor({
  value,
  onChange,
  onSave,
  onCancel,
  autoFocus,
  className,
  inputClassName,
}: {
  value: string
  onChange: (v: string) => void
  onSave: () => void
  onCancel: () => void
  autoFocus?: boolean
  className?: string
  inputClassName?: string
}) {
  const { t } = useI18n()
  const ref = useRef<HTMLTextAreaElement>(null)

  // 自适应高度（随内容增长，上限后内部滚动）。
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, EDITOR_MAX_HEIGHT)}px`
  }, [value])

  useLayoutEffect(() => {
    if (!autoFocus) return
    const el = ref.current
    if (!el) return
    el.focus()
    el.select()
  }, [autoFocus])

  return (
    <div className={cn('flex w-full flex-col gap-1.5', className)}>
      <textarea
        ref={ref}
        data-testid="goal-edit-input"
        value={value}
        rows={2}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          // IME 组合态：中文/日文选词时的 Enter 不是"提交"（历史 bug）。
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            onSave()
          } else if (e.key === 'Escape') {
            e.preventDefault()
            onCancel()
          }
        }}
        className={cn(
          'w-full resize-none rounded bg-transparent px-1.5 py-1 text-xs leading-relaxed outline-none',
          'ring-1 ring-accent/40',
          inputClassName,
        )}
        placeholder={t('agent.goal.inputPlaceholder')}
      />
      <div className="flex items-center justify-end gap-1.5">
        <button
          type="button"
          data-testid="goal-cancel"
          onClick={onCancel}
          className="rounded px-2 py-1 text-[11px] text-text-muted hover:bg-bg-secondary hover:text-text-primary"
        >
          {t('agent.goal.cancel')}
        </button>
        <button
          type="button"
          data-testid="goal-save"
          onClick={onSave}
          className="rounded bg-accent/15 px-2 py-1 text-[11px] font-medium text-accent hover:bg-accent/25"
        >
          {t('agent.goal.save')}
        </button>
      </div>
    </div>
  )
}

export function GoalBanner({ goal, onEdit, onClear }: GoalBannerProps) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const [editing, setEditing] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [draft, setDraft] = useState(goal.objective)
  const completed = goal.status === 'completed'
  const collapsible = goal.objective.length > COLLAPSE_THRESHOLD || goal.objective.includes('\n')

  // Sync draft when goal changes externally (仅非编辑态)。
  useEffect(() => {
    if (!editing) setDraft(goal.objective)
  }, [goal.objective, editing])

  const beginEdit = () => {
    if (completed) return
    setDraft(goal.objective)
    setEditing(true)
  }

  const save = () => {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== goal.objective) onEdit(trimmed)
    setEditing(false)
  }

  const cancel = () => {
    setDraft(goal.objective)
    setEditing(false)
  }

  const editor = (
    <GoalEditor
      value={draft}
      onChange={setDraft}
      onSave={save}
      onCancel={cancel}
      autoFocus
      inputClassName={isTouch ? 'text-base' : undefined}
    />
  )

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
      <div className="flex items-start gap-2 px-2.5 py-1.5">
        {/* Icon */}
        <div className="relative mt-0.5 shrink-0">
          {completed ? (
            <Check className="size-3.5 text-green-500" />
          ) : (
            <>
              <Target className="size-3.5 text-accent" />
              <span className="absolute inset-0 animate-ping rounded-full opacity-30 [animation-duration:2s]" />
            </>
          )}
        </div>

        {/* Goal text / inline editor (non-touch) */}
        {editing && !isTouch ? (
          <div className="min-w-0 flex-1">{editor}</div>
        ) : (
          <div className="min-w-0 flex-1">
            <button
              type="button"
              data-testid="goal-text"
              onClick={beginEdit}
              className={cn(
                'w-full whitespace-pre-wrap break-words text-left text-xs',
                !expanded && 'line-clamp-2',
                completed ? 'text-text-muted line-through' : 'text-text-primary',
                !completed && 'cursor-text hover:text-accent',
              )}
              title={completed ? undefined : t('agent.goal.clickToEdit')}
            >
              {goal.objective}
            </button>
            {collapsible && (
              <button
                type="button"
                data-testid="goal-expand"
                onClick={() => setExpanded((v) => !v)}
                className="mt-0.5 inline-flex items-center gap-0.5 text-[10px] text-text-muted hover:text-accent"
              >
                {expanded ? <ChevronUp className="size-3" /> : <ChevronDown className="size-3" />}
                {expanded ? t('agent.goal.collapse') : t('agent.goal.expand')}
              </button>
            )}
          </div>
        )}

        {/* Status badge / edit / clear (hidden while editing) */}
        {!editing && (
          <>
            <span
              className={cn(
                'shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium',
                completed ? 'bg-green-500/15 text-green-500' : 'bg-accent/15 text-accent',
              )}
            >
              {completed ? t('agent.goal.completed') : t('agent.goal.inProgress')}
            </span>

            {!completed && (
              <button
                type="button"
                onClick={beginEdit}
                className={cn(
                  'shrink-0 p-1.5 text-text-muted transition-opacity hover:text-text-primary',
                  isTouch ? 'opacity-70' : 'opacity-0 group-hover:opacity-100',
                )}
                title={t('agent.goal.edit')}
              >
                <Pencil className="size-3" />
              </button>
            )}

            <button
              type="button"
              data-testid="goal-clear"
              onClick={onClear}
              className="shrink-0 p-1.5 text-text-muted hover:text-destructive"
              title={t('agent.goal.clear')}
            >
              <X className="size-3" />
            </button>
          </>
        )}
      </div>

      {/* 触屏：底部 Sheet 编辑器（键盘不遮挡、safe-area 适配、16px 防缩放） */}
      {editing && isTouch && (
        <div
          data-testid="goal-sheet"
          className="fixed inset-x-0 bottom-0 z-50 border-t border-border bg-bg-primary p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] shadow-lg"
        >
          <div className="mb-2 text-xs font-medium text-text-secondary">{t('agent.goal.editing')}</div>
          {editor}
        </div>
      )}
    </div>
  )
}
