import { useEffect, useRef, useState } from 'react'
import { CheckCircle2, ChevronRight, Circle, Loader2, MoreHorizontal, Pencil, Target, Trash2 } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
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
  /** 触屏：哪一行的操作菜单（⋯）是打开的。 */
  const [menuIndex, setMenuIndex] = useState<number | null>(null)
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
            // 移动端：编辑必须走底部 Sheet —— 行内单行在手机上根本用不了（用户 2026-09-13）。
            if (editingIndex === i && isTouch) {
              return (
                <div
                  key={i}
                  data-testid="todo-edit-sheet"
                  className="fixed inset-x-0 bottom-0 z-50 border-t border-border bg-bg-primary p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] shadow-lg"
                >
                  <div className="mb-2 text-xs font-medium text-text-secondary">{t('agent.todoEdit')}</div>
                  <textarea
                    autoFocus
                    data-testid="todo-edit-input"
                    rows={3}
                    value={draft}
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && e.shiftKey) return
                      if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                        e.preventDefault()
                        commitEdit()
                      } else if (e.key === 'Escape') {
                        e.preventDefault()
                        setEditingIndex(null)
                      }
                    }}
                    aria-label={t('agent.todoEdit')}
                    className="max-h-[40dvh] w-full resize-none rounded border border-accent/60 bg-bg-primary px-2 py-1.5 text-base leading-relaxed text-text-primary outline-none ring-2 ring-accent/25"
                  />
                  <div className="mt-2 flex items-center justify-end gap-1.5">
                    <button
                      type="button"
                      data-testid="todo-edit-cancel"
                      onClick={() => setEditingIndex(null)}
                      className="rounded px-3 py-1.5 text-xs text-text-muted hover:bg-bg-secondary"
                    >
                      {t('agent.goal.cancel')}
                    </button>
                    <button
                      type="button"
                      data-testid="todo-edit-save"
                      onClick={commitEdit}
                      className="rounded bg-accent/15 px-3 py-1.5 text-xs font-medium text-accent hover:bg-accent/25"
                    >
                      {t('agent.goal.save')}
                    </button>
                  </div>
                </div>
              )
            }
            // 桌面：就地编辑（自适应高度 textarea）。
            if (editingIndex === i) {
              return (
                <div key={i} data-testid="todo-item" data-todo-text={todo.text} className="flex items-center gap-2 py-0.5">
                  {/* 与 goal 编辑同一套契约（用户 2026-09-13）：自适应高度 textarea ——
                      长 todo 不再被单行截断；Enter 保存 / Shift+Enter 换行 / Esc 取消 /
                      IME 组合态不提交（中文选词）；失焦保存（短文本改起来更顺手）。 */}
                  <textarea
                    ref={inputRef as never}
                    data-testid="todo-edit-input"
                    rows={1}
                    value={draft}
                    onChange={(e) => {
                      setDraft(e.target.value)
                      e.target.style.height = 'auto'
                      e.target.style.height = `${Math.min(e.target.scrollHeight, 72)}px`
                    }}
                    onBlur={commitEdit}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                        e.preventDefault()
                        commitEdit()
                      } else if (e.key === 'Escape') {
                        e.preventDefault()
                        setEditingIndex(null)
                      }
                    }}
                    aria-label={t('agent.todoEdit')}
                    className="min-w-0 flex-1 resize-none rounded border border-accent/60 bg-bg-primary px-1.5 py-0.5 text-xs leading-relaxed text-text-primary outline-none ring-2 ring-accent/25"
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
                    isTouch && '-m-2.5 p-2.5',
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
                  isTouch ? (
                    /* 触屏：只留一个 ⋯（32px）—— 三个操作收进菜单。
                       并排三个按钮 ≈104px，几乎和文本一样宽（手机上行宽仅 ~390px），
                       视觉上把待办正文挤没了。 */
                    <Popover
                      open={menuIndex === i}
                      onOpenChange={(o) => { if (!o) setMenuIndex(null) }}
                    >
                      <PopoverTrigger asChild>
                        <button
                          type="button"
                          data-testid="todo-more"
                          aria-label={t('agent.todoMoreActions')}
                          aria-haspopup="menu"
                          onClick={() => setMenuIndex((cur) => (cur === i ? null : i))}
                          className="flex size-8 shrink-0 items-center justify-center rounded-full text-text-secondary transition-colors active:bg-bg-tertiary"
                        >
                          <MoreHorizontal className="size-4" />
                        </button>
                      </PopoverTrigger>
                      <PopoverContent
                        align="end"
                        side="top"
                        sideOffset={6}
                        className="w-44 rounded-xl border-border bg-bg-secondary p-1 shadow-xl"
                      >
                        <div role="menu" data-testid="todo-actions-menu" className="flex flex-col">
                          {onSetGoalTodo && (
                            <button
                              type="button"
                              role="menuitem"
                              data-testid="todo-set-goal"
                              onClick={() => { setMenuIndex(null); onSetGoalTodo(todo.text) }}
                              className="flex h-10 items-center gap-2.5 rounded-lg px-2.5 text-left text-[13px] text-text-primary transition-colors active:bg-bg-tertiary hover:bg-bg-tertiary"
                            >
                              <Target className="size-4 shrink-0 text-accent" />
                              {t('agent.todoSetGoal')}
                            </button>
                          )}
                          <button
                            type="button"
                            role="menuitem"
                            data-testid="todo-edit"
                            onClick={() => { setMenuIndex(null); startEdit(i) }}
                            className="flex h-10 items-center gap-2.5 rounded-lg px-2.5 text-left text-[13px] text-text-primary transition-colors active:bg-bg-tertiary hover:bg-bg-tertiary"
                          >
                            <Pencil className="size-4 shrink-0 text-text-muted" />
                            {t('agent.todoEdit')}
                          </button>
                          <button
                            type="button"
                            role="menuitem"
                            data-testid="todo-delete"
                            onClick={() => { setMenuIndex(null); removeTodo(i) }}
                            className="flex h-10 items-center gap-2.5 rounded-lg px-2.5 text-left text-[13px] text-destructive transition-colors active:bg-destructive/10 hover:bg-destructive/10"
                          >
                            <Trash2 className="size-4 shrink-0" />
                            {t('agent.todoDelete')}
                          </button>
                        </div>
                      </PopoverContent>
                    </Popover>
                  ) : (
                    /* 桌面：hover 才出现的内联图标（不占常驻宽度） */
                    <div
                      data-testid="todo-actions"
                      className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100"
                    >
                      {onSetGoalTodo && (
                        <button
                          type="button"
                          data-testid="todo-set-goal"
                          aria-label={t('agent.todoSetGoal')}
                          title={t('agent.todoSetGoal')}
                          onClick={() => onSetGoalTodo(todo.text)}
                          className="flex size-5 items-center justify-center rounded text-accent transition-colors hover:bg-accent/15"
                        >
                          <Target className="size-3" />
                        </button>
                      )}
                      <button
                        type="button"
                        data-testid="todo-edit"
                        aria-label={t('agent.todoEdit')}
                        title={t('agent.todoEdit')}
                        onClick={() => startEdit(i)}
                        className="flex size-5 items-center justify-center rounded text-text-muted transition-colors hover:bg-bg-tertiary hover:text-text-primary"
                      >
                        <Pencil className="size-3" />
                      </button>
                      <button
                        type="button"
                        data-testid="todo-delete"
                        aria-label={t('agent.todoDelete')}
                        title={t('agent.todoDelete')}
                        onClick={() => removeTodo(i)}
                        className="flex size-5 items-center justify-center rounded text-text-muted transition-colors hover:bg-destructive/10 hover:text-destructive"
                      >
                        <Trash2 className="size-3" />
                      </button>
                    </div>
                  )
                )}
              </div>
            )
          })}
        </div>
      </AnimatedCollapse>
    </div>
  )
}
