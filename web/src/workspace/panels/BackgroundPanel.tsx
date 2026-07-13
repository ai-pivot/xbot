import { useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, Square } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { fetchBackgroundTasks } from '@/components/agent/api'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'
import type { BgTask } from '@/hooks/useTasks'

const REFRESH_MS = 2_000

export function BackgroundPanel({ params }: PanelProps) {
  const { ws } = useDockviewContext()
  const [task, setTask] = useState<BgTask | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const outputRef = useRef<HTMLPreElement>(null)
  const followRef = useRef(true)
  const taskID = params.taskID ?? ''
  const channel = params.taskChannel || 'web'
  const chatID = params.taskChatID || ''

  const output = task?.output || '(no output yet)'
  const running = task?.status === 'running' || task?.status === 'started'
  const title = useMemo(() => params.command || task?.command || taskID || 'Background task', [params.command, task?.command, taskID])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      if (!ws.connected || !taskID || !chatID) {
        if (!cancelled) setLoading(false)
        return
      }
      try {
        const tasks = await fetchBackgroundTasks<unknown>({ channel, chatID })
        if (cancelled) return
        const found = (tasks ?? []).map(normalizeBgTask).find((item) => item.id === taskID) ?? null
        setTask(found)
        setError(null)
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : 'fetch failed')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    const timer = window.setInterval(() => void load(), REFRESH_MS)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [channel, chatID, taskID, ws])

  useEffect(() => {
    const el = outputRef.current
    if (!el || !followRef.current) return
    requestAnimationFrame(() => {
      el.scrollTop = el.scrollHeight
    })
  }, [output])

  const kill = async () => {
    if (!taskID || !ws.connected) return
    try {
      await ws.rpc('kill_bg_task', { task_id: taskID })
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'failed to kill task')
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg-primary">
      <header className="flex min-h-10 items-center gap-2 border-b border-border px-3">
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium text-text-primary">{title}</div>
          <div className="truncate text-xs text-text-muted">
            {task ? task.status : loading ? 'loading' : 'not found'}
          </div>
        </div>
        {running && (
          <Button type="button" variant="ghost" size="icon-sm" aria-label="kill background task" onClick={() => void kill()}>
            <Square className="size-4" />
          </Button>
        )}
      </header>
      {error ? (
        <div className="flex flex-1 items-center justify-center px-6 text-sm text-status-error">{error}</div>
      ) : loading && !task ? (
        <div className="flex flex-1 items-center justify-center gap-2 text-sm text-text-muted">
          <Loader2 className="size-4 animate-spin" />
          Loading...
        </div>
      ) : (
        <pre
          ref={outputRef}
          onScroll={() => {
            const el = outputRef.current
            if (!el) return
            followRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
          }}
          className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap p-3 font-mono text-xs leading-5 text-text-secondary"
        >
          {output}
        </pre>
      )}
    </div>
  )
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
