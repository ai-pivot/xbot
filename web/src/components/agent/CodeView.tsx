/**
 * CodeView — code renderer with a line-number gutter and highlight.js coloring.
 *
 * Used by the Read tool renderer (and any tool that wants a code-preview look).
 * The gutter and the highlighted <pre> are two columns of one scroll container
 * sharing identical font metrics — identical leading keeps them row-aligned
 * without splitting hljs output across lines (which would break multi-line
 * spans).
 *
 * Highlighting follows the CodeBlock pattern: useMemo(highlightSync) during
 * render + ensureHljsLoaded() on mount + useHljsReady() to re-render when the
 * lazy hljs chunk finishes loading.
 */
import { memo, useEffect, useMemo } from 'react'

import { ensureHljsLoaded, highlightSync, useHljsReady } from './highlight'

interface CodeViewProps {
  code: string
  /** hljs language id (e.g. from the file extension). */
  language?: string
  /** 1-based number of the FIRST line (Read offset). */
  startLine?: number
  /** Max body height before vertical scrolling. */
  maxHeight?: number
  /** Muted notice line pinned at the bottom (e.g. truncation hint). */
  notice?: string
}

export const CodeView = memo(function CodeView({
  code,
  language,
  startLine = 1,
  maxHeight = 320,
  notice,
}: CodeViewProps) {
  const hljsReady = useHljsReady()
  const html = useMemo(() => highlightSync(code, language), [code, language, hljsReady])
  useEffect(() => {
    ensureHljsLoaded()
  }, [])

  const lineCount = useMemo(() => code.split('\n').length, [code])
  const lineNumbers = useMemo(
    () => Array.from({ length: lineCount }, (_, i) => startLine + i),
    [lineCount, startLine],
  )

  return (
    <div
      className="overflow-auto rounded-md"
      style={{ border: '1px solid var(--border)', maxHeight }}
    >
      <div className="flex min-w-max">
        {/* Line-number gutter */}
        <div
          className="sticky left-0 z-[1] shrink-0 select-none py-1.5 pl-2 pr-2 text-right font-mono text-[10px] leading-5"
          style={{ backgroundColor: 'var(--bg-secondary)', color: 'var(--text-muted)' }}
        >
          {lineNumbers.map((n) => (
            <div key={n}>{n}</div>
          ))}
        </div>
        {/* Highlighted code */}
        <div className="min-w-0 flex-1 py-1.5 pl-2 pr-3">
          {html != null ? (
            <pre
              className="whitespace-pre font-mono text-[12px] leading-5"
              style={{ color: 'var(--text-primary)' }}
              dangerouslySetInnerHTML={{ __html: html }}
            />
          ) : (
            <pre className="whitespace-pre font-mono text-[12px] leading-5 text-text-secondary">{code}</pre>
          )}
        </div>
      </div>
      {notice && (
        <div
          className="px-3 py-1 font-mono text-[10px] italic"
          style={{ backgroundColor: 'var(--bg-secondary)', color: 'var(--text-muted)', borderTop: '1px solid var(--border)' }}
        >
          {notice}
        </div>
      )}
    </div>
  )
})
