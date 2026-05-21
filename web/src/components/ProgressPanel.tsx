import { useTranslation } from '../i18n'
import { useState, memo } from 'react'
import { formatElapsed, computeDisplayIterations } from '../utils'

interface WsToolProgress {
  name: string
  label: string
  status: string
  elapsed_ms: number
  summary?: string
}

export interface WsSubAgent {
  role: string
  status: 'running' | 'done' | 'error' | 'pending'
  desc?: string
  children?: WsSubAgent[]
}

interface WsProgressPayload {
  phase: string
  iteration: number
  active_tools: WsToolProgress[]
  completed_tools: WsToolProgress[]
  thinking: string
  sub_agents?: WsSubAgent[]
  token_usage?: {
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
    cache_hit_tokens: number
  }
  todos?: { id: number; text: string; done: boolean }[]
}

export interface IterationSnapshot {
  iteration: number
  thinking?: string
  reasoning?: string
  tools: IterationToolSnapshot[]
}

export interface IterationToolSnapshot {
  name: string
  label?: string
  status: string
  elapsed_ms?: number
  summary?: string
}

interface ProgressPanelProps {
  progress: WsProgressPayload | null
  liveIterations?: IterationSnapshot[]
  loading: boolean
}


// --- SubAgent Tree Component ---

function SubAgentIcon({ status }: { status: string }) {
  switch (status) {
    case 'done': return <span>✅</span>
    case 'error': return <span>❌</span>
    case 'pending': return <span className="subagent-pulse">⏳</span>
    default: return <span className="subagent-spin">🔄</span>
  }
}

function SubAgentNode({ node, depth = 0 }: { node: WsSubAgent; depth?: number }) {
  const [expanded, setExpanded] = useState(true)
  const hasChildren = node.children && node.children.length > 0
  const isRunning = node.status === 'running'
  const isPending = node.status === 'pending'

  return (
    <div className="subagent-node" style={{ marginLeft: depth > 0 ? '16px' : 0 }}>
      <div
        className={`flex items-center gap-2 px-2 py-1.5 rounded-lg cursor-pointer transition-colors ${
          isRunning ? 'subagent-node-running' : isPending ? 'subagent-node-pending' : 'subagent-node-idle'
        }`}
        onClick={() => hasChildren && setExpanded(!expanded)}
      >
        <SubAgentIcon status={node.status} />
        <span className="subagent-role">{node.role}</span>
        {node.desc && <span className="subagent-desc">{node.desc}</span>}
        {hasChildren && <span className={`subagent-chevron ${expanded ? 'subagent-chevron-open' : ''}`}>▸</span>}
      </div>
      {expanded && hasChildren && (
        <div className="mt-0.5">
          {node.children!.map((child, i) => <SubAgentNode key={`${child.role}-${i}`} node={child} depth={depth + 1} />)}
        </div>
      )}
    </div>
  )
}

export function SubAgentTree({ agents }: { agents: WsSubAgent[] }) {
  if (!agents || agents.length === 0) return null
  return (
    <div className="subagent-tree">
      {agents.map((agent, i) => <SubAgentNode key={`${agent.role}-${i}`} node={agent} />)}
    </div>
  )
}

function ThinkingOrb() {
  const { t } = useTranslation()
  return (
    <div className="thinking-orb-row">
      <div className="thinking-orb">
        <div className="thinking-orb-ring thinking-orb-ring-1" />
        <div className="thinking-orb-ring thinking-orb-ring-2" />
        <div className="thinking-orb-ring thinking-orb-ring-3" />
        <div className="thinking-orb-core" />
      </div>
      <span className="thinking-label">{t("thinking")}</span>
    </div>
  )
}

export function BouncingDots({ text }: { text?: string }) {
  return (
    <div className="bouncing-dots-row">
      <span className="thinking-dots">
        <span className="thinking-dot" style={{ animationDelay: '0ms' }} />
        <span className="thinking-dot" style={{ animationDelay: '160ms' }} />
        <span className="thinking-dot" style={{ animationDelay: '320ms' }} />
      </span>
      {text && <span className="bouncing-dots-text">{text}</span>}
    </div>
  )
}

export const CompletedIteration = memo(function CompletedIteration({ snap }: { snap: IterationSnapshot }) {
  const hasThinking = !!(snap.thinking || '').trim()
  const hasReasoning = !!(snap.reasoning || '').trim()
  const hasTools = (snap.tools ?? []).length > 0
  const isEmpty = !hasThinking && !hasReasoning && !hasTools
  return (
    <div className="iteration-item">
      <div className="iteration-header">#{snap.iteration}</div>
      {hasReasoning && (
        <div className="iteration-block iteration-reasoning">
          <div className="iteration-block-label">💭 Reasoning</div>
          <div className="iteration-block-text">{snap.reasoning}</div>
        </div>
      )}
      {hasThinking && (
        <div className="iteration-block iteration-thinking">
          <div className="iteration-block-label">💡 Thinking</div>
          <div className="iteration-block-text italic">{snap.thinking}</div>
        </div>
      )}
      {hasTools && (
        <div className="space-y-0.5">
          {(snap.tools ?? []).map((tool, i) => {
            const icon = tool.status === 'error' ? '❌' : '✅'
            return (
              <div key={`${snap.iteration}-${i}`} className="iteration-tool">
                <div className="flex items-center gap-2">
                  <span>{icon}</span>
                  <span className="iteration-tool-name">{tool.label || tool.name}</span>
                  {tool.elapsed_ms != null && tool.elapsed_ms > 0 && <span className="iteration-tool-time">{formatElapsed(tool.elapsed_ms)}</span>}
                </div>
                {tool.summary && <div className="iteration-tool-summary">{tool.summary}</div>}
              </div>
            )
          })}
        </div>
      )}
      {isEmpty && <BouncingDots />}
    </div>
  )
})


function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function TokenUsageBar({ tokenUsage }: { tokenUsage: NonNullable<WsProgressPayload['token_usage']> }) {
  return (
    <div className="token-usage-bar">
      <span className="token-usage-item"><span className="token-label-in">in</span> {formatTokenCount(tokenUsage.prompt_tokens)}</span>
      <span className="token-usage-item"><span className="token-label-out">out</span> {formatTokenCount(tokenUsage.completion_tokens)}</span>
      <span className="token-usage-item"><span className="token-label-total">total</span> <span className="token-value">{formatTokenCount(tokenUsage.total_tokens)}</span></span>
      {tokenUsage.cache_hit_tokens > 0 && (
        <span className="token-usage-item"><span className="token-label-cache">cache</span> {formatTokenCount(tokenUsage.cache_hit_tokens)}</span>
      )}
    </div>
  )
}

function TodoList({ todos }: { todos: NonNullable<WsProgressPayload['todos']> }) {
  const done = todos.filter(t => t.done).length
  const total = todos.length
  const progress = total > 0 ? (done / total) * 100 : 0

  return (
    <div className="todo-section">
      <div className="todo-header">
        <span className="todo-title">📋 TODO {done}/{total}</span>
        <span className="todo-percent">{Math.round(progress)}%</span>
      </div>
      <div className="todo-progress-track">
        <div className="todo-progress-bar" style={{ width: `${progress}%` }} />
      </div>
      <div className="todo-items">
        {todos.map(todo => (
          <div key={todo.id} className={`todo-item ${todo.done ? 'todo-item-done' : ''}`}>
            <span className="todo-check">{todo.done ? '✅' : '⬜'}</span>
            <span className="todo-text">{todo.text}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

export default function ProgressPanel({ progress, liveIterations, loading }: ProgressPanelProps) {
  if (!progress && loading) {
    return (
      <div className="flex justify-start">
        <div className="progress-card progress-card-thinking">
          <ThinkingOrb />
        </div>
      </div>
    )
  }
  if (!progress) return null

  const isActive = progress.phase !== 'done'
  const displayLiveIterations = computeDisplayIterations(liveIterations, progress)

  const activeTools = progress.active_tools?.filter(t => t.status !== 'done' && t.status !== 'error') ?? []
  const hasActiveTools = activeTools.length > 0
  const currentThinking = (progress.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  const hasVisibleContent = shouldShowCurrentThinking
    || hasActiveTools
    || (progress.phase === 'thinking' && !progress.thinking)
    || (progress.phase === 'tool_exec' && (progress.completed_tools?.length ?? 0) > 0)
    || ['compressing', 'retrying'].includes(progress.phase)

  return (
    <div className="flex justify-start progress-fade-in">
      <div className={`progress-card ${isActive ? 'progress-card-active' : 'progress-card-done'}`}>
        <div className="progress-inner">
          {displayLiveIterations.map(snap => <CompletedIteration key={snap.iteration} snap={snap} />)}

          {isActive && (
            <div className="iteration-item">
              <div className="iteration-header">#{progress.iteration}</div>

              {shouldShowCurrentThinking && (
                <div className="iteration-block iteration-reasoning">
                  <div className="iteration-block-label">💭 Reasoning</div>
                  <div className="iteration-block-text">{progress.thinking}</div>
                </div>
              )}

              {progress.phase === 'thinking' && !progress.thinking && <BouncingDots text="thinking…" />}

              {hasActiveTools && activeTools.map((tool, i) => (
                <div key={`${tool.name}-${i}`} className="iteration-tool">
                  <div className="flex items-center gap-2">
                    <span className="tool-pulse">⏳</span>
                    <span className="iteration-tool-name">{tool.label || tool.name}</span>
                    {tool.elapsed_ms > 0 && <span className="iteration-tool-time">{formatElapsed(tool.elapsed_ms)}</span>}
                  </div>
                </div>
              ))}

              {!hasActiveTools && progress.phase === 'tool_exec' && (() => {
                const completed = progress.completed_tools ?? []
                const last = completed.length > 0 ? completed[completed.length - 1] : null
                if (!last) return <BouncingDots text="executing…" />
                return (
                  <div className="iteration-tool">
                    <div className="flex items-center gap-2">
                      <span>{last.status === 'done' ? '✅' : '❌'}</span>
                      <span className="iteration-tool-name">{last.label || last.name}</span>
                      {last.elapsed_ms != null && last.elapsed_ms > 0 && <span className="iteration-tool-time">{formatElapsed(last.elapsed_ms)}</span>}
                    </div>
                  </div>
                )
              })()}

              {['compressing', 'retrying'].includes(progress.phase) && (
                <div className="iteration-phase-text">
                  <span>{progress.phase === 'compressing' ? '📦' : '🔄'}</span>
                  <span>{progress.phase}…</span>
                </div>
              )}

              {!hasVisibleContent && <BouncingDots />}
            </div>
          )}

          {progress.sub_agents && progress.sub_agents.length > 0 && (
            <div className="progress-sub-section">
              <SubAgentTree agents={progress.sub_agents} />
            </div>
          )}
          {progress.token_usage && <TokenUsageBar tokenUsage={progress.token_usage} />}
          {progress.todos && progress.todos.length > 0 && <TodoList todos={progress.todos} />}
        </div>
      </div>
    </div>
  )
}

export type { WsProgressPayload, WsToolProgress }

