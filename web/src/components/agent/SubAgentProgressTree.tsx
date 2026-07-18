/**
 * SubAgentProgressTree — inline highlight cards for SubAgent progress (Spec A §1).
 *
 * Replaces the old static tree list with animated inline cards:
 *   - 2px accent left bar (running=accent, done=accent/30%, error=destructive)
 *   - Status icons (Sparkles, Check, AlertCircle)
 *   - Light-sweep CSS animation while running
 *   - Framer Motion AnimatePresence for node mount/unmount
 *   - Children indented with dashed connection lines
 *   - done/error nodes collapse to single-line summary after 1s delay
 */
import { memo, useCallback, useContext, useEffect, useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { AlertCircle, Sparkles, Check } from 'lucide-react'

import { cn } from '@/lib/utils'
import { parseAgentChatID } from '@/lib/session-grouping'
import type { WebSubAgentProgress } from '@/types/shared'
import { DockviewContext } from '@/workspace/types'
import { AnimatedCollapse } from '@/components/ui/animated-collapse'
import { SweepText } from './SweepText'

interface SubAgentProgressTreeProps {
  nodes: WebSubAgentProgress[]
}

export const SubAgentProgressTree = memo(function SubAgentProgressTree({
  nodes,
}: SubAgentProgressTreeProps) {
  const dockview = useContext(DockviewContext)
  const openTab = dockview?.openTab
  const openSubAgent = useCallback((node: WebSubAgentProgress) => {
    const sessionKey = node.sessionKey
    if (!sessionKey) return
    const parsed = parseAgentChatID(sessionKey)
    if (!parsed) return
    openTab?.({
      type: 'agent',
      title: parsed.instance ? `${parsed.role}/${parsed.instance}` : parsed.role,
      icon: 'bot',
      closable: true,
      data: {
        subAgentRole: parsed.role,
        subAgentInstance: parsed.instance,
        parentChatID: parsed.parentChatID,
        parentChannel: parsed.parentChannel,
        agentChatID: sessionKey,
      },
    })
  }, [openTab])

  if (nodes.length === 0) return null
  return (
    <div className="flex flex-col gap-1.5">
      <AnimatePresence initial={true}>
        {nodes.map((node, i) => (
          <SubAgentCard
            key={`${node.role}:${node.instance ?? ''}:${i}`}
            node={node}
            onOpen={openTab ? openSubAgent : undefined}
          />
        ))}
      </AnimatePresence>
    </div>
  )
})

function SubAgentCard({
  node,
  onOpen,
}: {
  node: WebSubAgentProgress
  onOpen?: (node: WebSubAgentProgress) => void
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
        <button
          type="button"
          disabled={!node.sessionKey || !onOpen}
          aria-label={node.sessionKey && onOpen ? `Open SubAgent ${node.role}${node.instance ? `/${node.instance}` : ''}` : undefined}
          className="flex w-full min-w-0 items-center gap-1.5 text-left enabled:cursor-pointer disabled:cursor-default"
          onClick={() => onOpen?.(node)}
        >
          {running ? (
            <Sparkles className="size-3.5 shrink-0" style={{ color: 'var(--accent)' }} />
          ) : done ? (
            <Check className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
          ) : errored ? (
            <AlertCircle className="size-3.5 shrink-0" style={{ color: 'var(--destructive)' }} />
          ) : (
            <Sparkles className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
          )}
          {running ? (
            <SweepText
              text={`${node.role}${node.instance ? `:${node.instance}` : ''}`}
              color="var(--accent)"
              className="shrink-0 text-xs font-medium"
            />
          ) : (
            <span className="shrink-0 text-xs font-medium text-text-secondary">
              {node.role}{node.instance ? `:${node.instance}` : ''}
            </span>
          )}
          {node.desc && (
            <span className="min-w-0 truncate text-xs text-text-muted" title={node.desc}>
              {node.desc}
            </span>
          )}
        </button>

        {/* Children — hidden when collapsed */}
        {hasChildren && (
          <AnimatedCollapse open={!collapsed} lazy className="mt-1 ml-3 border-l border-dashed" contentClassName="flex flex-col gap-1 pl-3" >
            <AnimatePresence initial={true}>
              {(node.children ?? []).map((child, i) => (
                <SubAgentChild
                  key={`${child.role}:${child.instance ?? ''}:${i}`}
                  node={child}
                  onOpen={onOpen}
                />
              ))}
            </AnimatePresence>
          </AnimatedCollapse>
        )}
      </div>
    </motion.div>
  )
}

/** Child node — rendered inside the parent card with dashed connection line. */
function SubAgentChild({ node, onOpen }: { node: WebSubAgentProgress; onOpen?: (node: WebSubAgentProgress) => void }) {
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
      <button
        type="button"
        disabled={!node.sessionKey || !onOpen}
        aria-label={node.sessionKey && onOpen ? `Open SubAgent ${node.role}${node.instance ? `/${node.instance}` : ''}` : undefined}
        className="flex w-full min-w-0 items-center gap-1.5 text-left enabled:cursor-pointer disabled:cursor-default"
        onClick={() => onOpen?.(node)}
      >
        {running ? (
          <Sparkles className="size-3 shrink-0" style={{ color: 'var(--accent)' }} />
        ) : done ? (
          <Check className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
        ) : errored ? (
          <AlertCircle className="size-3 shrink-0" style={{ color: 'var(--destructive)' }} />
        ) : (
          <Sparkles className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
        )}
        {running ? (
          <SweepText
            text={`${node.role}${node.instance ? `:${node.instance}` : ''}`}
            color="var(--accent)"
            className="shrink-0 text-xs font-medium"
          />
        ) : (
          <span className="shrink-0 text-xs font-medium text-text-secondary">
            {node.role}{node.instance ? `:${node.instance}` : ''}
          </span>
        )}
        {node.desc && (
          <span className="min-w-0 truncate text-xs text-text-muted" title={node.desc}>
            {node.desc}
          </span>
        )}
      </button>
      {hasChildren && (
        <AnimatedCollapse open className="mt-0.5 ml-3 border-l border-dashed" contentClassName="flex flex-col gap-1 pl-3">
          {(node.children ?? []).map((child, i) => (
            <SubAgentChild
              key={`${child.role}:${child.instance ?? ''}:${i}`}
              node={child}
              onOpen={onOpen}
            />
          ))}
        </AnimatedCollapse>
      )}
    </motion.div>
  )
}
