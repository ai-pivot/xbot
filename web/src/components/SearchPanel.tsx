import { useEffect, useRef, useState } from 'react'
import type { Virtualizer } from '@tanstack/react-virtual'
import type { Turn } from '../types'
import { useTranslation } from '../i18n'
import { splitByQuery } from '../utils/highlight'

interface SearchResult {
  id: number
  role: string
  snippet: string
  created_at: string
}

interface SearchPanelProps {
  open: boolean
  onClose: () => void
  messagesContainerRef: React.RefObject<HTMLDivElement | null>
  virtualizer: Virtualizer<HTMLDivElement, Element>
  turns: Turn[]
}

export default function SearchPanel({ open, onClose, messagesContainerRef, virtualizer, turns }: SearchPanelProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<SearchResult[]>([])
  const [searchLoading, setSearchLoading] = useState(false)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const highlightTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const { t } = useTranslation()

  // Focus input when panel opens
  useEffect(() => {
    if (open) {
      setSearchQuery('')
      setSearchResults([])
      setTimeout(() => searchInputRef.current?.focus(), 0)
    }
  }, [open])

  // Cleanup highlight timer on unmount
  useEffect(() => {
    return () => {
      if (highlightTimerRef.current) {
        clearTimeout(highlightTimerRef.current)
      }
    }
  }, [])

  // Search: debounce 300ms
  useEffect(() => {
    if (!open || !searchQuery.trim()) {
      setSearchResults([])
      return
    }
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      setSearchLoading(true)
      try {
        const resp = await fetch(`/api/search?q=${encodeURIComponent(searchQuery.trim())}&limit=20`, {
          signal: controller.signal,
        })
        const data = await resp.json()
        if (data.ok) {
          setSearchResults(data.results || [])
        }
      } catch (e) {
        if (e instanceof DOMException && e.name === 'AbortError') return
      }
      setSearchLoading(false)
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [searchQuery, open])

  if (!open) return null

  const handleResultClick = (hit: SearchResult) => {
    onClose()
    const targetMsgId = `hist-${hit.id}`

    const turnIndex = turns.findIndex(t => {
      if (t.type === 'user') return t.message.id === targetMsgId
      return t.messages.some(m => m.id === targetMsgId)
    })

    if (turnIndex >= 0) {
      virtualizer.scrollToIndex(turnIndex, { align: 'center', behavior: 'smooth' })

      if (highlightTimerRef.current) {
        clearTimeout(highlightTimerRef.current)
      }

      let attempts = 0
      const tryHighlight = () => {
        const el = messagesContainerRef.current?.querySelector(`[data-msg-id="${targetMsgId}"]`)
        if (el) {
          el.classList.add('search-highlight')
          highlightTimerRef.current = setTimeout(() => {
            el.classList.remove('search-highlight')
            highlightTimerRef.current = null
          }, 2000)
        } else if (attempts < 5) {
          attempts++
          highlightTimerRef.current = setTimeout(tryHighlight, 100)
        }
      }
      highlightTimerRef.current = setTimeout(tryHighlight, 150)
    }
  }

  return (
    <div className="bg-slate-800/95 border-b border-slate-700 px-4 py-3 backdrop-blur-sm" role="search" aria-label={t('searchMessages')}>
      <div className="max-w-2xl mx-auto">
        <div className="relative">
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            onKeyDown={e => { if (e.key === 'Escape') onClose() }}
            placeholder={t('searchMessages')}
            autoFocus
            className="w-full px-4 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-400 focus:outline-none focus:border-blue-500"
          />
          {searchLoading && <span className="absolute right-3 top-1/2 -translate-y-1/2 text-xs text-slate-400">{t('searching')}</span>}
        </div>
        {searchResults.length > 0 && (
          <>
            <div className="flex items-center justify-between mt-2 mb-1">
              <span className="text-xs text-slate-500">{t('searchResults', { count: searchResults.length })}</span>
            </div>
            <div className="max-h-64 overflow-y-auto space-y-1">
              {searchResults.map(hit => (
                <div
                  key={hit.id}
                  className="px-3 py-2 rounded-lg bg-slate-700/50 hover:bg-slate-700 cursor-pointer text-sm"
                  onClick={() => handleResultClick(hit)}
                >
                  <div className="flex items-center gap-2 mb-1">
                    <span className="text-xs font-medium text-slate-400">{hit.role === 'user' ? '👤' : '🤖'}</span>
                    {hit.created_at && <span className="text-xs text-slate-500">{new Date(hit.created_at).toLocaleString()}</span>}
                  </div>
                  <div className="text-slate-300 text-xs line-clamp-2 whitespace-pre-wrap break-words">
                    {searchQuery ? splitByQuery(hit.snippet, searchQuery).map((part, i) =>
                      part.isMatch
                        ? <mark key={i} className="bg-yellow-400/30 text-yellow-200 rounded px-0.5">{part.text}</mark>
                        : <span key={i}>{part.text}</span>
                    ) : hit.snippet}
                  </div>
                </div>
              ))}
            </div>
          </>
        )}
        {searchQuery && !searchLoading && searchResults.length === 0 && (
          <div className="mt-2 text-center text-xs text-slate-500">{t('noResults')}</div>
        )}
      </div>
    </div>
  )
}
