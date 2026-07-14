/**
 * SubAgentProgressTree — inline highlight cards for SubAgent progress (Spec A §1).
 *
 * Replaces the old static tree list with animated inline cards:
 *   - 2px accent left bar (running=accent, done=accent/30%, error=destructive)
 *   - Status icons (Asterisk+pulse, Check, AlertCircle)
 *   - Light-sweep CSS animation while running
 *   - Framer Motion AnimatePresence for node mount/unmount
 *   - Children indented with dashed connection lines
 *   - done/error nodes collapse to single-line summary after 1s delay
 */
import { memo, useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { AlertCircle, Sparkles, Check } from 'lucide-react'

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
    <div className="flex flex-col gap-1.5">
      <AnimatePresence initial={true}>
        {nodes.map((node, i) => (
          <SubAgentCard
            key={`${node.role}:${node.instance ?? ''}:${i}`}
            node={node}
          />
        ))}
      </AnimatePresence>
    </div>
  )
})

function SubAgentCard({
  node,
}: {
  node: WebSubAgentProgress
}) {
  const running = node.status === 'running' || node.status === 'active' || node.status === 'pending'
  const done = node.status === 'done' || node.status === 'completed'
  const errored = node.status === 'error' || node.status === 'failed'

  // Collapse done/error nodes to single-line summary after 1s delay
  const [collapsed, setCollapsed] = useState(false)
  useEffect(() => {
    if ((done || errored) && !collapsed) {
      const timer = setTimeout(() => setCollapsed(true), 1000)
      return () => clearTimeout(timer)
    }
  }, [done, errored, collapsed])

  const hasChildren = (node.children?.length ?? 0) > 0

  const barColor = errored
    ? 'var(--destructive)'
    : done
      ? 'color-mix(in srgb, var(--accent) 30%, transparent)'
      : 'var(--accent)'

  return (
    <motion.div
      initial={{ opacity: 0, height: 0 }}
      animate={{ opacity: 1, height: 'auto' }}
      exit={{ opacity: 0, height: 0 }}
      transition={{ duration: 0.2 }}
      className={cn(
        'relative overflow-hidden rounded-lg border border-border/50 bg-bg-secondary/50',
        running && 'subagent-card--running',
      )}
      style={{ paddingLeft: '2px' }}
    >
      {/* Left accent bar */}
      <div
        className="absolute inset-y-0 left-0 w-[2px]"
        style={{ backgroundColor: barColor }}
      />

      <div className="py-1.5 pl-2.5 pr-2">
        {/* Node header row */}
        <div className="flex min-w-0 items-center gap-1.5">
          {running ? (
            <Sparkles className="size-3.5 shrink-0 animate-pulse" style={{ color: 'var(--accent)' }} />
          ) : done ? (
            <Check className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
          ) : errored ? (
            <AlertCircle className="size-3.5 shrink-0" style={{ color: 'var(--destructive)' }} />
          ) : (
            <Sparkles className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
          )}
          <span className="shrink-0 text-xs font-medium text-text-secondary">
            {node.role}{node.instance ? `:${node.instance}` : ''}
          </span>
          {node.desc && (
            <span className="min-w-0 truncate text-xs text-text-muted" title={node.desc}>
              {node.desc}
            </span>
          )}
        </div>

        {/* Children — hidden when collapsed */}
        {!collapsed && hasChildren && (
          <div
            className="mt-1 ml-3 flex flex-col gap-1 border-l border-dashed"
            style={{ borderColor: 'var(--border)', paddingLeft: '12px' }}
          >
            <AnimatePresence initial={true}>
              {(node.children ?? []).map((child, i) => (
                <SubAgentChild
                  key={`${child.role}:${child.instance ?? ''}:${i}`}
                  node={child}
                />
              ))}
            </AnimatePresence>
          </div>
        )}
      </div>
    </motion.div>
  )
}

/** Child node — rendered inside the parent card with dashed connection line. */
function SubAgentChild({ node }: { node: WebSubAgentProgress }) {
  const running = node.status === 'running' || node.status === 'active' || node.status === 'pending'
  const done = node.status === 'done' || node.status === 'completed'
  const errored = node.status === 'error' || node.status === 'failed'
  const hasChildren = (node.children?.length ?? 0) > 0

  return (
    <motion.div
      initial={{ opacity: 0, height: 0 }}
      animate={{ opacity: 1, height: 'auto' }}
      exit={{ opacity: 0, height: 0 }}
      transition={{ duration: 0.2 }}
      className="relative"
    >
      <div className="flex min-w-0 items-center gap-1.5">
        {running ? (
          <Sparkles className="size-3 shrink-0 animate-pulse" style={{ color: 'var(--accent)' }} />
        ) : done ? (
          <Check className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
        ) : errored ? (
          <AlertCircle className="size-3 shrink-0" style={{ color: 'var(--destructive)' }} />
        ) : (
          <Sparkles className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
        )}
        <span className="shrink-0 text-xs font-medium text-text-secondary">
          {node.role}{node.instance ? `:${node.instance}` : ''}
        </span>
        {node.desc && (
          <span className="min-w-0 truncate text-xs text-text-muted" title={node.desc}>
            {node.desc}
          </span>
        )}
      </div>
      {hasChildren && (
        <div
          className="mt-0.5 ml-3 flex flex-col gap-1 border-l border-dashed"
          style={{ borderColor: 'var(--border)', paddingLeft: '12px' }}
        >
          {(node.children ?? []).map((child, i) => (
            <SubAgentChild
              key={`${child.role}:${child.instance ?? ''}:${i}`}
              node={child}
            />
          ))}
        </div>
      )}
    </motion.div>
  )
}
