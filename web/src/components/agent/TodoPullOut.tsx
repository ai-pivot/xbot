import { useEffect, useRef, useState } from 'react'
import { CheckCircle2, ChevronRight, Circle, Loader2, Pencil, Target, Trash2 } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'
import type { TodoState } from '@/hooks/useTodos'
import type { TodoItem } from '@/types/shared'
import { AnimatedCollapse } from '@/components/ui/animated-collapse'

interface TodoPullOutProps {
  todoState: TodoState
  /** When provided, shows a 🎯 button on the right to set a goal (no goal active). */
  hasGoal?: boolean
  onSetGoal?: () => void
  /** Persist an edited list (rename / toggle done / delete). Without it the
   *  rows stay read-only (e.g. a surface embedding the toolbar without a session). */
  onUpdateTodos?: (todos: TodoItem[]) => void
  /** Set one item's text as the session goal, in a single click. */
  onSetGoalTodo?: (text: string) => void
  /** Text of the active goal — marks the matching row as "is goal". */
  goalText?: string | null
}

/** TODO-only inset toolbar restored above the composer. */
export function TodoPullOut({
  todoState,
  hasGoal,
  onSetGoal,
  onUpdateTodos,
  onSetGoalTodo,
  goalText,
}: TodoPullOutProps) {
  const { t } = useI18n()
  const isTouch = useIsTouch()
  const [expanded, setExpanded] = useState(false)
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  const [draft, setDraft] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  const { todos, doneCount, total, currentTask } = todoState
  const editable = typeof onUpdateTodos === 'function'

  useEffect(() => {
    if (editingIndex !== null) {
      inputRef.current?.focus()
      inputRef.current?.select()
    }
  }, [editingIndex])

  if (total === 0) return null

  const percent = Math.round((doneCount / total) * 100)

  const startEdit = (i: number) => {
    if (!editable) return
    setEditingIndex(i)
    setDraft(todos[i].text)
  }

  const commitEdit = () => {
    if (editingIndex === null) return
    const text = draft.trim()
    const prev = todos[editingIndex]
    setEditingIndex(null)
    // Clearing the text would silently drop a checklist item — keep the old text.
    if (!text || !prev || text === prev.text) return
    onUpdateTodos?.(todos.map((it, i) => (i === editingIndex ? { ...it, text } : it)))
  }

  const toggleDone = (i: number) => {
    onUpdateTodos?.(
      todos.map((it, idx) => (idx === i ? { ...it, status: it.status === 'done' ? 'pending' : 'done' } : it)),
    )
  }

  const removeTodo = (i: number) => {
    onUpdateTodos?.(todos.filter((_, idx) => idx !== i))
  }

  return (
    <div className="mx-1.5 mb-1 overflow-hidden rounded-md border border-border bg-bg-secondary text-sm md:mx-2 md:mb-1.5">
      <div className="flex h-7 w-full items-center gap-2 px-2.5 text-left md:h-8">
        <button
          type="button"
          data-testid="todo-toggle"
          aria-expanded={expanded}
          aria-label={expanded ? t('agent.collapseTodos') : t('agent.expandTodos')}
          onClick={() => setExpanded((open) => !open)}
          className="flex h-full min-w-0 flex-1 items-center gap-2 text-left transition-colors hover:bg-bg-tertiary -mx-2.5 px-2.5"
        >
          <ChevronRight
            className={cn('size-3.5 shrink-0 text-text-muted transition-transform', expanded && 'rotate-90')}
          />
          <div className="h-1.5 w-12 shrink-0 overflow-hidden rounded-full bg-bg-tertiary">
            <div
              className="h-full rounded-full bg-accent transition-[width] duration-300"
              style={{ width: `${percent}%` }}
            />
          </div>
          <span className="shrink-0 text-xs tabular-nums text-text-secondary">
            {doneCount}/{total}
          </span>
          <span className={cn('min-w-0 flex-1 truncate text-xs', currentTask ? 'text-text-primary' : 'text-text-muted')}>
            {currentTask?.text ?? t('agent.todoAllDone')}
          </span>
        </button>
        {/* 🎯 Goal button — only show when no goal active and callback provided */}
        {!hasGoal && onSetGoal && (
          <button
            type="button"
            onClick={onSetGoal}
            className="shrink-0 rounded p-1 text-text-muted transition-colors hover:bg-accent/10 hover:text-accent"
            title={t('agent.setAsGoal')}
          >
            <Target className="size-3.5" />
          </button>
        )}
      </div>
      <AnimatedCollapse open={expanded}>
        <div className="max-h-[240px] overflow-y-auto border-t border-border px-2 py-1.5">
          {todos.map((todo, i) => {
            const isGoal = !!goalText && goalText.trim() === todo.text.trim()
            if (editingIndex === i) {
              return (
                <div key={i} data-testid="todo-item" data-todo-text={todo.text} className="flex items-center gap-2 py-0.5">
                  <input
                    ref={inputRef}
                    data-testid="todo-edit-input"
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    onBlur={commitEdit}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        commitEdit()
                      } else if (e.key === 'Escape') {
                        e.preventDefault()
                        setEditingIndex(null)
                      }
                    }}
                    aria-label={t('agent.todoEdit')}
                    className="min-w-0 flex-1 rounded border border-accent/60 bg-bg-primary px-1.5 py-0.5 text-xs text-text-primary outline-none ring-2 ring-accent/25"
                  />
                </div>
              )
            }
            return (
              <div
                key={i}
                data-testid="todo-item"
                data-todo-text={todo.text}
                className={cn(
                  'group flex items-start gap-2 rounded px-1 py-1 text-xs transition-colors hover:bg-bg-tertiary/50',
                  todo.status === 'done' ? 'text-text-muted' : 'text-text-primary',
                  isGoal && 'bg-accent/[0.07]',
                )}
              >
                <button
                  type="button"
                  data-testid="todo-status"
                  disabled={!editable}
                  aria-label={todo.status === 'done' ? t('agent.todoMarkPending') : t('agent.todoMarkDone')}
                  onClick={() => toggleDone(i)}
                  className={cn(
                    'mt-0.5 shrink-0 rounded-full transition-transform',
                    // 触屏：把 12px 的图标包进 ≥32px 的命中区（不改变视觉位置）
                    isTouch && '-m-2 p-2',
                    editable && 'cursor-pointer hover:scale-110',
                  )}
                >
                  {todo.status === 'done' ? (
                    <CheckCircle2 className={isTouch ? 'h-4 w-4' : 'h-3 w-3'} style={{ color: 'var(--status-success)' }} />
                  ) : todo.status === 'doing' ? (
                    <Loader2 className={cn(isTouch ? 'h-4 w-4' : 'h-3 w-3', 'animate-spin')} style={{ color: 'var(--accent)' }} />
                  ) : (
                    <Circle className={cn(isTouch ? 'h-4 w-4' : 'h-3 w-3', 'text-text-muted')} />
                  )}
                </button>

                <button
                  type="button"
                  data-testid="todo-text"
                  disabled={!editable}
                  onClick={() => startEdit(i)}
                  title={editable ? t('agent.todoClickToEdit') : undefined}
                  className={cn(
                    'min-w-0 flex-1 rounded text-left leading-4',
                    todo.status === 'done' && 'line-through',
                    todo.status === 'doing' && 'font-medium',
                    editable && 'cursor-text',
                  )}
                >
                  {todo.text}
                </button>

                {isGoal && (
                  <span
                    data-testid="todo-goal-badge"
                    className="mt-0.5 shrink-0 rounded-full bg-accent/15 px-1.5 py-px text-[9px] font-medium text-accent"
                  >
                    {t('agent.todoIsGoal')}
                  </span>
                )}

                {editable && (
                  <div
                    data-testid="todo-actions"
                    className={cn(
                      'flex shrink-0 items-center transition-opacity',
                      // 触屏没有 hover：动作按钮必须常显，否则手机用户永远点不到
                      // （电脑端仍是 hover 才出现，避免每行都堆三个图标）
                      isTouch ? 'gap-1 opacity-100' : 'gap-0.5 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100',
                    )}
                  >
                    {onSetGoalTodo && (
                      <button
                        type="button"
                        data-testid="todo-set-goal"
                        aria-label={t('agent.todoSetGoal')}
                        title={t('agent.todoSetGoal')}
                        onClick={() => onSetGoalTodo(todo.text)}
                        className={cn(
                          'flex items-center justify-center rounded text-accent transition-colors hover:bg-accent/15',
                          isTouch ? 'size-8' : 'size-5',
                        )}
                      >
                        <Target className={isTouch ? 'size-4' : 'size-3'} />
                      </button>
                    )}
                    <button
                      type="button"
                      data-testid="todo-edit"
                      aria-label={t('agent.todoEdit')}
                      title={t('agent.todoEdit')}
                      onClick={() => startEdit(i)}
                      className={cn(
                        'flex items-center justify-center rounded text-text-muted transition-colors hover:bg-bg-tertiary hover:text-text-primary',
                        isTouch ? 'size-8' : 'size-5',
                      )}
                    >
                      <Pencil className={isTouch ? 'size-4' : 'size-3'} />
                    </button>
                    <button
                      type="button"
                      data-testid="todo-delete"
                      aria-label={t('agent.todoDelete')}
                      title={t('agent.todoDelete')}
                      onClick={() => removeTodo(i)}
                      className={cn(
                        'flex items-center justify-center rounded text-text-muted transition-colors hover:bg-destructive/10 hover:text-destructive',
                        isTouch ? 'size-8' : 'size-5',
                      )}
                    >
                      <Trash2 className={isTouch ? 'size-4' : 'size-3'} />
                    </button>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </AnimatedCollapse>
    </div>
  )
}
