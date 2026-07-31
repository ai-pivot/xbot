import { useState } from 'react'
import { ChevronRight } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import type { TodoState } from '@/hooks/useTodos'
import { AnimatedCollapse } from '@/components/ui/animated-collapse'

interface TodoPullOutProps {
  todoState: TodoState
}

/** TODO-only inset toolbar restored above the composer. */
export function TodoPullOut({ todoState }: TodoPullOutProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)
  const { todos, doneCount, total, currentTask } = todoState
  if (total === 0) return null

  const percent = Math.round((doneCount / total) * 100)

  return (
    <div className="mx-2 mb-1.5 overflow-hidden rounded-md border border-border bg-bg-secondary text-sm">
      <button
        type="button"
        aria-expanded={expanded}
        aria-label={expanded ? t('agent.collapseTodos') : t('agent.expandTodos')}
        onClick={() => setExpanded((open) => !open)}
        className="flex h-8 w-full items-center gap-2 px-2.5 text-left transition-colors hover:bg-bg-tertiary"
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
      <AnimatedCollapse open={expanded}>
        <div className="max-h-[200px] overflow-y-auto border-t border-border px-3 py-1.5">
          {todos.map((todo) => (
            <div
              key={todo.id}
              className={cn('flex items-start gap-2 py-1 text-xs', todo.done ? 'text-text-muted' : 'text-text-primary')}
            >
              <span className="mt-0.5 shrink-0">{todo.done ? '✓' : '○'}</span>
              <span className={cn('min-w-0 flex-1', todo.done && 'line-through')}>{todo.text}</span>
            </div>
          ))}
        </div>
      </AnimatedCollapse>
    </div>
  )
}
