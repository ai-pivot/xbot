import { memo } from 'react'

interface CollapsibleContentProps {
  /** @deprecated threshold is ignored — content is always fully expanded */
  threshold?: number
  children: React.ReactNode
}

/**
 * Passthrough wrapper — content is always fully expanded.
 * Previously this auto-collapsed long content, but it caused virtualizer
 * measurement loops. Now it just renders children directly.
 */
const CollapsibleContent = memo(function CollapsibleContent({
  children,
}: CollapsibleContentProps) {
  return <>{children}</>
})

export default CollapsibleContent
