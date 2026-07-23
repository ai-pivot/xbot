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
import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, type ComponentPropsWithoutRef } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import type { PluggableList } from 'unified'
import { Check, Copy } from 'lucide-react'

import { highlightAuto, highlightCode, normalizeLanguage } from './highlight'
import { useCodeWordWrap } from '@/hooks/useCodeWordWrap'
import { cn } from '@/lib/utils'

interface MarkdownRendererProps {
  content: string
  className?: string
  /** True while the source is live; keeps the rendered markdown current. */
  streaming?: boolean
  /** Skip debounce and render immediately. Used by committed messages
   *  that don't need the streaming debounce delay. */
  noDebounce?: boolean
  /** Number of source characters to reveal without re-parsing markdown. */
  visibleChars?: number
}

/**
 * Debounce a value by `delay` ms. During non-streaming renders, this reduces
 * Markdown parse frequency. During streaming (typewriter), `streaming` prop
 * bypasses the debounce so each 50ms tick renders immediately.
 */
function useDebouncedValue<T>(value: T, delay: number, enabled: boolean): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    if (!enabled) return // bypass: use raw value
    const timer = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay, enabled])
  return enabled ? debounced : value
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
  const { wordWrap } = useCodeWordWrap()
  const text = String(children ?? '')
  const lang = normalizeLanguage(
    /language-(\w+)/.exec(className ?? '')?.[1] ??
      (props as unknown as { 'data-language'?: string })['data-language'],
  )
  const isInline = inline || (!lang && !text.includes('\n'))

  // Async highlighting: render plain text first (instant LCP), then swap in
  // highlighted HTML after highlight.js loads. This keeps highlight.js (~300KB)
  // off the critical render path.
  const [html, setHtml] = useState<string | null>(null)
  useEffect(() => {
    if (isInline) return
    let cancelled = false
    const run = async () => {
      const result = (lang ? await highlightCode(text, lang) : null) ?? await highlightAuto(text)
      if (!cancelled && result) setHtml(result)
    }
    void run()
    return () => { cancelled = true }
  }, [text, lang, isInline])

  // Inline code: short, no newline, no language fence.
  if (isInline) {
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
      <pre className={cn(
        'p-3 pt-7 text-[13px] leading-relaxed',
        wordWrap ? 'whitespace-pre-wrap break-words' : 'overflow-x-auto whitespace-pre',
      )}>
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

/**
 * remark-math follows Markdown math syntax ($ / $$), while models commonly
 * emit TeX delimiters (\\( / \\[). Normalize only outside fenced and inline
 * code so both notations reach the same authoritative remark-math parser.
 */
function normalizeMathDelimiters(markdown: string): string {
  const lines = markdown.split('\n')
  let fence = ''
  return lines.map((line) => {
    const fenceMatch = line.match(/^\s*(`{3,}|~{3,})/)
    if (fenceMatch) {
      const marker = fenceMatch[1][0]
      if (!fence) fence = marker
      else if (fence === marker) fence = ''
      return line
    }
    if (fence) return line

    const parts = line.split(/(`+[^`]*`+)/g)
    return parts.map((part, index) => {
      if (index % 2 === 1) return part
      return part
        .replace(/\\\[/g, () => '$$')
        .replace(/\\\]/g, () => '$$')
        .replace(/\\\(/g, () => '$')
        .replace(/\\\)/g, () => '$')
    }).join('')
  }).join('\n')
}

function clipTextNodes(root: HTMLElement, visibleChars: number): void {
  let remaining = Math.max(0, visibleChars)
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  let node: Node | null
  while ((node = walker.nextNode())) nodes.push(node as Text)
  for (const text of nodes) {
    const source = text.data
    const runes = Array.from(source)
    if (remaining >= runes.length) {
      remaining -= runes.length
      continue
    }
    text.data = runes.slice(0, remaining).join('')
    remaining = 0
    for (const rest of nodes.slice(nodes.indexOf(text) + 1)) rest.data = ''
    break
  }
}

const ParsedMarkdown = memo(function ParsedMarkdown({ content }: { content: string }) {
  return (
    <Markdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS} components={COMPONENTS}>
      {normalizeMathDelimiters(content)}
    </Markdown>
  )
})

export const MarkdownRenderer = memo(function MarkdownRenderer({
  content,
  className,
  streaming = false,
  noDebounce = false,
  visibleChars,
}: MarkdownRendererProps) {
  const debouncedContent = useDebouncedValue(content, 150, !streaming && !noDebounce)
  const rootRef = useRef<HTMLDivElement>(null)
  // Cache of full text per Text node. Keyed by node identity — valid only
  // within a single ParsedMarkdown render (React reuses nodes when content
  // is unchanged, replaces them when content changes). We rebuild this cache
  // whenever debouncedContent changes, and use it to restore text.data on
  // typewriter ticks (where content is the same but text.data was clipped).
  const sourceRef = useRef(new Map<Text, string>())
  const sourceContentRef = useRef<string | null>(null)

  useLayoutEffect(() => {
    const root = rootRef.current
    if (!root || visibleChars === undefined) return

    const contentChanged = sourceContentRef.current !== debouncedContent

    if (contentChanged) {
      // New content → React rendered fresh DOM. Capture full text from DOM
      // (React just set it, so text.data is the full value). No restore needed.
      sourceContentRef.current = debouncedContent
      sourceRef.current = new Map()
    } else {
      // Typewriter tick (same content) → text.data was clipped by previous
      // tick. Restore full text from sourceRef before re-clipping.
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
      let node: Node | null
      while ((node = walker.nextNode())) {
        const text = node as Text
        const saved = sourceRef.current.get(text)
        if (saved !== undefined) {
          text.data = saved
        }
      }
    }

    // Capture full text for all current Text nodes (new or restored).
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    let node: Node | null
    while ((node = walker.nextNode())) {
      const text = node as Text
      if (!sourceRef.current.has(text)) {
        sourceRef.current.set(text, text.data)
      }
    }

    clipTextNodes(root, visibleChars)
  }, [visibleChars, debouncedContent])

  return (
    <div ref={rootRef} className={cn('markdown-body text-sm leading-relaxed', className)}>
      {/* key forces React to create fresh DOM nodes on every content change.
          clipTextNodes mutates text.data behind React's back; without a remount,
          React's reconciler skips DOM updates for text nodes whose virtual DOM
          value is unchanged, leaving clipped (empty) values in place. */}
      <ParsedMarkdown key={debouncedContent} content={debouncedContent} />
    </div>
  )
}, (prev, next) => (
  prev.content === next.content && prev.className === next.className &&
  prev.streaming === next.streaming && prev.noDebounce === next.noDebounce && prev.visibleChars === next.visibleChars
))
