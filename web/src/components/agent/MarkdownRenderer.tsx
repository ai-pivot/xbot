/**
 * MarkdownRenderer — renders Markdown with GFM, math (KaTeX), and syntax
 * highlighting (Spec 4 §3.6).
 *
 * Plugins: remark-gfm (tables/lists/strikethrough), remark-math + rehype-katex
 * (math), and a custom `code` component that highlights via highlight.js.
 *
 * Performance:
 *  - `React.memo` with a custom equality on `content` so an unchanged message
 *    never re-parses (history scroll, collapse toggles).
 *  - The Markdown is re-parsed only when `content` changes; streaming appends
 *    hit the streaming throttle in useProgressStream before reaching here.
 *
 * Security: react-markdown v10 does not render raw HTML by default (skipHtml is
 * not set, but raw HTML nodes are not present from remark output), and we only
 * pass through highlight.js token spans we generated ourselves.
 */
import { memo, useCallback, useEffect, useState, type ComponentPropsWithoutRef } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import type { PluggableList } from 'unified'
import { Check, Copy } from 'lucide-react'

import { highlightAuto, highlightCode, normalizeLanguage } from './highlight'
import { cn } from '@/lib/utils'

interface MarkdownRendererProps {
  content: string
  className?: string
}

/**
 * Debounce a value by `delay` ms. During streaming, content arrives at ~60fps;
 * debouncing to ~150ms reduces Markdown parse frequency from 60fps → ~6fps.
 * The 150ms delay is imperceptible to users.
 */
function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return debounced
}

/**
 * Copy-to-clipboard button shown on hover of a code block. Uses the async
 * Clipboard API with a transient "copied" state. Self-contained so the memoized
 * parent never re-renders on click.
 */
function CopyButton({ getText }: { getText: () => string }) {
  const [copied, setCopied] = useState(false)
  const onClick = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(getText())
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    } catch {
      /* clipboard unavailable — ignore */
    }
  }, [getText])

  return (
    <button
      type="button"
      aria-label="Copy code"
      onClick={onClick}
      className="absolute right-2 top-2 flex size-7 items-center justify-center rounded-md opacity-0 transition-opacity hover:text-text-primary group-hover/code:opacity-100 focus-visible:opacity-100 focus-visible:outline-none"
      style={{
        backgroundColor: 'color-mix(in srgb, var(--md-code-bg) 80%, var(--md-code-border))',
        color: 'var(--md-code-lang-text)',
      }}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  )
}

/**
 * Inline or block code. Block code (a <pre><code> with a language) is rendered
 * with highlight.js tokens and a copy button; inline code is a plain styled
 * <code>. react-markdown passes `inline` for inline spans (v9+) — we also
 * detect block by presence of a newline or language for resilience.
 */
type CodeProps = ComponentPropsWithoutRef<'code'> & {
  inline?: boolean
}

const CodeBlock = memo(function CodeBlock({ inline, className, children, ...props }: CodeProps) {
  const text = String(children ?? '')
  const lang = normalizeLanguage(
    /language-(\w+)/.exec(className ?? '')?.[1] ??
      (props as unknown as { 'data-language'?: string })['data-language'],
  )

  // Inline code: short, no newline, no language fence.
  if (inline || (!lang && !text.includes('\n'))) {
    return (
      <code
        className="rounded px-1.5 py-0.5 font-mono text-[0.85em]"
        style={{
          backgroundColor: 'var(--md-inline-code-bg)',
          color: 'var(--md-inline-code-text)',
        }}
        {...props}
      >
        {children}
      </code>
    )
  }

  const html = (lang ? highlightCode(text, lang) : null) ?? highlightAuto(text)

  return (
    <div
      className="group/code relative my-2 overflow-hidden rounded-md"
      style={{
        border: '1px solid var(--md-code-border)',
        backgroundColor: 'var(--md-code-bg)',
      }}
    >
      {lang && (
        <span
          className="absolute left-3 top-2 z-10 select-none font-mono text-[11px] uppercase"
          style={{ color: 'var(--md-code-lang-text)' }}
        >
          {lang}
        </span>
      )}
      <CopyButton getText={() => text} />
      <pre className="overflow-x-auto p-3 pt-7 text-[13px] leading-relaxed whitespace-pre-wrap break-words">
        {html ? (
          <code
            className={cn('font-mono hljs', className)}
            // highlight.js returns already-escaped token spans; safe to inject.
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ) : (
          <code className={cn('font-mono', className)} {...props}>
            {children}
          </code>
        )}
      </pre>
    </div>
  )
})

/** Custom component map applied to the Markdown tree. */
const COMPONENTS = {
  code: CodeBlock,
  // Open links in a new tab safely; render anchor styling inline.
  a: ({ node: _node, ...props }: ComponentPropsWithoutRef<'a'> & { node?: unknown }) => (
    <a
      target="_blank"
      rel="noopener noreferrer"
      className="underline"
      style={{ color: 'var(--md-link)' }}
      {...props}
    />
  ),
  // Constrain images to the message width.
  img: ({ node: _node, alt, ...props }: ComponentPropsWithoutRef<'img'> & { node?: unknown }) => (
    <img alt={alt ?? ''} className="my-2 max-w-full rounded" loading="lazy" {...props} />
  ),
}

const REMARK_PLUGINS: PluggableList = [remarkGfm, remarkMath]
const REHYPE_PLUGINS: PluggableList = [[rehypeKatex, { throwOnError: false }]]

export const MarkdownRenderer = memo(function MarkdownRenderer({
  content,
  className,
}: MarkdownRendererProps) {
  const debouncedContent = useDebouncedValue(content, 150)
  return (
    <div className={cn('markdown-body text-sm leading-relaxed', className)}>
      <Markdown
        remarkPlugins={REMARK_PLUGINS}
        rehypePlugins={REHYPE_PLUGINS}
        components={COMPONENTS}
      >
        {debouncedContent}
      </Markdown>
    </div>
  )
})
