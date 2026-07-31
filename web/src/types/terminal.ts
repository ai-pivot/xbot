/**
 * Terminal types — frontend terminal session model (xterm.js spec).
 *
 * A `TerminalSession` is the store-level record for one backend PTY. The
 * backend assigns a `tid` on `POST /api/terminal/create`; the frontend keeps a
 * local `id` (used as the Dockview tab identity) plus metadata for the sidebar
 * list. The live WS data channel and xterm instance are owned by the panel that
 * renders this terminal (see TerminalPanel), not by the store — the store only
 * tracks the list and the create/close lifecycle.
 *
 * Backend contracts (channel/web/web_pty.go):
 *   POST   /api/terminal/create   { chatID, cwd } → { tid }
 *   DELETE /api/terminal/{tid}     → { ok: true }
 *   WS     /ws/terminal?tid=<tid> — bidirectional data channel (cookie auth)
 */

/** Lifecycle of a terminal's data channel (mirrors TerminalWS state). */
export type TerminalStatus =
  | 'connecting' // WS opening / reconnecting
  | 'connected' // WS open, bidirectional data flowing
  | 'exited' // PTY process exited (terminal done; no more output)
  | 'error' // unrecoverable error (terminal not found, etc.)
  | 'closed' // explicitly closed by the user

/** One terminal session tracked by the store. */
export interface TerminalSession {
  /** Frontend id (also the Dockview tab identity). Stable for the terminal's life. */
  id: string
  /** Backend terminal id from `POST /api/terminal/create`. */
  tid: string
  /** Owning chat session (for session-scoped lifecycle / cleanup). */
  chatID: string
  /** Working directory the PTY started in (may be empty = server home). */
  cwd: string
  /** Tab title shown in the Dockview tab header + sidebar list. */
  title: string
  /** Current data-channel status, kept in sync by the panel. */
  status: TerminalStatus
  /** Exit code reported by the PTY on exit, if any. */
  exitCode?: number
  /** Error message when status === 'error'. */
  error?: string
  /** Dockview tab id once the tab is opened (for focus/close wiring). */
  tabId?: string
  /** True when the user explicitly closed this terminal (panel cleanup sends WS close). */
  closing?: boolean
  /** Creation timestamp (ms). */
  createdAt: number
}

/* ── WebSocket protocol messages (mirrors channel/web/web_pty.go) ── */

/** Server → Client. */
export type TerminalServerMessage =
  | { type: 'stdout'; data: string } // base64-encoded PTY bytes
  | { type: 'stderr'; data: string } // base64-encoded PTY stderr (if split)
  | { type: 'exit'; code: number }
  | { type: 'error'; message: string }

/** Client → Server. */
export type TerminalClientMessage =
  | { type: 'stdin'; data: string } // base64-encoded input bytes
  | { type: 'resize'; cols: number; rows: number }
  | { type: 'close' }
