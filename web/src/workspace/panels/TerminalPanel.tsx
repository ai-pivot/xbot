/**
 * TerminalPanel — xterm.js terminal panel backed by a real PTY via WebSocket.
 *
 * Mounts a Terminal instance on a container div, connects to the backend PTY
 * via TerminalWS, and handles resize/close/exit lifecycle.
 *
 * On mobile, an accessory bar above the soft keyboard provides arrow keys,
 * Tab, Ctrl, Esc, and other control keys that are hard to type on a touch
 * keyboard.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'

import { TerminalWS } from '@/lib/terminalWS'
import { terminalStore } from '@/hooks/useTerminal'
import { useDockviewContext } from '@/workspace/types'
import { useIsMobile } from '@/hooks/useIsMobile'
import type { PanelProps } from '@/workspace/panels/types'

// ── Mobile accessory bar keys ─────────────────────────────────────────

/** A key button for the mobile accessory bar. */
interface AuxKey {
  label: string
  /** Data to send to the PTY. Empty for toggle keys (Ctrl). */
  data: string
  /** Whether this is a toggle key (stays active until tapped again). */
  toggle?: boolean
  /** Wider than a normal key (e.g. Ctrl toggle). */
  wide?: boolean
}

/** Primary row: frequently used keys. */
const PRIMARY_KEYS: AuxKey[] = [
  { label: 'Esc', data: '\x1b' },
  { label: 'Tab', data: '\t' },
  { label: 'Ctrl', data: '', toggle: true, wide: true },
  { label: '↑', data: '\x1b[A' },
  { label: '↓', data: '\x1b[B' },
  { label: '←', data: '\x1b[D' },
  { label: '→', data: '\x1b[C' },
]

/** Secondary row: less common keys, shown when expanded. */
const SECONDARY_KEYS: AuxKey[] = [
  { label: 'Home', data: '\x1b[H' },
  { label: 'End', data: '\x1b[F' },
  { label: 'PgUp', data: '\x1b[5~' },
  { label: 'PgDn', data: '\x1b[6~' },
  { label: '|', data: '|' },
  { label: '~', data: '~' },
  { label: '/', data: '/' },
  { label: '-', data: '-' },
]

/** Keys shown when Ctrl is active (common shell shortcuts). */
const CTRL_KEYS: AuxKey[] = [
  { label: 'C', data: '\x03' }, // SIGINT
  { label: 'D', data: '\x04' }, // EOF
  { label: 'L', data: '\x0c' }, // clear
  { label: 'A', data: '\x01' }, // line start
  { label: 'E', data: '\x05' }, // line end
  { label: 'W', data: '\x17' }, // delete word
  { label: 'R', data: '\x12' }, // reverse search
  { label: 'Z', data: '\x1a' }, // suspend
]

// ── Component ──────────────────────────────────────────────────────────

export function TerminalPanel({ params }: PanelProps) {
  const { i18n, theme: themeCtx } = useDockviewContext()
  const { t } = i18n
  const { theme } = themeCtx
  const isMobile = useIsMobile()
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<TerminalWS | null>(null)

  // Accessory bar state
  const [showAux, setShowAux] = useState(true)
  const [showExpanded, setShowExpanded] = useState(false)
  const [ctrlActive, setCtrlActive] = useState(false)

  const terminalId = params.terminalId
  const session = terminalId ? terminalStore.getSession(terminalId) : null
  const tid = session?.tid

  // Send data to the PTY via the WebSocket.
  const sendToPty = useCallback((data: string) => {
    if (!data) return
    const ws = wsRef.current
    if (!ws) return
    const bytes = new TextEncoder().encode(data)
    ws.sendStdin(bytes)
  }, [])

  // Handle a tap on an auxiliary key.
  const handleAuxKey = useCallback((key: AuxKey) => {
    if (key.toggle) {
      setCtrlActive((v) => !v)
      return
    }
    if (ctrlActive && /^[a-zA-Z]$/.test(key.label)) {
      // Ctrl + letter → control character
      sendToPty(String.fromCharCode(key.label.toUpperCase().charCodeAt(0) - 64))
    } else {
      sendToPty(key.data)
    }
  }, [ctrlActive, sendToPty])

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
        background: '#1e1e1e',
        foreground: '#cccccc',
        cursor: '#cccccc',
      } : {
        background: '#ffffff',
        foreground: '#1e1e1e',
        cursor: '#1e1e1e',
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

      const sess = terminalStore.getSession(terminalId)
      if (sess?.closing) {
        ws.close()
        terminalStore.remove(terminalId)
      } else {
        ws.disconnect()
        terminalStore.clearTabId(terminalId)
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

  // Determine which key set to show based on Ctrl state.
  const activeKeys = ctrlActive ? CTRL_KEYS : showExpanded ? SECONDARY_KEYS : PRIMARY_KEYS

  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-bg-primary">
      {/* Terminal area */}
      <div ref={containerRef} className="min-h-0 flex-1 overflow-hidden" />

      {/* Mobile accessory bar */}
      {isMobile && showAux && (
        <div className="shrink-0 border-t border-border bg-bg-secondary">
          {/* Key row */}
          <div className="flex items-center gap-1 px-1 py-1.5 overflow-x-auto">
            {/* Toggle button for the accessory bar itself */}
            <button
              type="button"
              onClick={() => setShowAux(false)}
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded text-text-muted hover:bg-bg-tertiary"
              aria-label="Hide keyboard"
            >
              <span className="text-xs">▾</span>
            </button>

            {activeKeys.map((key) => (
              <button
                key={key.label}
                type="button"
                onTouchStart={(e) => {
                  e.preventDefault()
                  handleAuxKey(key)
                }}
                onMouseDown={(e) => {
                  e.preventDefault()
                  handleAuxKey(key)
                }}
                className={
                  'flex h-7 shrink-0 items-center justify-center rounded text-xs font-medium transition-colors ' +
                  (key.toggle && ctrlActive
                    ? 'bg-accent text-white '
                    : 'bg-bg-tertiary text-text-primary hover:bg-bg-tertiary/80 ') +
                  (key.wide ? 'min-w-12 px-3 ' : 'min-w-7 px-1')
                }
                style={key.toggle && ctrlActive ? { backgroundColor: 'var(--accent)', color: 'white' } : undefined}
              >
                {key.toggle ? (ctrlActive ? 'Ctrl ✓' : key.label) : key.label}
              </button>
            ))}

            {/* Expand/collapse secondary keys (only when not in Ctrl mode) */}
            {!ctrlActive && (
              <button
                type="button"
                onClick={() => setShowExpanded((v) => !v)}
                className="flex h-7 w-7 shrink-0 items-center justify-center rounded text-text-muted hover:bg-bg-tertiary"
                aria-label={showExpanded ? 'Show less' : 'Show more'}
              >
                <span className="text-xs">{showExpanded ? '‹' : '›'}</span>
              </button>
            )}
          </div>
        </div>
      )}

      {/* Show button to re-open the accessory bar when hidden */}
      {isMobile && !showAux && (
        <button
          type="button"
          onClick={() => setShowAux(true)}
          className="flex h-7 shrink-0 items-center justify-center border-t border-border bg-bg-secondary text-text-muted hover:bg-bg-tertiary"
          aria-label="Show keyboard"
        >
          <span className="text-xs">▴ Keys</span>
        </button>
      )}
    </div>
  )
}
