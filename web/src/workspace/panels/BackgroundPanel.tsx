import { useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, Square } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { fetchBackgroundTasks } from '@/components/agent/api'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'
import type { BgTask } from '@/hooks/useTasks'

const REFRESH_MS = 2_000

export function BackgroundPanel({ params }: PanelProps) {
  const { ws, theme: themeCtx } = useDockviewContext()
  const { theme } = themeCtx
  const [task, setTask] = useState<BgTask | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const lastLenRef = useRef(0)
  const followRef = useRef(true)
  const taskID = params.taskID ?? ''
  const channel = params.taskChannel || 'web'
  const chatID = params.taskChatID || ''

  const output = task?.output || ''
  const running = task?.status === 'running' || task?.status === 'started'
  const title = useMemo(() => params.command || task?.command || taskID || 'Background task', [params.command, task?.command, taskID])

  // Create xterm instance once.
  useEffect(() => {
    if (!containerRef.current) return
    const term = new Terminal({
      fontSize: 13,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      cursorBlink: false,
      scrollback: 10000,
      convertEol: false,
      theme: theme === 'dark' ? {
        background: '#1e1e2e',
        foreground: '#cdd6f4',
      } : {
        background: '#ffffff',
        foreground: '#1e1e2e',
      },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current)
    fitAddon.fit()
    termRef.current = term

    const resizeObserver = new ResizeObserver(() => {
      try { fitAddon.fit() } catch { /* ignore */ }
    })
    resizeObserver.observe(containerRef.current)

    return () => {
      resizeObserver.disconnect()
      term.dispose()
      termRef.current = null
      lastLenRef.current = 0
    }
  }, [theme])

  // Write output delta to xterm (handles \r, ANSI, cursor sequences correctly).
  useEffect(() => {
    const term = termRef.current
    if (!term || !output) return
    if (output.length > lastLenRef.current) {
      term.write(output.slice(lastLenRef.current))
      lastLenRef.current = output.length
      if (followRef.current) {
        term.scrollToBottom()
      }
    }
  }, [output])

  // Track scroll position.
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    const disp = term.onScroll(() => {
      followRef.current = term.buffer.active.baseY + term.buffer.active.cursorY >= term.buffer.active.length - 2
    })
    return () => disp.dispose()
  }, [])

  // Poll task status.
  useEffect(() => {
    let cancelled = false
    const load = async () => {
      if (!taskID || !chatID) {
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

  const kill = async () => {
    if (!taskID) return
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
        <div ref={containerRef} className="min-h-0 flex-1 overflow-hidden bg-black/95" />
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
