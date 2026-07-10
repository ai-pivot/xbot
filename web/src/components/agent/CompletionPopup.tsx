/**
 * CompletionPopup — a floating candidate list above the message input.
 *
 * Renders when `visible` is true. The first candidate is highlighted by
 * default; ↑↓ navigates (handled by useCompletion). The popup is positioned
 * absolutely above the input, anchored to the bottom of the parent container.
 */
import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import type { CompletionCandidate } from '@/hooks/useCompletion'
import { useI18n } from '@/providers/i18n'

interface CompletionPopupProps {
  candidates: CompletionCandidate[]
  selectedIndex: number
  visible: boolean
  triggerType: 'command' | 'file' | null
  onSelect: (index: number) => void
}

export function CompletionPopup({
  candidates,
  selectedIndex,
  visible,
  triggerType,
  onSelect,
}: CompletionPopupProps) {
  const { t } = useI18n()
  const listRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to keep the selected item visible.
  useEffect(() => {
    if (!visible || !listRef.current) return
    const el = listRef.current.querySelector<HTMLElement>(
      `[data-idx="${selectedIndex}"]`,
    )
    el?.scrollIntoView?.({ block: 'nearest' })
  }, [selectedIndex, visible])

  if (!visible || candidates.length === 0) return null

  return (
    <div
      className="absolute bottom-full left-3 right-3 z-50 mb-1 overflow-hidden rounded-lg border border-border bg-bg-primary shadow-lg"
    >
      <div ref={listRef} className="max-h-60 overflow-y-auto py-1">
        {candidates.map((c, i) => (
          <div
            key={`${c.label}-${i}`}
            data-idx={i}
            onMouseDown={(e) => {
              // Prevent textarea blur on click
              e.preventDefault()
            }}
            onClick={() => onSelect(i)}
            className={cn(
              'flex items-center gap-2 px-3 py-1.5 text-sm',
              i === selectedIndex
                ? 'bg-accent/15 text-text-primary'
                : 'text-text-secondary hover:bg-bg-tertiary',
            )}
          >
            {triggerType === 'file' ? (
              <span className="shrink-0 text-xs">
                {c.isDir ? '📁' : '📄'}
              </span>
            ) : (
              <span className="shrink-0 text-xs text-text-muted">⌘</span>
            )}
            <span className={cn('min-w-0 flex-1 truncate', i === selectedIndex && 'font-medium')}>
              {c.label}
            </span>
            {c.description && (
              <span className="shrink-0 truncate text-xs text-text-muted">
                {c.description}
              </span>
            )}
          </div>
        ))}
      </div>
      <div className="border-t border-border px-3 py-1 text-xs text-text-muted">
        {t('agent.completionHint')}
      </div>
    </div>
  )
}
