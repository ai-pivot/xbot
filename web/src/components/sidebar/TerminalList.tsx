/**
 * TerminalList — right-sidebar panel that lists and manages terminals.
 *
 * VSCode-style terminal management surface:
 *   - a header "+" creates a new terminal (resolves the current session's CWD,
 *     opens a Dockview tab);
 *   - each row shows the terminal title + a live status dot; clicking a row
 *     focuses its tab; the per-row trash button closes it (which unmounts the
 *     panel and tears down the backend PTY — see TerminalPanel).
 *
 * Pure presentational over the `useTerminal` store; the list re-renders on
 * store notifications (create/close/status changes).
 */
import { Plus, SquareTerminal, Trash2, Loader2, Circle } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { TerminalManager } from '@/hooks/useTerminal'
import type { TerminalSession, TerminalStatus } from '@/types/terminal'

interface TerminalListProps {
  terminalManager: TerminalManager
}

const STATUS_COLOR: Record<TerminalStatus, string> = {
  connecting: '#e0a800',
  connected: '#3fb950',
  exited: '#8b949e',
  error: '#f85149',
  closed: '#8b949e',
}

export function TerminalList({ terminalManager }: TerminalListProps) {
  const { t } = useI18n()
  const { terminals, createTerminal, closeTerminal, focusTerminal } = terminalManager

  return (
    <div className="flex h-full flex-col">
      {/* New terminal */}
      <div className="flex shrink-0 items-center justify-between px-3 py-2">
        <span className="text-xs text-text-secondary">{t('sidebar.terminalCount', { count: terminals.length })}</span>
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t('sidebar.terminalNew')}
              onClick={() => void createTerminal()}
              className="flex size-6 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-bg-tertiary hover:text-text-primary"
            >
              <Plus className="size-4" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{t('sidebar.terminalNew')}</TooltipContent>
        </Tooltip>
      </div>

      {/* List */}
      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {terminals.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 px-4 text-center text-text-secondary">
            <SquareTerminal className="size-7 opacity-40" />
            <p className="text-xs">{t('sidebar.terminalEmpty')}</p>
          </div>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {terminals.map((term) => (
              <TerminalRow
                key={term.id}
                term={term}
                onFocus={() => focusTerminal(term.id)}
                onClose={() => closeTerminal(term.id)}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

interface TerminalRowProps {
  term: TerminalSession
  onFocus: () => void
  onClose: () => void
}

function TerminalRow({ term, onFocus, onClose }: TerminalRowProps) {
  const { t } = useI18n()
  const statusLabel = statusText(term.status, t)
  const color = STATUS_COLOR[term.status]

  return (
    <li>
      <div
        role="button"
        tabIndex={0}
        onClick={onFocus}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onFocus()
          }
        }}
        className="group flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 transition-colors hover:bg-bg-tertiary"
      >
        {/* status indicator */}
        {term.status === 'connecting' ? (
          <Loader2 className="size-3.5 shrink-0 animate-spin" style={{ color }} />
        ) : (
          <Circle
            className="size-3 shrink-0 fill-current"
            style={{ color }}
            aria-hidden
          />
        )}

        <div className="min-w-0 flex-1">
          <div className="truncate text-xs font-medium text-text-primary">{term.title}</div>
          <div className="truncate text-[11px] text-text-secondary">{statusLabel}</div>
        </div>

        {/* close button (kill terminal) */}
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t('sidebar.terminalKill')}
              onClick={(e) => {
                e.stopPropagation()
                onClose()
              }}
              className="flex size-5 shrink-0 items-center justify-center rounded-sm text-text-secondary opacity-0 transition-opacity hover:bg-bg-secondary hover:text-error group-hover:opacity-100 focus-visible:opacity-100"
            >
              <Trash2 className="size-3.5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="left">{t('sidebar.terminalKill')}</TooltipContent>
        </Tooltip>
      </div>
    </li>
  )
}

function statusText(status: TerminalStatus, t: (k: string) => string): string {
  switch (status) {
    case 'connecting':
      return t('sidebar.terminalStatus.connecting')
    case 'connected':
      return t('sidebar.terminalStatus.connected')
    case 'exited':
      return t('sidebar.terminalStatus.exited')
    case 'error':
      return t('sidebar.terminalStatus.error')
    case 'closed':
      return t('sidebar.terminalStatus.closed')
  }
}
