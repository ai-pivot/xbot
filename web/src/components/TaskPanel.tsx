import { memo, useState } from 'react'
import { useTranslation } from '../i18n'
import { SubAgentTree, type WsSubAgent } from './ProgressPanel'
import type { TodoItem } from './TodoBar'

export interface TaskPanelProps {
  open: boolean
  onClose: () => void
  todos: TodoItem[]
  subAgents: WsSubAgent[]
  loading: boolean
}

/**
 * TaskPanel — slide-out panel showing all active tasks, agents, and todos.
 * Mirrors TUI's background tasks panel (triggered by `^` key or Ctrl+T).
 * Consolidates TodoBar, SubAgentPanel, and active progress into one view.
 */
export const TaskPanel = memo(function TaskPanel({
  open,
  onClose,
  todos,
  subAgents,
  loading,
}: TaskPanelProps) {
  const t = useTranslation()
  const [activeTab, setActiveTab] = useState<'overview' | 'todos' | 'agents'>('overview')

  if (!open) return null

  const todoDone = todos.filter(t => t.done).length
  const todoTotal = todos.length
  const runningAgents = countByStatus(subAgents, 'running')
  const totalAgents = countAll(subAgents)

  return (
    <div className="task-panel-overlay" onClick={onClose} role="dialog" aria-label={t('taskPanelTitle')}>
      <div className="task-panel" onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div className="task-panel-header">
          <h2 className="task-panel-title">
            ⚡ {t('taskPanelTitle')}
          </h2>
          <button className="task-panel-close" onClick={onClose} aria-label={t('close')}>
            ✕
          </button>
        </div>

        {/* Tab bar */}
        <div className="task-panel-tabs">
          <button
            className={`task-panel-tab ${activeTab === 'overview' ? 'active' : ''}`}
            onClick={() => setActiveTab('overview')}
          >
            {t('taskTabOverview')}
          </button>
          <button
            className={`task-panel-tab ${activeTab === 'todos' ? 'active' : ''}`}
            onClick={() => setActiveTab('todos')}
          >
            📋 {t('taskTabTodos')} {todoTotal > 0 && <span className="task-panel-badge">{todoDone}/{todoTotal}</span>}
          </button>
          <button
            className={`task-panel-tab ${activeTab === 'agents' ? 'active' : ''}`}
            onClick={() => setActiveTab('agents')}
          >
            🤖 {t('taskTabAgents')} {totalAgents > 0 && <span className="task-panel-badge">{runningAgents}/{totalAgents}</span>}
          </button>
        </div>

        {/* Content */}
        <div className="task-panel-content">
          {activeTab === 'overview' && (
            <div className="task-panel-overview">
              {/* Status card */}
              <div className="task-status-card">
                <div className="task-status-indicator">
                  {loading ? (
                    <>
                      <span className="task-status-dot task-status-running" />
                      <span className="task-status-text">{t('taskStatusRunning')}</span>
                    </>
                  ) : (
                    <>
                      <span className="task-status-dot task-status-idle" />
                      <span className="task-status-text">{t('taskStatusIdle')}</span>
                    </>
                  )}
                </div>
              </div>

              {/* Todo summary */}
              {todoTotal > 0 && (
                <div className="task-section">
                  <h3 className="task-section-title">📋 {t('todoTitle')}</h3>
                  <div className="task-todo-progress">
                    <div className="task-todo-bar">
                      <div
                        className={`task-todo-fill ${todoDone === todoTotal ? 'task-todo-done' : ''}`}
                        style={{ width: `${(todoDone / todoTotal) * 100}%` }}
                      />
                    </div>
                    <span className="task-todo-count">{todoDone}/{todoTotal}</span>
                  </div>
                  <div className="task-todo-list">
                    {todos.map(todo => (
                      <div key={todo.id} className={`task-todo-item ${todo.done ? 'done' : ''}`}>
                        <span className="task-todo-check">{todo.done ? '✓' : '○'}</span>
                        <span className="task-todo-text">{todo.text}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Agent summary */}
              {totalAgents > 0 && (
                <div className="task-section">
                  <h3 className="task-section-title">🤖 {t('agentsTitle')}</h3>
                  <div className="task-agent-tree">
                    <SubAgentTree agents={subAgents} />
                  </div>
                </div>
              )}

              {todoTotal === 0 && totalAgents === 0 && !loading && (
                <div className="task-panel-empty">
                  <span className="text-2xl opacity-30">✅</span>
                  <p className="text-sm text-slate-500 mt-2">{t('taskPanelEmpty')}</p>
                </div>
              )}
            </div>
          )}

          {activeTab === 'todos' && (
            <div className="task-panel-todos">
              {todoTotal > 0 ? (
                <div className="task-full-todo-list">
                  <div className="task-todo-progress mb-4">
                    <div className="task-todo-bar task-todo-bar-large">
                      <div
                        className={`task-todo-fill ${todoDone === todoTotal ? 'task-todo-done' : ''}`}
                        style={{ width: `${(todoDone / todoTotal) * 100}%` }}
                      />
                    </div>
                    <span className="task-todo-count text-base">{todoDone}/{todoTotal} ({Math.round(todoDone / todoTotal * 100)}%)</span>
                  </div>
                  {todos.map(todo => (
                    <div key={todo.id} className={`task-todo-item ${todo.done ? 'done' : ''}`}>
                      <span className={`task-todo-check ${todo.done ? 'text-green-400' : 'text-slate-500'}`}>
                        {todo.done ? '✓' : '○'}
                      </span>
                      <span className={`task-todo-text ${todo.done ? 'line-through text-slate-500' : 'text-slate-300'}`}>
                        {todo.text}
                      </span>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="task-panel-empty">
                  <span className="text-2xl opacity-30">📋</span>
                  <p className="text-sm text-slate-500 mt-2">{t('noTodos')}</p>
                </div>
              )}
            </div>
          )}

          {activeTab === 'agents' && (
            <div className="task-panel-agents">
              {totalAgents > 0 ? (
                <div className="task-full-agent-tree">
                  <div className="task-agent-status-bar">
                    <span>{t('activeAgents')}:</span>
                    <span className="ml-2 text-blue-400">{runningAgents}</span>
                    <span className="mx-1 text-slate-600">/</span>
                    <span>{totalAgents}</span>
                  </div>
                  <SubAgentTree agents={subAgents} />
                </div>
              ) : (
                <div className="task-panel-empty">
                  <span className="text-2xl opacity-30">🤖</span>
                  <p className="text-sm text-slate-500 mt-2">{t('noAgents')}</p>
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer with shortcuts */}
        <div className="task-panel-footer">
          <span className="text-[10px] text-slate-600">
            <kbd className="info-bar-kbd">Esc</kbd> {t('close')}
          </span>
        </div>
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
