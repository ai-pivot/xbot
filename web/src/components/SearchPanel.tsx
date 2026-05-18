import { useEffect, useRef, useState, useMemo } from 'react'
import type { Virtualizer } from '@tanstack/react-virtual'
import type { Turn, Message } from '../types'
import { useTranslation } from '../i18n'
import type { I18nKey } from '../i18n'
import { splitByQuery } from '../utils/highlight'

interface SearchResult {
  id: string
  role: string
  snippet: string
  context: string  // surrounding context
  created_at: string
  message: Message
  turnIndex: number
}

type FilterRole = 'all' | 'user' | 'assistant'
type FilterDate = 'all' | 'today' | 'week' | 'month'

interface SearchPanelProps {
  open: boolean
  onClose: () => void
  messagesContainerRef: React.RefObject<HTMLDivElement | null>
  virtualizer: Virtualizer<HTMLDivElement, Element>
  turns: Turn[]
}

const SEARCH_HISTORY_KEY = 'xbot-search-history'
const MAX_HISTORY = 10

function loadSearchHistory(): string[] {
  try {
    const raw = localStorage.getItem(SEARCH_HISTORY_KEY)
    return raw ? JSON.parse(raw) : []
  } catch { return [] }
}

function saveSearchHistory(history: string[]) {
  try {
    localStorage.setItem(SEARCH_HISTORY_KEY, JSON.stringify(history.slice(0, MAX_HISTORY)))
  } catch { /* ignore */ }
}

export default function SearchPanel({ open, onClose, messagesContainerRef, virtualizer, turns }: SearchPanelProps) {
  const [searchQuery, setSearchQuery] = useState('')
  const [filterRole, setFilterRole] = useState<FilterRole>('all')
  const [filterDate, setFilterDate] = useState<FilterDate>('all')
  const [searchHistory, setSearchHistory] = useState(loadSearchHistory)
  const [showHistory, setShowHistory] = useState(false)
  const [expandedResult, setExpandedResult] = useState<string | null>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const highlightTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const { t } = useTranslation()

  // Current time for date filtering — updated periodically via timer
  const [now, setNow] = useState(() => Date.now())

  // Focus input when panel opens
  useEffect(() => {
    if (open) {
      setSearchQuery('')
      setShowHistory(true)
      setTimeout(() => searchInputRef.current?.focus(), 0)
    }
  }, [open])

  // Keep now up-to-date for date filtering
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000)
    return () => clearInterval(timer)
  }, [])

  // Cleanup highlight timer on unmount
  useEffect(() => {
    return () => {
      if (highlightTimerRef.current) clearTimeout(highlightTimerRef.current)
    }
  }, [])

  // Collect all messages from turns
  const allMessages = useMemo(() => {
    const msgs: { message: Message; turnIndex: number }[] = []
    turns.forEach((turn, idx) => {
      if (turn.type === 'user') {
        msgs.push({ message: turn.message, turnIndex: idx })
      } else {
        turn.messages.forEach(m => msgs.push({ message: m, turnIndex: idx }))
      }
    })
    return msgs
  }, [turns])

  // Client-side search
  const searchResults = useMemo(() => {
    if (!searchQuery.trim()) return [] as SearchResult[]

    const q = searchQuery.trim().toLowerCase()
    const oneDayMs = 86400000
    const results: SearchResult[] = []

    for (const { message, turnIndex } of allMessages) {
      // Role filter
      if (filterRole !== 'all') {
        if (filterRole === 'user' && message.type !== 'user') continue
        if (filterRole === 'assistant' && message.type !== 'assistant') continue
      }

      // Date filter
      if (filterDate !== 'all' && message.ts) {
        const age = now - message.ts
        if (filterDate === 'today' && age > oneDayMs) continue
        if (filterDate === 'week' && age > 7 * oneDayMs) continue
        if (filterDate === 'month' && age > 30 * oneDayMs) continue
      }

      // Text match
      const content = message.content.toLowerCase()
      if (!content.includes(q)) continue

      // Extract snippet with context around the match
      const idx = content.indexOf(q)
      const start = Math.max(0, idx - 50)
      const end = Math.min(message.content.length, idx + searchQuery.length + 50)
      const snippet = (start > 0 ? '...' : '') + message.content.slice(start, end) + (end < message.content.length ? '...' : '')
      const context = message.content.slice(Math.max(0, idx - 100), Math.min(message.content.length, idx + searchQuery.length + 100))

      results.push({
        id: message.id,
        role: message.type,
        snippet,
        context,
        created_at: message.ts ? new Date(message.ts).toISOString() : '',
        message,
        turnIndex,
      })
    }

    return results.slice(0, 30)
  }, [searchQuery, allMessages, filterRole, filterDate, now])

  if (!open) return null

  const handleResultClick = (hit: SearchResult) => {
    // Save to search history
    const newHistory = [searchQuery.trim(), ...searchHistory.filter(h => h !== searchQuery.trim())].slice(0, MAX_HISTORY)
    setSearchHistory(newHistory)
    saveSearchHistory(newHistory)

    onClose()
    const targetMsgId = hit.id

    virtualizer.scrollToIndex(hit.turnIndex, { align: 'center', behavior: 'smooth' })

    if (highlightTimerRef.current) clearTimeout(highlightTimerRef.current)

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

  const handleHistoryClick = (query: string) => {
    setSearchQuery(query)
    setShowHistory(false)
    searchInputRef.current?.focus()
  }

  const clearHistory = () => {
    setSearchHistory([])
    saveSearchHistory([])
  }

  return (
    <div className="bg-slate-800/95 border-b border-slate-700 px-4 py-3 backdrop-blur-sm" role="search" aria-label={t('searchMessages')}>
      <div className="max-w-2xl mx-auto">
        <div className="relative">
          <input
            ref={searchInputRef}
            type="text"
            value={searchQuery}
            onChange={e => { setSearchQuery(e.target.value); setShowHistory(false) }}
            onFocus={() => { if (!searchQuery.trim() && searchHistory.length > 0) setShowHistory(true) }}
            onKeyDown={e => { if (e.key === 'Escape') onClose() }}
            placeholder={t('searchMessages')}
            autoFocus
            className="w-full px-4 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-400 focus:outline-none focus:border-blue-500"
          />
        </div>

        {/* Filters */}
        <div className="flex items-center gap-2 mt-2">
          <span className="text-xs text-slate-500">{t('searchFilter')}:</span>
          <div className="flex gap-1">
            {(['all', 'user', 'assistant'] as FilterRole[]).map(role => (
              <button
                key={role}
                className={`px-2 py-0.5 text-xs rounded ${filterRole === role ? 'bg-slate-600 text-white' : 'text-slate-400 hover:text-white hover:bg-slate-700'}`}
                onClick={() => setFilterRole(role)}
                data-testid={`filter-role-${role}`}
              >
                {t(`searchFilter${role.charAt(0).toUpperCase() + role.slice(1)}` as I18nKey)}
              </button>
            ))}
          </div>
          <span className="text-xs text-slate-600">|</span>
          <div className="flex gap-1">
            {(['all', 'today', 'week', 'month'] as FilterDate[]).map(date => (
              <button
                key={date}
                className={`px-2 py-0.5 text-xs rounded ${filterDate === date ? 'bg-slate-600 text-white' : 'text-slate-400 hover:text-white hover:bg-slate-700'}`}
                onClick={() => setFilterDate(date)}
                data-testid={`filter-date-${date}`}
              >
                {t(`searchFilter${date.charAt(0).toUpperCase() + date.slice(1)}` as I18nKey)}
              </button>
            ))}
          </div>
        </div>

        {/* Search history (when no query) */}
        {showHistory && !searchQuery.trim() && searchHistory.length > 0 && (
          <div className="mt-2">
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-slate-500">{t('searchHistoryR14')}</span>
              <button onClick={clearHistory} className="text-xs text-slate-500 hover:text-slate-300" data-testid="clear-search-history">
                {t('clearSearchHistory')}
              </button>
            </div>
            <div className="flex flex-wrap gap-1">
              {searchHistory.map((h, i) => (
                <button
                  key={i}
                  className="px-2 py-1 text-xs rounded bg-slate-700/50 text-slate-400 hover:text-white hover:bg-slate-700"
                  onClick={() => handleHistoryClick(h)}
                >
                  {h}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Results */}
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
                    <button
                      className="text-xs text-slate-500 hover:text-slate-300 ml-auto"
                      onClick={(e) => { e.stopPropagation(); setExpandedResult(expandedResult === hit.id ? null : hit.id) }}
                      data-testid={`toggle-context-${hit.id}`}
                    >
                      {expandedResult === hit.id ? t('hideContext') : t('showContext')}
                    </button>
                  </div>
                  <div className="text-slate-300 text-xs line-clamp-2 whitespace-pre-wrap break-words">
                    {searchQuery ? splitByQuery(hit.snippet, searchQuery).map((part, i) =>
                      part.isMatch
                        ? <mark key={i} className="bg-yellow-400/30 text-yellow-200 rounded px-0.5">{part.text}</mark>
                        : <span key={i}>{part.text}</span>
                    ) : hit.snippet}
                  </div>
                  {expandedResult === hit.id && (
                    <div className="mt-1 p-2 rounded bg-slate-800/50 text-xs text-slate-400 whitespace-pre-wrap break-words border border-slate-700/50" data-testid={`context-${hit.id}`}>
                      {hit.context}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </>
        )}
        {searchQuery && searchResults.length === 0 && (
          <div className="mt-2 text-center text-xs text-slate-500">{t('noResults')}</div>
        )}
      </div>
    </div>
  )
}
