/**
 * ContextBar — compact progress bar above the input area (Spec C §1.2).
 *
 * Replaces TodoPullOut. Fuses two progress indicators in one compact bar:
 *   - TODO progress (left, accent/20 fill)
 *   - Context usage (overlay, accent/10 fill, red when >80%)
 *   - Right-side info: model | promptTokens/maxContext | percentage
 *
 * Click to expand/collapse the full TODO list.
 * Uses Framer Motion for expand/collapse animation.
 */
import { useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { TodoState } from '@/hooks/useTodos'
import { formatTokenCount } from '@/hooks/useSessionContext'
import { useI18n } from '@/providers/i18n'

interface ContextBarProps {
  /** TODO state from progress snapshot; null/total=0 hides TODO progress. */
  todoState: TodoState | null
  /** Current model name (from session subscription). */
  model: string
  /** Max context tokens (from resolution chain). */
  maxContext: number
  /** Current prompt tokens (from SSE tokenUsage). */
  promptTokens: number
}

export function ContextBar({ todoState, model, maxContext, promptTokens }: ContextBarProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)

  const hasTodos = todoState && todoState.total > 0
  const todoPct = hasTodos ? Math.round((todoState!.doneCount / todoState!.total) * 100) : 0
  const contextPct = maxContext > 0 && promptTokens > 0
    ? Math.min(100, Math.round((promptTokens / maxContext) * 100))
    : 0
  const isContextHigh = contextPct >= 80

  // Determine if clicking should toggle the TODO list
  const canExpand = hasTodos && todoState!.total > 0

  return (
    <div className="mx-1.5 mb-1.5 overflow-visible">
      {/* Collapsed summary — click to expand/collapse TODO list */}
      <button
        type="button"
        onClick={() => canExpand && setExpanded((v) => !v)}
        className={cn(
          'relative flex h-6 w-full items-center overflow-hidden rounded-md bg-bg-secondary/50',
          canExpand && 'cursor-pointer hover:bg-bg-secondary/80',
        )}
        title={canExpand ? (expanded ? t('agent.collapseTodos') : t('agent.expandTodos')) : undefined}
      >
        {/* TODO progress fill (top half, accent/20) */}
        {hasTodos && todoPct > 0 && (
          <div
            className="absolute left-0 top-0 h-1/2 bg-accent/20 transition-all duration-300"
            style={{ width: `${todoPct}%` }}
          />
        )}

        {/* Context usage fill (bottom half, accent/10 or red when >80%) */}
        {contextPct > 0 && (
          <div
            className={cn(
              'absolute left-0 bottom-0 h-1/2 transition-all duration-300',
              isContextHigh ? 'bg-red-500/20' : 'bg-accent/10',
            )}
            style={{ width: `${contextPct}%` }}
          />
        )}

        {/* Left side: TODO text (if todos) */}
        <div className="relative z-10 flex items-center gap-1.5 pl-2">
          {canExpand && (
            <ChevronDown
              className={cn(
                'size-3 shrink-0 text-text-muted transition-transform',
                expanded && 'rotate-180',
              )}
            />
          )}
          {hasTodos ? (
            <span className="text-[10px] tabular-nums text-accent-foreground/70">
              {todoState!.doneCount}/{todoState!.total} {t('agent.todoCompleted')}
            </span>
          ) : null}
        </div>

        {/* Right side: model + context info */}
        <div className="relative z-10 ml-auto flex max-w-[200px] items-center gap-1 pr-2">
          <span className="max-w-[120px] truncate text-[10px] font-mono text-muted-foreground">
            {model || '—'}
          </span>
          {maxContext > 0 && (
            <span className="text-[10px] font-mono tabular-nums text-muted-foreground">
              {formatTokenCount(promptTokens)}/{formatTokenCount(maxContext)}
            </span>
          )}
          {maxContext > 0 && (
            <span
              className={cn(
                'text-[10px] font-mono tabular-nums',
                isContextHigh ? 'text-red-500' : 'text-muted-foreground',
              )}
            >
              {contextPct}%
            </span>
          )}
        </div>
      </button>

      {/* Expanded TODO list */}
      <AnimatePresence initial={false}>
        {expanded && hasTodos && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: 'auto', opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.2 }}
            className="overflow-hidden rounded-md border border-border bg-bg-secondary"
          >
            <div className="max-h-[200px] overflow-y-auto px-3 py-1.5">
              {todoState!.todos.map((todo) => (
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
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
