import { useEffect, useMemo, useRef, useState } from 'react'
import { Search, MessageSquare, ArrowUp, ArrowDown, CornerDownLeft } from 'lucide-react'
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import { isSubAgentSession, sortSessions, sessionKey } from '@/lib/session-grouping'
import type { SessionInfo } from '@/types/shared'

export const OPEN_SESSION_SEARCH = 'xbot:session-search'
export function openSessionSearch() { window.dispatchEvent(new Event(OPEN_SESSION_SEARCH)) }

export function SessionSearchDialog({ onSelect }: { onSelect: (session: SessionInfo) => void }) {
  const { t } = useI18n()
  const store = useSessionStore()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const results = useMemo(() => {
    const q = query.trim().toLocaleLowerCase()
    return sortSessions(store.sessions.filter(s => !isSubAgentSession(s)), [])
      .filter(s => !q || [s.label, s.preview, s.chatID].some(text => text?.toLocaleLowerCase().includes(q)))
  }, [store.sessions, query])
  const selectedIndex = Math.max(0, results.findIndex(s => sessionKey(s) === selectedKey))

  useEffect(() => {
    const show = () => { setQuery(''); setSelectedKey(null); setOpen(true) }
    const keydown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.repeat) return
      if (!(event.metaKey || event.ctrlKey) || !event.shiftKey || event.altKey || event.code !== 'KeyF') return
      // Do not replace another modal's shortcut handling.
      if (!open && document.querySelector('[role="dialog"], [role="alertdialog"], [role="menu"]')) return
      event.preventDefault()
      if (open) input.current?.focus()
      else show()
    }
    window.addEventListener(OPEN_SESSION_SEARCH, show)
    window.addEventListener('keydown', keydown)
    return () => {
      window.removeEventListener(OPEN_SESSION_SEARCH, show)
      window.removeEventListener('keydown', keydown)
    }
  }, [open])

  useEffect(() => {
    if (open) document.getElementById(`session-search-result-${selectedIndex}`)?.scrollIntoView?.({ block: 'nearest' })
  }, [selectedIndex, open])

  const choose = (session: SessionInfo) => { onSelect(session); setOpen(false) }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent showCloseButton={false} className="gap-0 overflow-hidden border-border bg-bg-elevated p-0 text-text-primary sm:max-w-2xl">
        <DialogTitle className="sr-only">{t('session.quickSearchTitle')}</DialogTitle>
        <DialogDescription className="sr-only">{t('session.quickSearchScope')}</DialogDescription>
        <div className="flex items-center gap-3 border-b border-border px-5 py-4">
          <Search aria-hidden className="size-5 shrink-0 text-text-muted" />
          <input ref={input} autoFocus role="combobox" aria-label={t('session.quickSearchTitle')} aria-expanded="true" aria-controls="session-search-results" aria-autocomplete="list" aria-activedescendant={results.length ? `session-search-result-${selectedIndex}` : undefined}
            name="xbot-quick-session-search" autoComplete="off" autoCorrect="off" autoCapitalize="off" spellCheck={false} inputMode="search"
            value={query} placeholder={t('session.quickSearchPlaceholder')}
            onChange={event => { setQuery(event.target.value); setSelectedKey(null) }}
            onKeyDown={event => {
              if (event.nativeEvent.isComposing || event.keyCode === 229) return
              if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                event.preventDefault()
                if (results.length) setSelectedKey(sessionKey(results[(selectedIndex + (event.key === 'ArrowDown' ? 1 : -1) + results.length) % results.length]))
              } else if (event.key === 'Enter' && results[selectedIndex]) {
                event.preventDefault(); choose(results[selectedIndex])
              }
            }} className="min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-text-muted" />
          <button type="button" onClick={() => setOpen(false)} aria-label={t('common.close')} className="rounded border border-border px-1.5 py-0.5 text-[10px] text-text-muted hover:bg-bg-tertiary">Esc</button>
        </div>
        <div className="flex items-center justify-between px-5 pb-2 pt-3 text-[11px] text-text-muted">
          <span>{t(query.trim() ? 'session.quickSearchMatches' : 'session.quickSearchRecent')}</span>
          <span>{results.length}</span>
        </div>
        <div id="session-search-results" role="listbox" aria-label={t('session.quickSearchTitle')} className="max-h-[min(48dvh,420px)] overflow-y-auto px-2 pb-2">
          {results.map((session, i) => (
            <div key={sessionKey(session)} id={`session-search-result-${i}`} role="option" aria-selected={i === selectedIndex} onClick={() => choose(session)}
              className={`flex cursor-pointer items-start gap-3 rounded-lg px-3 py-3 ${i === selectedIndex ? 'bg-bg-tertiary' : 'hover:bg-bg-tertiary/60'}`}>
              <MessageSquare aria-hidden className="mt-0.5 size-4 shrink-0 text-text-muted" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{session.label || session.chatID}</div>
                {session.preview && <div className="mt-1 line-clamp-2 text-xs leading-relaxed text-text-muted">{session.preview}</div>}
                <div className="mt-1 truncate text-[10px] text-text-muted">{session.channel} · {session.chatID}</div>
              </div>
              {i === selectedIndex && <CornerDownLeft aria-hidden className="mt-1 size-3.5 shrink-0 text-text-muted" />}
            </div>
          ))}
          {!results.length && <div role="status" className="px-4 py-12 text-center text-sm text-text-muted">{t('session.quickSearchEmpty')}</div>}
          {store.hasMore && <button type="button" disabled={store.loading} onClick={() => { void store.loadMore() }} className="w-full rounded-md p-3 text-xs text-text-muted hover:bg-bg-tertiary disabled:opacity-50">{t('session.quickSearchLoadMore')}</button>}
        </div>
        <div className="flex items-center justify-between gap-3 border-t border-border px-5 py-3 text-[10px] text-text-muted">
          <span>{t('session.quickSearchScope')}</span>
          <span className="flex shrink-0 items-center gap-1"><ArrowUp className="size-3" /><ArrowDown className="size-3" />{t('session.quickSearchNavigate')}<CornerDownLeft className="ml-2 size-3" />{t('session.quickSearchOpen')}</span>
        </div>
      </DialogContent>
    </Dialog>
  )
}
