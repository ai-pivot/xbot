/**
 * SessionEmptyState — shown when the (filtered) session list has no rows.
 *
 * Distinguishes "no sessions at all" from "no search match" so the user gets
 * an actionable hint in each case.
 */
import { Inbox, Search } from 'lucide-react'

import { useI18n } from '@/providers/i18n'

interface SessionEmptyStateProps {
  /** True when there are zero sessions (vs. a search that matched nothing). */
  emptyList: boolean
}

export function SessionEmptyState({ emptyList }: SessionEmptyStateProps) {
  const { t } = useI18n()
  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center"
      style={{ color: 'var(--text-muted)' }}
    >
      {/* Apple 式空态：图标容器 + 层次（图标 → 说明），而非一行裸文字 */}
      <div className="flex size-12 items-center justify-center rounded-2xl bg-bg-tertiary/40 ring-1 ring-border/40">
        {emptyList ? <Inbox className="size-5 opacity-50" /> : <Search className="size-5 opacity-50" />}
      </div>
      <p className="max-w-[220px] text-xs leading-relaxed opacity-80">
        {emptyList ? t('session.empty') : t('session.noResults')}
      </p>
    </div>
  )
}
