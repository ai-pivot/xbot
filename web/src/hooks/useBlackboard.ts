/**
 * useBlackboard — the shared blackboard of a session (cross-agent workspace).
 *
 * Data flow is deliberately one-directional: the SSE `blackboard_update` event is
 * only a REFETCH signal, and the board is re-read from the server. Keeping a
 * single authority means a missed broadcast costs one refresh instead of
 * consistency — the same contract the session/progress streams use.
 *
 * Lease countdowns are computed locally from `claim_expires_at` with a 1s tick;
 * they never poll the server (a countdown is arithmetic, not data).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  closeBlackboardEntry,
  deleteBlackboardEntry,
  fetchBlackboardBoards,
  fetchBlackboardEntries,
  fetchBlackboardEntry,
  postBlackboardEntry,
  releaseBlackboardEntry,
} from '@/components/agent/api'
import type { BlackboardBoard, BlackboardEntry, SessionSelector } from '@/types/shared'

/** Coalesce a burst of board events into at most one refetch per window. */
const REFRESH_DEBOUNCE_MS = 120
/** Fallback refresh: the live signal is the SSE event; this only covers a lost
 *  one (and keeps the panel honest when a broadcast was dropped). */
const FALLBACK_REFRESH_MS = 30_000

export interface BlackboardState {
  /** The board being displayed ("" = this session's board). */
  selectedBoard: string
  /** Resolved board key the entries belong to (never empty once loaded). */
  board: string
  entries: BlackboardEntry[]
  boards: BlackboardBoard[]
  /** Lazily loaded bodies, keyed by entry key (list omits bodies on purpose). */
  bodies: Record<string, string>
  loading: boolean
  error: string | null
  /** Ticking clock (unix ms) for lease countdowns — pure client-side. */
  now: number
  refresh: () => void
  selectBoard: (board: string) => void
  loadBody: (key: string) => Promise<void>
  post: (entry: {
    key: string
    kind?: string
    title: string
    body?: string
    blocked_by?: string[]
  }) => Promise<void>
  setClosed: (key: string, closed: boolean) => Promise<void>
  release: (key: string) => Promise<void>
  remove: (key: string) => Promise<void>
}

export function useBlackboard(session: SessionSelector | null): BlackboardState {
  const [selectedBoard, setSelectedBoard] = useState('')
  const [board, setBoard] = useState('')
  const [entries, setEntries] = useState<BlackboardEntry[]>([])
  const [boards, setBoards] = useState<BlackboardBoard[]>([])
  const [bodies, setBodies] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())

  const sessionRef = useRef(session)
  sessionRef.current = session
  const boardRef = useRef(selectedBoard)
  boardRef.current = selectedBoard
  const seqRef = useRef(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const sessionKey = session ? `${session.channel}:${session.chatID}` : ''

  const load = useCallback(async () => {
    const seq = ++seqRef.current
    setLoading(true)
    try {
      const [list, allBoards] = await Promise.all([
        fetchBlackboardEntries(sessionRef.current, { board: boardRef.current, includeClosed: true }),
        fetchBlackboardBoards(),
      ])
      // Drop a stale response (session/board switched while in flight) — the
      // panel must never render another board's entries.
      if (seq !== seqRef.current) return
      setBoard(list.board)
      setEntries(list.entries)
      setBoards(allBoards)
      setError(null)
    } catch (err) {
      if (seq !== seqRef.current) return
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      if (seq === seqRef.current) setLoading(false)
    }
  }, [])

  const refresh = useCallback(() => {
    void load()
  }, [load])

  // Load on mount, session switch and board switch.
  useEffect(() => {
    setBodies({})
    if (!sessionKey && !selectedBoard) {
      setEntries([])
      setBoard('')
      return
    }
    void load()
  }, [load, sessionKey, selectedBoard])

  // Live updates: the SSE broadcast is a refetch signal, debounced so a burst
  // of changes costs one round-trip.
  useEffect(() => {
    const onUpdate = () => {
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        void load()
      }, REFRESH_DEBOUNCE_MS)
    }
    window.addEventListener('blackboard-update', onUpdate)
    return () => {
      window.removeEventListener('blackboard-update', onUpdate)
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [load])

  // Fallback poll (only while the panel is mounted).
  useEffect(() => {
    const id = setInterval(() => void load(), FALLBACK_REFRESH_MS)
    return () => clearInterval(id)
  }, [load])

  // Lease countdowns: local clock only.
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  const loadBody = useCallback(async (key: string) => {
    const current = boardRef.current || board
    if (!current) return
    const entry = await fetchBlackboardEntry(current, key)
    setBodies((prev) => ({ ...prev, [key]: entry.body ?? '' }))
  }, [board])

  const afterMutation = useCallback(async () => {
    await load()
  }, [load])

  const post = useCallback<BlackboardState['post']>(async (entry) => {
    await postBlackboardEntry(sessionRef.current, { ...entry, board: boardRef.current })
    await afterMutation()
  }, [afterMutation])

  const setClosed = useCallback(async (key: string, closed: boolean) => {
    const current = boardRef.current || board
    try {
      await closeBlackboardEntry(current, key, closed)
      await afterMutation()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      await afterMutation() // surface the authoritative state after a conflict
    }
  }, [afterMutation, board])

  const release = useCallback(async (key: string) => {
    const current = boardRef.current || board
    await releaseBlackboardEntry(current, key)
    await afterMutation()
  }, [afterMutation, board])

  const remove = useCallback(async (key: string) => {
    const current = boardRef.current || board
    await deleteBlackboardEntry(current, key)
    setBodies((prev) => {
      const next = { ...prev }
      delete next[key]
      return next
    })
    await afterMutation()
  }, [afterMutation, board])

  return useMemo(
    () => ({
      selectedBoard, board, entries, boards, bodies, loading, error, now,
      refresh, selectBoard: setSelectedBoard, loadBody, post, setClosed, release, remove,
    }),
    [selectedBoard, board, entries, boards, bodies, loading, error, now, refresh, loadBody, post, setClosed, release, remove],
  )
}
