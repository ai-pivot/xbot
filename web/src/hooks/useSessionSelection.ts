import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { collapseKey, groupSessions, isSubAgentSession, sessionKey, sortSessions } from '@/lib/session-grouping'
import type { SessionStore } from './useSessionStore'
import { matchesSelfOrDescendant } from '@/components/session/session-tree'
import type { SessionInfo } from '@/types/shared'

/** Same filtered, sorted main rows as SessionList; collapsed groups are outside the selection scope. */
export function selectableSessionRows(sessions: SessionInfo[], store: Pick<SessionStore, 'category' | 'starredIds' | 'collapsedGroups'>, search: string): SessionInfo[] {
  const main = sessions.filter(s => !isSubAgentSession(s) && !s.synthetic)
  const query = search.trim().toLowerCase()
  if (query) return sortSessions(main.filter(s => matchesSelfOrDescendant(s, candidate => candidate.label.toLowerCase().includes(query) || candidate.preview.toLowerCase().includes(query))), store.starredIds)
  return groupSessions(main, store.category, store.starredIds)
    .filter(g => !store.collapsedGroups.has(collapseKey(store.category, g.key)))
    .flatMap(g => g.sessions)
}

export function useSessionSelection(sessions: SessionInfo[], store: SessionStore, search: string) {
  const scope = JSON.stringify([store.activeChannel, store.category, search.trim().toLowerCase()])
  const [mode, setMode] = useState(false)
  const [busy, setBusy] = useState(false)
  const [state, setState] = useState({ scope, ids: new Set<string>(), anchor: null as string | null })
  const rows = useMemo(() => selectableSessionRows(sessions, store, search), [sessions, store.category, store.starredIds, store.collapsedGroups, search])
  const keys = useMemo(() => rows.map(sessionKey), [rows])
  const ids = useMemo(() => new Set(state.scope === scope ? keys.filter(k => state.ids.has(k)) : []), [state, scope, keys])
  const currentScope = useRef(scope)
  currentScope.current = scope
  const locked = useRef(false)
  // Discard hidden/deleted selections permanently; revealing rows later must not silently reselect them.
  useEffect(() => {
    setState(prev => {
      const keep = prev.scope === scope ? keys.filter(k => prev.ids.has(k)) : []
      if (prev.scope === scope && keep.length === prev.ids.size && (!prev.anchor || keys.includes(prev.anchor))) return prev
      return { scope, ids: new Set(keep), anchor: prev.scope === scope && prev.anchor && keys.includes(prev.anchor) ? prev.anchor : null }
    })
  }, [scope, keys])
  const clear = () => setState({ scope, ids: new Set(), anchor: null })
  const exit = () => { if (locked.current) return; clear(); setMode(false) }
  const all = () => { if (!locked.current) setState({ scope, ids: new Set(keys), anchor: null }) }
  const toggle = (key: string, shift: boolean) => {
    if (locked.current || !keys.includes(key)) return
    setState(prev => {
      const next = new Set(prev.scope === scope ? keys.filter(k => prev.ids.has(k)) : [])
      const anchor = prev.scope === scope ? prev.anchor : null
      const start = anchor ? keys.indexOf(anchor) : -1
      if (shift && start >= 0) {
        const end = keys.indexOf(key)
        keys.slice(Math.min(start, end), Math.max(start, end) + 1).forEach(k => next.add(k))
      } else if (next.has(key)) next.delete(key)
      else next.add(key)
      return { scope, ids: next, anchor: shift && anchor ? anchor : key }
    })
  }
  const onKeyDown = (event: KeyboardEvent) => {
    if (!mode || locked.current || event.defaultPrevented || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229 || event.repeat) return
    const target = event.target as Element
    if (target.closest('input, textarea, select, [contenteditable="true"], [role="textbox"]')) return
    const overlay = target.closest('[role="dialog"], [role="alertdialog"], [role="menu"]')
    // The mobile sidebar itself lives in a Sheet dialog. Only nested action dialogs own these keys.
    if (overlay && !overlay.contains(event.currentTarget)) return
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); exit() }
    else if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'a') {
      event.preventDefault(); event.stopPropagation(); all()
    }
  }
  return {
    mode, busy, scope, ids, rows, selected: rows.filter(s => ids.has(sessionKey(s))),
    toggle, all, clear: () => { if (!locked.current) clear() }, exit, onKeyDown,
    toggleMode: () => { if (locked.current) return; clear(); setMode(v => !v) },
    lock: (value: boolean) => { locked.current = value; setBusy(value) },
    keepOnly: (failed: string[], operationScope: string) => {
      if (currentScope.current === operationScope) setState({ scope: operationScope, ids: new Set(failed), anchor: null })
    },
  }
}
export type SessionSelection = ReturnType<typeof useSessionSelection>
