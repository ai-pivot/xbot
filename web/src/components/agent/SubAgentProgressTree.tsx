import { memo } from 'react'
import { Bot } from 'lucide-react'

import { cn } from '@/lib/utils'
import type { WebSubAgentProgress } from '@/types/shared'

interface SubAgentProgressTreeProps {
  nodes: WebSubAgentProgress[]
}

export const SubAgentProgressTree = memo(function SubAgentProgressTree({
  nodes,
}: SubAgentProgressTreeProps) {
  if (nodes.length === 0) return null
  return (
    <div className="rounded-md border border-border bg-bg-secondary/60 px-2 py-1.5">
      <div className="flex flex-col gap-1">
        {nodes.map((node, i) => (
          <SubAgentNode key={`${node.role}:${node.instance ?? ''}:${i}`} node={node} depth={0} />
        ))}
      </div>
    </div>
  )
})

function SubAgentNode({ node, depth }: { node: WebSubAgentProgress; depth: number }) {
  const running = node.status === 'running' || node.status === 'active' || node.status === 'pending'
  const done = node.status === 'done' || node.status === 'completed'
  const errored = node.status === 'error' || node.status === 'failed'
  return (
    <div className="flex flex-col gap-1">
      <div
        className="flex min-w-0 items-center gap-1.5 text-xs"
        style={{ paddingLeft: `${depth * 14}px` }}
      >
        <Bot
          className={cn('size-3.5 shrink-0', running && 'animate-pulse')}
          style={{
            color: errored
              ? 'var(--status-error)'
              : done
                ? 'var(--status-idle)'
                : 'var(--status-running)',
          }}
        />
        <span className="shrink-0 font-medium text-text-secondary">
          {node.role}{node.instance ? `/${node.instance}` : ''}
        </span>
        {node.desc && (
          <span className="min-w-0 truncate text-text-muted" title={node.desc}>
            {node.desc}
          </span>
        )}
      </div>
      {(node.children ?? []).map((child, i) => (
        <SubAgentNode key={`${child.role}:${child.instance ?? ''}:${i}`} node={child} depth={depth + 1} />
      ))}
    </div>
  )
}
