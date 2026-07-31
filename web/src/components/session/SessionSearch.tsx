/**
 * SessionSearch — the realtime filter box at the top of the sidebar (Spec 3 §3.7).
 *
 * Pure frontend filtering: matches label OR preview, case-insensitive. While a
 * query is present the list ignores category grouping and shows a flat sorted
 * result. Clearing the query restores the grouped view.
 */
import { Search, X } from 'lucide-react'
import { useI18n } from '@/providers/i18n'

interface SessionSearchProps {
  value: string
  onChange: (v: string) => void
}

export function SessionSearch({ value, onChange }: SessionSearchProps) {
  const { t } = useI18n()
  return (
    <div
      className="flex items-center gap-1.5 px-2 py-1.5"
      style={{ borderBottom: '1px solid var(--border)' }}
    >
      <Search className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={t('session.searchPlaceholder')}
        className="h-6 flex-1 bg-transparent text-xs outline-none placeholder:text-text-muted"
        style={{ color: 'var(--text-primary)' }}
        aria-label={t('common.search')}
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange('')}
          aria-label={t('common.close')}
          className="shrink-0 rounded p-0.5 hover:bg-bg-tertiary"
          style={{ color: 'var(--text-muted)' }}
        >
          <X className="size-3.5" />
        </button>
      )}
    </div>
  )
}
