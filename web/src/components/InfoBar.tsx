import { memo } from 'react'
import { useTranslation } from '../i18n'
import type { WsSubAgent } from './ProgressPanel'
import type { TodoItem } from './TodoBar'

export interface InfoBarProps {
  todos: TodoItem[]
  subAgents: WsSubAgent[]
  messageQueue: number
  loading: boolean
  workspace?: string
}

/**
 * InfoBar — status bar displayed below the input area.
 * Mirrors TUI's renderInfoBar: shows workspace indicator, background task count,
 * active agent count, and queued message count.
 */
export const InfoBar = memo(function InfoBar({
  todos,
  subAgents,
  messageQueue,
  loading,
  workspace,
}: InfoBarProps) {
  const { t } = useTranslation()

  const runningAgents = countByStatus(subAgents, 'running')
  const totalAgents = countAll(subAgents)
  const todoDone = todos.filter(t => t.done).length
  const todoTotal = todos.length

  // Don't render if there's nothing to show
  const hasContent = loading || totalAgents > 0 || messageQueue > 0 || todoTotal > 0 || !!workspace
  if (!hasContent) return null

  return (
    <div className="info-bar" role="status" aria-label={t('infoBarLabel')}>
      <div className="info-bar-content">
        {workspace && (
          <span className="info-bar-item" title={t('workspace')}>
            🏠 {workspace}
          </span>
        )}
        {loading && (
          <span className="info-bar-item info-bar-loading" title={t('processing')}>
            <span className="info-bar-spinner" /> {t('processing')}
          </span>
        )}
        {todoTotal > 0 && (
          <span className="info-bar-item" title={t('todoProgress')}>
            📋 {todoDone}/{todoTotal}
          </span>
        )}
        {totalAgents > 0 && (
          <span className="info-bar-item" title={t('activeAgents')}>
            🧠 {runningAgents}/{totalAgents}
          </span>
        )}
        {messageQueue > 0 && (
          <span className="info-bar-item" title={t('queuedMessages')}>
            📬 {messageQueue}
          </span>
        )}
      </div>
      <div className="info-bar-shortcuts">
        <kbd className="info-bar-kbd">Ctrl+T</kbd>
        <span className="info-bar-kbd-label">{t('tasks')}</span>
        <kbd className="info-bar-kbd">Ctrl+K</kbd>
        <span className="info-bar-kbd-label">{t('commandPalette')}</span>
      </div>
    </div>
  )
})

function countByStatus(agents: WsSubAgent[], status: string): number {
  let count = 0
  for (const a of agents) {
    if (a.status === status) count++
    if (a.children) count += countByStatus(a.children, status)
  }
  return count
}

function countAll(agents: WsSubAgent[]): number {
  let count = 0
  for (const a of agents) {
    count++
    if (a.children) count += countAll(a.children)
  }
  return count
}
