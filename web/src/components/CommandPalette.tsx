import { useEffect, useRef, useState, useCallback, useMemo, type ReactNode } from 'react'
import type { Virtualizer } from '@tanstack/react-virtual'
import type { Turn } from '../types'
import { useTranslation } from '../i18n'
import { splitByQuery } from '../utils/highlight'
import { IconUser, IconBot } from './Icons'

export interface CommandItem {
  id: string
  label: string
  icon: ReactNode
  description?: string
  action: () => void
}

interface CommandPaletteProps {
  open: boolean
  onClose: () => void
  commands: CommandItem[]
  // Search tab props — passed from ChatPage
  messagesContainerRef: React.RefObject<HTMLDivElement | null>
  virtualizer: Virtualizer<HTMLDivElement, Element>
  turns: Turn[]
}

interface SearchResult {
  id: number
  role: string
  snippet: string
  created_at: string
}

const HISTORY_KEY = 'xbot-command-history'
const MAX_HISTORY = 10

function loadHistory(): { id: string; ts: number }[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY)
    if (!raw) return []
    return JSON.parse(raw)
  } catch {
    return []
  }
}

function saveHistory(id: string) {
  try {
    const history = loadHistory().filter(h => h.id !== id)
    history.unshift({ id, ts: Date.now() })
    if (history.length > MAX_HISTORY) history.length = MAX_HISTORY
    localStorage.setItem(HISTORY_KEY, JSON.stringify(history))
  } catch { /* silent degradation */ }
}

/** Fuzzy sequence match: characters of query must appear in order in target */
function fuzzyMatch(query: string, target: string): boolean {
  const q = query.toLowerCase()
  const t = target.toLowerCase()
  let qi = 0
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) qi++
  }
  return qi === q.length
}

export default function CommandPalette({
  open,
  onClose,
  commands,
  messagesContainerRef,
  virtualizer,
  turns,
}: CommandPaletteProps) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'commands' | 'search'>('commands')
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [searchResults, setSearchResults] = useState<SearchResult[]>([])
  const [searchLoading, setSearchLoading] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const highlightTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Focus input when opened
  useEffect(() => {
    if (open) {
      setQuery('')
      setSelectedIndex(0)
      setSearchResults([])
      setTab('commands')
      setTimeout(() => inputRef.current?.focus(), 0)
    }
  }, [open])

  // Cleanup highlight timer
  useEffect(() => {
    return () => {
      if (highlightTimerRef.current) {
        clearTimeout(highlightTimerRef.current)
      }
    }
  }, [])

  // Filtered commands
  const filteredCommands = useMemo(() => {
    if (query) {
      return commands.filter(c => fuzzyMatch(query, c.label))
    }
    // No query: show recent + all
    const history = loadHistory()
    const recentIds = history.slice(0, 5).map(h => h.id)
    const recent = recentIds
      .map(id => commands.find(c => c.id === id))
      .filter((c): c is CommandItem => c !== undefined)
    const rest = commands.filter(c => !recentIds.includes(c.id))
    return [...recent, ...rest]
  }, [query, commands])

  // Reset selected index when filtered list changes
  useEffect(() => {
    setSelectedIndex(0)
  }, [query, tab])

  // Search: debounce 300ms (same logic as SearchPanel)
  useEffect(() => {
    if (tab !== 'search' || !query.trim()) {
      setSearchResults([])
      return
    }
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      setSearchLoading(true)
      try {
        const resp = await fetch(`/api/search?q=${encodeURIComponent(query.trim())}&limit=20`, {
          signal: controller.signal,
        })
        const data = await resp.json()
        if (data.ok) {
          setSearchResults(Array.isArray(data.results) ? data.results : [])
        }
      } catch (e) {
        if (e instanceof DOMException && e.name === 'AbortError') {
          setSearchLoading(false)
          return
        }
      }
      setSearchLoading(false)
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [query, tab])

  const executeCommand = useCallback((cmd: CommandItem) => {
    saveHistory(cmd.id)
    onClose()
    cmd.action()
  }, [onClose])

  // Handle search result click (same logic as SearchPanel)
  const handleResultClick = useCallback((hit: SearchResult) => {
    onClose()
    const targetMsgId = `hist-${hit.id}`

    const turnIndex = turns.findIndex(tr => {
      if (tr.type === 'user') return tr.message.id === targetMsgId
      return tr.messages.some(m => m.id === targetMsgId)
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
  }, [onClose, turns, virtualizer, messagesContainerRef])

  // Keyboard navigation
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    const maxIndex = tab === 'commands'
      ? filteredCommands.length - 1
      : searchResults.length - 1

    // Empty list: skip navigation keys (Escape still works)
    if (maxIndex < 0 && (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter')) {
      return
    }

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelectedIndex(prev => (prev < maxIndex ? prev + 1 : 0))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelectedIndex(prev => (prev > 0 ? prev - 1 : maxIndex))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (tab === 'commands' && filteredCommands[selectedIndex]) {
        executeCommand(filteredCommands[selectedIndex])
      } else if (tab === 'search' && searchResults[selectedIndex]) {
        handleResultClick(searchResults[selectedIndex])
      }
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    }
  }, [tab, filteredCommands, searchResults, selectedIndex, executeCommand, handleResultClick, onClose])

  // Scroll selected item into view
  useEffect(() => {
    if (!listRef.current) return
    const items = listRef.current.querySelectorAll('[data-command-item]')
    if (items[selectedIndex]) {
      items[selectedIndex].scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIndex])

  if (!open) return null

  return (
    <div className="command-palette-overlay" onClick={onClose} data-testid="command-palette">
      <div className="command-palette-panel" role="dialog" aria-modal="true" aria-label={t('commandPalette')} onClick={e => e.stopPropagation()}>
        {/* Tab bar */}
        <div className="command-palette-tabs">
          <button
            className={tab === 'commands' ? 'active' : ''}
            onClick={() => setTab('commands')}
            data-testid="command-palette-tab-commands"
          >
            {t('commandPaletteTabCommands')}
          </button>
          <button
            className={tab === 'search' ? 'active' : ''}
            onClick={() => setTab('search')}
            data-testid="command-palette-tab-search"
          >
            {t('commandPaletteTabSearch')}
          </button>
        </div>

        {/* Input */}
        <input
          ref={inputRef}
          type="text"
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={t('commandPaletteHint')}
          className="command-palette-input"
          data-testid="command-palette-input"
        />

        {/* Content */}
        <div className="command-palette-list" ref={listRef}>
          {tab === 'commands' && (
            filteredCommands.length > 0 ? (
              filteredCommands.map((cmd, idx) => (
                <div
                  key={cmd.id}
                  className={`command-palette-item ${idx === selectedIndex ? 'command-palette-item--selected' : ''}`}
                  onClick={() => executeCommand(cmd)}
                  onMouseEnter={() => setSelectedIndex(idx)}
                  data-command-item
                  data-testid={`command-item-${cmd.id}`}
                >
                  <span className="command-palette-item-icon">{cmd.icon}</span>
                  <div className="command-palette-item-text">
                    <span className="command-palette-item-label">{cmd.label}</span>
                    {cmd.description && (
                      <span className="command-palette-item-desc">{cmd.description}</span>
                    )}
                  </div>
                </div>
              ))
            ) : (
              <div className="command-palette-empty">{t('noMatchingCommands')}</div>
            )
          )}

          {tab === 'search' && (
            <>
              {searchLoading && (
                <div className="command-palette-empty">{t('searching')}</div>
              )}
              {!searchLoading && searchResults.length > 0 && (
                <>
                  <div className="command-palette-search-count">
                    {t('searchResults', { count: searchResults.length })}
                  </div>
                  {searchResults.map((hit, idx) => (
                    <div
                      key={hit.id}
                      className={`command-palette-search-result ${idx === selectedIndex ? 'command-palette-item--selected' : ''}`}
                      onClick={() => handleResultClick(hit)}
                      onMouseEnter={() => setSelectedIndex(idx)}
                      data-command-item
                    >
                      <div className="command-palette-search-result-header">
                        <span className="command-palette-search-result-role">
                          {hit.role === 'user' ? <IconUser className="inline" /> : <IconBot className="inline" />}
                        </span>
                        {hit.created_at && (
                          <span className="command-palette-search-result-time">
                            {new Date(hit.created_at).toLocaleString()}
                          </span>
                        )}
                      </div>
                      <div className="command-palette-search-result-snippet">
                        {query ? splitByQuery(hit.snippet, query).map((part, i) =>
                          part.isMatch
                            ? <mark key={i} className="bg-yellow-400/30 text-yellow-200 rounded px-0.5">{part.text}</mark>
                            : <span key={i}>{part.text}</span>
                        ) : hit.snippet}
                      </div>
                    </div>
                  ))}
                </>
              )}
              {!searchLoading && query && searchResults.length === 0 && (
                <div className="command-palette-empty">{t('noResults')}</div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
