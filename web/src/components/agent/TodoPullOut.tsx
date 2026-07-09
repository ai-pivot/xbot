/**
 * TodoPullOut — a pull-out TODO panel above the message input.
 *
 * Collapsed (default): a single row showing progress bar N/M + the first
 *   incomplete task text. Click to expand.
 * Expanded: the full TODO list (✓ done, ○ pending).
 * Hidden entirely when there are no TODOs.
 *
 * The width is slightly narrower than the input (mx-2 margin).
 */
import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { TodoState } from '@/hooks/useTodos'
import { useI18n } from '@/providers/i18n'

interface TodoPullOutProps {
  todoState: TodoState
}

export function TodoPullOut({ todoState }: TodoPullOutProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)

  const { todos, doneCount, total, currentTask } = todoState
  if (total === 0) return null

  const pct = total > 0 ? Math.round((doneCount / total) * 100) : 0

  return (
    <div className="mx-2 mb-1.5 overflow-hidden rounded-lg border border-border bg-bg-secondary text-sm">
      {/* Collapsed summary — click to expand/collapse */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-bg-tertiary"
      >
        <ChevronDown
          className={cn(
            'size-3.5 shrink-0 text-text-muted transition-transform',
            expanded && 'rotate-180',
          )}
        />
        {/* Progress bar */}
        <div className="flex items-center gap-2">
          <div className="h-1.5 w-16 overflow-hidden rounded-full bg-bg-tertiary">
            <div
              className="h-full rounded-full bg-accent transition-all"
              style={{ width: `${pct}%` }}
            />
          </div>
          <span className="shrink-0 text-xs tabular-nums text-text-secondary">
            {doneCount}/{total}
          </span>
        </div>
        {/* Current task (first incomplete) */}
        {currentTask ? (
          <span className="min-w-0 flex-1 truncate text-text-primary">
            {currentTask.text}
          </span>
        ) : (
          <span className="min-w-0 flex-1 truncate text-text-muted">
            {t('agent.todoAllDone')}
          </span>
        )}
      </button>

      {/* Expanded list */}
      {expanded && (
        <div className="border-t border-border px-3 py-1.5">
          {todos.map((todo) => (
            <div
              key={todo.id}
              className={cn(
                'flex items-start gap-2 py-1',
                todo.done ? 'text-text-muted' : 'text-text-primary',
              )}
            >
              <span className="mt-0.5 shrink-0">
                {todo.done ? '✓' : '○'}
              </span>
              <span className={cn('min-w-0 flex-1', todo.done && 'line-through')}>
                {todo.text}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
