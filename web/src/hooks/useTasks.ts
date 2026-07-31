/**
 * useTasks — fetches Cron tasks and background Shell tasks via Web REST APIs.
 *
 * Refreshes every 30 seconds and on session switch.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchCronTasks, fetchBackgroundTasks } from '@/components/agent/api'
import type { WSConnection } from '@/types/ws'
import type { SessionSelector } from '@/types/shared'

/** Cron job (mirrors Go storage/sqlite/cron.go CronJob). */
export interface CronTask {
  id: string
  message: string
  channel: string
  chatID: string
  cronExpr?: string
  everySeconds?: number
  delaySeconds?: number
  at?: string
  createdAt?: string
  nextRun?: string
  oneShot?: boolean
}

/** Background shell task (mirrors Go serverapp/rpc_table.go bgTaskJSON). */
export interface BgTask {
  id: string
  command: string
  status: string
  startedAt: string
  finishedAt?: string
  exitCode: number
  error?: string
  output?: string
}

export interface TasksState {
  cronTasks: CronTask[]
  bgTasks: BgTask[]
  loading: boolean
  error: string | null
  refresh: () => void
  killBgTask: (taskID: string) => Promise<void>
}

const REFRESH_INTERVAL_MS = 30_000

export function useTasks(ws: WSConnection, session: SessionSelector | null): TasksState {
  const [cronTasks, setCronTasks] = useState<CronTask[]>([])
  const [bgTasks, setBgTasks] = useState<BgTask[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const sessionRef = useRef(session)
  sessionRef.current = session
  const sessionKeyForEffect = session ? `${session.channel}:${session.chatID}` : ''
  const lastSessionKeyRef = useRef<string | null>(null)
  const hasLoadedRef = useRef(false)
  const refreshSeqRef = useRef(0)

  const refresh = useCallback(async () => {
    const current = sessionRef.current
    if (!current) return
    const seq = ++refreshSeqRef.current
    const sessionKey = `${current.channel}:${current.chatID}`
    const firstLoadForSession = lastSessionKeyRef.current !== sessionKey || !hasLoadedRef.current
    if (lastSessionKeyRef.current !== sessionKey) {
      setCronTasks([])
      setBgTasks([])
      hasLoadedRef.current = false
    }
    lastSessionKeyRef.current = sessionKey
    setLoading(firstLoadForSession)
    setError(null)
    try {
      const [cron, bg] = await Promise.all([
        fetchCronTasks<CronTask>(current),
        fetchBackgroundTasks<unknown>(current),
      ])
      const latest = sessionRef.current
      if (seq !== refreshSeqRef.current || !latest || `${latest.channel}:${latest.chatID}` !== sessionKey) return
      setCronTasks(cron)
      setBgTasks(bg.map(normalizeBgTask).filter(isRunningBgTask))
      hasLoadedRef.current = true
    } catch (e) {
      if (seq !== refreshSeqRef.current) return
      setError(e instanceof Error ? e.message : 'fetch failed')
    } finally {
      if (seq === refreshSeqRef.current) setLoading(false)
    }
  }, [ws])

  const killBgTask = useCallback(async (taskID: string) => {
    if (!taskID) return
    await ws.rpc('kill_bg_task', { task_id: taskID })
    await refresh()
  }, [refresh, ws])

  // Refresh on mount + session switch.
  useEffect(() => {
    void refresh()
  }, [refresh, sessionKeyForEffect])

  // Auto-refresh every 30s.
  useEffect(() => {
    if (!sessionKeyForEffect) return
    const hasRunning = bgTasks.some((t) => t.status === 'running' || t.status === 'started')
    const timer = setInterval(() => void refresh(), hasRunning ? 2_000 : REFRESH_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [bgTasks, refresh, sessionKeyForEffect])

  return { cronTasks, bgTasks, loading, error, refresh, killBgTask }
}

export function isRunningBgTask(task: Pick<BgTask, 'status'>): boolean {
  return task.status === 'running' || task.status === 'started' || task.status === 'pending'
}

function normalizeBgTask(raw: unknown): BgTask {
  const r = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  return {
    id: stringField(r.id),
    command: stringField(r.command),
    status: stringField(r.status),
    startedAt: stringField(r.startedAt ?? r.started_at),
    finishedAt: optionalString(r.finishedAt ?? r.finished_at),
    exitCode: numberField(r.exitCode ?? r.exit_code),
    error: optionalString(r.error),
    output: optionalString(r.output),
  }
}

function stringField(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function optionalString(v: unknown): string | undefined {
  return typeof v === 'string' && v ? v : undefined
}

function numberField(v: unknown): number {
  return typeof v === 'number' ? v : 0
}
