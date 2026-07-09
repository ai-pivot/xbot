/**
 * CollapsibleCard — a compact collapsible container shared by the Agent
 * intermediate-process blocks (Spec 4 §3.3).
 *
 * Header row: [chevron] [icon] [title] [right meta]. Body mounts lazily on
 * first open (defer) so collapsed history with heavy content never pays the
 * parse cost — mirrors opencode's BasicTool defer. Once opened it stays mounted
 * (cheap to toggle after that).
 */
import { useState, type ComponentPropsWithoutRef, type ReactNode } from 'react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'

interface CollapsibleCardProps {
  /** Controlled open state. When omitted, the card manages its own state. */
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /** Start open (used when uncontrolled). */
  defaultOpen?: boolean
  /** Leading icon node (e.g. a Lucide icon). */
  icon?: ReactNode
  title: ReactNode
  /** Right-aligned meta (status chip, count, ...). */
  meta?: ReactNode
  children: ReactNode
  className?: string
  /** Optional header class override. */
  headerClassName?: string
  /** Optional body class override. */
  bodyClassName?: string
  'aria-label'?: string
}

export function CollapsibleCard({
  open,
  onOpenChange,
  defaultOpen = false,
  icon,
  title,
  meta,
  children,
  className,
  headerClassName,
  bodyClassName,
  ...rest
}: CollapsibleCardProps) {
  const isControlled = open !== undefined
  // Single source of truth for the open state, mirrored from props when
  // controlled and self-managed otherwise. `mounted` lags behind so heavy body
  // content is created on first open and never torn down (defer, like opencode).
  const [internalOpen, setInternalOpen] = useState(defaultOpen)
  const [mounted, setMounted] = useState(defaultOpen)
  const currentOpen = isControlled ? open : internalOpen

  const handleOpenChange = (next: boolean) => {
    if (next) setMounted(true) // defer: mount body the first time it opens
    if (!isControlled) setInternalOpen(next)
    onOpenChange?.(next)
  }

  return (
    <Collapsible
      open={currentOpen}
      onOpenChange={handleOpenChange}
      className={cn('rounded-md border border-border bg-bg-secondary/40', className)}
      {...(rest['aria-label'] ? { 'aria-label': rest['aria-label'] } : {})}
    >
      <CollapsibleTrigger asChild>
        <button
          type="button"
          aria-expanded={currentOpen}
          className={cn(
            'flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs',
            'hover:bg-bg-tertiary/50 transition-colors',
            headerClassName,
          )}
        >
          <ChevronRight
            className={cn(
              'size-3.5 shrink-0 text-text-muted transition-transform',
              currentOpen && 'rotate-90',
            )}
          />
          {icon && <span className="shrink-0 text-text-secondary">{icon}</span>}
          <span className="min-w-0 flex-1 truncate font-medium text-text-secondary">{title}</span>
          {meta && <span className="shrink-0">{meta}</span>}
        </button>
      </CollapsibleTrigger>
      {mounted && (
        <CollapsibleContent className={cn('border-t border-border', bodyClassName)}>
          {children}
        </CollapsibleContent>
      )}
    </Collapsible>
  )
}

/** Props forwarded to a header button when used standalone. */
export type CollapsibleCardHeaderProps = ComponentPropsWithoutRef<'button'>
