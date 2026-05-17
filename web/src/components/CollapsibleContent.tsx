import { useState, useRef, useEffect, useCallback, memo } from 'react'
import { useTranslation } from '../i18n'
import { COLLAPSE_LINE_THRESHOLD } from '../constants'

interface CollapsibleContentProps {
  /** Line count threshold to trigger collapse (default: COLLAPSE_LINE_THRESHOLD = 20) */
  threshold?: number
  children: React.ReactNode
}

/**
 * Generic collapsible wrapper for long content.
 * Uses ResizeObserver to detect when content exceeds the line threshold,
 * then shows a gradient mask + expand/collapse button.
 */
const CollapsibleContent = memo(function CollapsibleContent({
  threshold = COLLAPSE_LINE_THRESHOLD,
  children,
}: CollapsibleContentProps) {
  const [collapsed, setCollapsed] = useState(true)
  const [tooLong, setTooLong] = useState(false)
  const contentRef = useRef<HTMLDivElement>(null)
  const measuredRef = useRef(false)
  const { t } = useTranslation()

  const checkOverflow = useCallback(() => {
    const el = contentRef.current
    if (!el) return
    const lineHeight = parseFloat(getComputedStyle(el).lineHeight) || 20
    const lineCount = Math.ceil(el.scrollHeight / lineHeight)
    const isTooLong = lineCount > threshold
    // Only transition from false→true or when content changes (measuredRef reset)
    if (isTooLong !== tooLong || !measuredRef.current) {
      setTooLong(isTooLong)
      measuredRef.current = true
    }
  }, [threshold, tooLong])

  // Use ResizeObserver for reliable overflow detection
  useEffect(() => {
    const el = contentRef.current
    if (!el) return

    // Initial check
    checkOverflow()

    const observer = new ResizeObserver(() => {
      // Reset measured flag on resize to allow re-evaluation
      measuredRef.current = false
      checkOverflow()
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [checkOverflow, children])

  // If content doesn't overflow, render as-is
  if (!tooLong) {
    return <div ref={contentRef}>{children}</div>
  }

  return (
    <div ref={contentRef} className="collapsible-wrapper">
      <div className={`collapsible-content ${collapsed ? 'collapsible-collapsed' : ''}`}>
        {children}
        {collapsed && <div className="collapsible-fade-mask" />}
      </div>
      <button
        onClick={() => setCollapsed(!collapsed)}
        className="collapsible-toggle"
        aria-expanded={!collapsed}
        data-testid="collapsible-toggle"
      >
        <span className={`collapsible-chevron ${collapsed ? '' : 'collapsible-chevron-open'}`}>▸</span>
        {collapsed ? t('expandAll') : t('collapse')}
      </button>
    </div>
  )
})

export default CollapsibleContent
