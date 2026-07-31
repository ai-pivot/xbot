/**
 * SessionEmptyState — shown when the (filtered) session list has no rows.
 *
 * Distinguishes "no sessions at all" from "no search match" so the user gets
 * an actionable hint in each case.
 */
import { useI18n } from '@/providers/i18n'

interface SessionEmptyStateProps {
  /** True when there are zero sessions (vs. a search that matched nothing). */
  emptyList: boolean
}

export function SessionEmptyState({ emptyList }: SessionEmptyStateProps) {
  const { t } = useI18n()
  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center"
      style={{ color: 'var(--text-muted)' }}
    >
      <p className="text-xs">
        {emptyList ? t('session.empty') : t('session.noResults')}
      </p>
    </div>
  )
}
