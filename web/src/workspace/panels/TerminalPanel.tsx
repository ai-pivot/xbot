/**
 * TerminalPanel — xterm.js terminal panel backed by a real PTY via WebSocket.
 *
 * Mounts a Terminal instance on a container div, connects to the backend PTY
 * via TerminalWS, and handles resize/close/exit lifecycle.
 */
import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'

import { TerminalWS } from '@/lib/terminalWS'
import { terminalStore } from '@/hooks/useTerminal'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'

export function TerminalPanel({ params }: PanelProps) {
  const { i18n, theme: themeCtx } = useDockviewContext()
  const { t } = i18n
  const { theme } = themeCtx
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<TerminalWS | null>(null)

  const terminalId = params.terminalId
  const session = terminalId ? terminalStore.getSession(terminalId) : null
  const tid = session?.tid

  useEffect(() => {
    if (!containerRef.current || !tid || !terminalId) return

    // Create xterm instance
    const term = new Terminal({
      fontSize: 13,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      cursorBlink: true,
      scrollback: 10000,
      convertEol: true,
      theme: theme === 'dark' ? {
        background: '#1e1e2e',
        foreground: '#cdd6f4',
        cursor: '#f5e0dc',
      } : {
        background: '#ffffff',
        foreground: '#1e1e2e',
        cursor: '#1e1e2e',
      },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())
    term.open(containerRef.current)
    fitAddon.fit()

    termRef.current = term

    // Connect to backend PTY via WebSocket
    const ws = new TerminalWS(tid, {
      onStdout: (data) => term.write(data),
      onStderr: (data) => term.write(data),
      onExit: (code) => {
        term.write(`\r\n[Process exited with code ${code}]\r\n`)
        terminalStore.updateStatus(terminalId, 'exited', { exitCode: code })
      },
      onError: (message) => {
        term.write(`\r\n[Error: ${message}]\r\n`)
        terminalStore.updateStatus(terminalId, 'error', { error: message })
      },
      onOpen: () => {
        terminalStore.updateStatus(terminalId, 'connected')
        // Send initial size
        fitAddon.fit()
        ws.resize(term.cols, term.rows)
      },
      onClose: () => {
        // Will be called on disconnect; status updated by onExit/onError
      },
    })
    wsRef.current = ws

    // Wire xterm input → WS stdin
    const inputData = term.onData((data) => ws.sendStdin(data))

    // Wire xterm resize → WS resize
    const inputResize = term.onResize(({ cols, rows }) => ws.resize(cols, rows))

    // Watch container size → fitAddon
    const resizeObserver = new ResizeObserver(() => {
      try { fitAddon.fit() } catch { /* ignore */ }
    })
    resizeObserver.observe(containerRef.current)

    // Cleanup on unmount
    return () => {
      inputData.dispose()
      inputResize.dispose()
      resizeObserver.disconnect()

      // If the user explicitly closed this terminal (closing flag set by
      // closeTerminal), send a WS close frame so the backend destroys the PTY
      // and remove the session from the store. Otherwise just disconnect the
      // WS — the terminal persists so a later panel remount can reconnect.
      const sess = terminalStore.getSession(terminalId)
      if (sess?.closing) {
        ws.close()
        terminalStore.remove(terminalId)
      } else {
        ws.disconnect()
      }

      wsRef.current = null
      term.dispose()
      termRef.current = null
    }
  }, [tid, terminalId, theme])

  if (!tid || !terminalId) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-text-secondary">
        <p className="text-sm">{t('workspace.terminalNotAvailable')}</p>
      </div>
    )
  }

  return <div ref={containerRef} className="h-full w-full overflow-hidden bg-black" />
}
