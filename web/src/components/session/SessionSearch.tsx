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
      className="flex min-w-0 flex-1 items-center gap-2 rounded-xl border px-3 py-2"
      style={{ borderColor: 'var(--border)', background: 'var(--app-bg)' }}
    >
      <Search className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
      {/* readOnly-until-focus: Chrome IGNORES autoComplete="off" for form-history
          fill (it offered the saved login username "adm" in this box). A readOnly
          input can NEVER be autofilled — flip it synchronously in onFocus (direct
          DOM write, lands before the first keystroke; React won't reset it because
          the JSX prop never changes). autoComplete stays as belt-and-suspenders. */}
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={t('session.searchPlaceholder')}
        autoComplete="off"
        readOnly
        onFocus={(e) => { if (e.currentTarget.readOnly) e.currentTarget.readOnly = false }}
        className="h-6 flex-1 bg-transparent text-xs outline-none placeholder:text-text-muted"
        style={{ color: 'var(--text-primary)' }}
        aria-label={t('common.search')}
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange('')}
          aria-label={t('common.close')}
          className="shrink-0 rounded p-0.5 hover:bg-surface-bg"
          style={{ color: 'var(--text-muted)' }}
        >
          <X className="size-3.5" />
        </button>
      )}
    </div>
  )
}
