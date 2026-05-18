import { useState, memo } from 'react'
import { SubAgentTree, type WsSubAgent } from './ProgressPanel'

export interface SubAgentPanelProps {
  agents: WsSubAgent[]
}

/**
 * Persistent SubAgentPanel — displayed above the message list.
 * Shows active sub-agent tree across turns, cleared only on new user message.
 * Reuses SubAgentTree from ProgressPanel for the tree rendering.
 */
export const SubAgentPanel = memo(function SubAgentPanel({ agents }: SubAgentPanelProps) {
  const [collapsed, setCollapsed] = useState(false)

  if (!agents || agents.length === 0) return null

  // Count agents by status
  const running = countByStatus(agents, 'running')
  const total = countAll(agents)

  return (
    <div className="mx-4 my-1 rounded-lg border bg-slate-800/60 border-slate-700/40 overflow-hidden">
      <button
        className="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-slate-700/20 transition-colors"
        onClick={() => setCollapsed(!collapsed)}
        aria-expanded={!collapsed}
        aria-label={`Agents ${running}/${total}`}
      >
        <span className="text-xs flex-shrink-0">🤖</span>
        <span className="text-xs font-semibold text-slate-300 flex-shrink-0">Agents</span>
        <span className="text-[11px] text-slate-500 font-mono flex-shrink-0">{running}/{total}</span>
        {running > 0 && (
          <span className="subagent-pulse text-[10px] text-blue-400 flex-shrink-0">active</span>
        )}
        <span className="flex-1" />
        <span className={`text-slate-500 text-[10px] transition-transform duration-200 flex-shrink-0 ${collapsed ? '' : 'rotate-90'}`}>
          ▸
        </span>
      </button>
      {!collapsed && (
        <div className="px-2 pb-2 border-t border-slate-700/20 pt-1 max-h-60 overflow-y-auto">
          <SubAgentTree agents={agents} />
        </div>
      )}
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
