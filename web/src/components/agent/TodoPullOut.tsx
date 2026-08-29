import { useState } from 'react'
import { CheckCircle2, ChevronRight, Circle, Loader2, Target } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import type { TodoState } from '@/hooks/useTodos'
import { AnimatedCollapse } from '@/components/ui/animated-collapse'

interface TodoPullOutProps {
  todoState: TodoState
  /** When provided, shows a 🎯 button on the right to set a goal (no goal active). */
  hasGoal?: boolean
  onSetGoal?: () => void
}

/** TODO-only inset toolbar restored above the composer. */
export function TodoPullOut({ todoState, hasGoal, onSetGoal }: TodoPullOutProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)
  const { todos, doneCount, total, currentTask } = todoState
  if (total === 0) return null

  const percent = Math.round((doneCount / total) * 100)

  return (
    <div className="mx-2 mb-1.5 overflow-hidden rounded-md border border-border bg-bg-secondary text-sm">
      <div className="flex h-8 w-full items-center gap-2 px-2.5 text-left">
        <button
          type="button"
          aria-expanded={expanded}
          aria-label={expanded ? t('agent.collapseTodos') : t('agent.expandTodos')}
          onClick={() => setExpanded((open) => !open)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left transition-colors hover:bg-bg-tertiary -mx-2.5 px-2.5 h-full"
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
            title="设为目标"
          >
            <Target className="size-3.5" />
          </button>
        )}
      </div>
      <AnimatedCollapse open={expanded}>
        <div className="max-h-[200px] overflow-y-auto border-t border-border px-3 py-1.5">
          {todos.map((todo) => (
            <div
              key={todo.id}
              className={cn('flex items-start gap-2 py-1 text-xs', todo.status === 'done' ? 'text-text-muted' : 'text-text-primary')}
            >
              <span className="mt-0.5 shrink-0">
                {todo.status === 'done' ? (
                  <CheckCircle2 className="h-3 w-3" style={{ color: 'var(--status-success)' }} />
                ) : todo.status === 'doing' ? (
                  <Loader2 className="h-3 w-3 animate-spin" style={{ color: 'var(--accent)' }} />
                ) : (
                  <Circle className="h-3 w-3 text-text-muted" />
                )}
              </span>
              <span className={cn(
                'min-w-0 flex-1 leading-4',
                todo.status === 'done' && 'line-through',
                todo.status === 'doing' && 'font-medium',
              )}>
                {todo.text}
              </span>
            </div>
          ))}
        </div>
      </AnimatedCollapse>
    </div>
  )
}
