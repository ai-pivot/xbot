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
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ComponentPropsWithoutRef } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import type { PluggableList } from 'unified'
import { Check, Copy } from 'lucide-react'

import { highlightSync, normalizeLanguage, ensureHljsLoaded, useHljsReady } from './highlight'
import { useCodeWordWrap } from '@/hooks/useCodeWordWrap'
import { useIsTouch } from '@/hooks/useIsMobile'
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
  const isTouch = useIsTouch()
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
      className={cn(
        'absolute right-2 top-2 flex size-7 items-center justify-center rounded-md transition-opacity hover:text-text-primary focus-visible:opacity-100 focus-visible:outline-none',
        isTouch ? 'opacity-60' : 'opacity-0 group-hover/code:opacity-100',
      )}
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

  // Synchronous highlighting: compute the highlighted HTML during render
  // (via useMemo), not in a post-render useEffect. This shares the streaming
  // debounce + typewriter clip optimizations — no "plain text → highlighted"
  // flash on every content change or re-render.
  //
  // First render: hljs not yet loaded → ensureHljsLoaded() kicks off the
  // dynamic import (fire-and-forget). useHljsReady() subscribes to the load
  // status so the component re-renders when hljs becomes available — then
  // highlightSync() returns immediately (synchronous, no async gap).
  const hljsReady = useHljsReady()
  const html = useMemo(
    () => (isInline ? null : highlightSync(text, lang)),
    // Re-compute when text/lang changes, or when hljs finishes loading
    // (transitions from plain-text → highlighted on the first code block).
    [text, lang, isInline, hljsReady],
  )

  // Kick off hljs load on first block render (no-op if already loaded/loading).
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { if (!isInline) ensureHljsLoaded() }, [])

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
  // ── Typewriter-gated re-parse ──
  // During streaming, the typewriter reveals content at gap/3 per 50ms tick.
  // Re-parsing markdown on every SSE chunk is wasteful — the user can't see
  // content beyond visibleChars yet. We hold the parsed content steady until
  // the typewriter catches up to (or past) the end of the currently-parsed
  // content. Only then do we advance to the latest content and re-parse.
  //
  // This makes md re-parse frequency = typewriter catch-up frequency (a few
  // times per second at most), NOT SSE chunk frequency (~50ms). The typewriter's
  // adaptive gap/3 speed ensures re-parse frequency scales with content growth
  // — fast SSE → larger gap → faster catch-up → slightly more frequent re-parses,
  // but still far less than per-chunk.
  //
  // parsedContentRef tracks what ParsedMarkdown actually rendered. We compare
  // visibleChars (from the parent's useTypewriter) against the rune length of
  // parsedContent to decide whether to advance.
  const parsedContentRef = useRef(content)
  const isStreamingMode = streaming && visibleChars !== undefined

  // Decide what content ParsedMarkdown should render this frame.
  // - Non-streaming: always use the latest (debounced) content.
  // - Streaming + content shrank (new iteration reset): advance immediately.
  // - Streaming + typer hasn't caught up: keep the old parsed content
  //   (parsedContentRef). The typer keeps clipping the existing DOM.
  // - Streaming + typer caught up: advance to latest content + re-parse.
  const latestContent = useDebouncedValue(content, 150, !streaming && !noDebounce)
  if (isStreamingMode) {
    // Content shrank (new iteration / store reset) → must advance immediately
    // so the old content doesn't linger.
    if (content.length < parsedContentRef.current.length) {
      parsedContentRef.current = latestContent
    } else {
      const parsedRunes = Array.from(parsedContentRef.current)
      if (visibleChars >= parsedRunes.length) {
        // Typer has caught up to the end of parsed content → advance.
        parsedContentRef.current = latestContent
      }
      // else: typer still catching up → keep old parsed content, typer clips.
    }
  } else {
    parsedContentRef.current = latestContent
  }
  const debouncedContent = parsedContentRef.current

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
}, (prev, next) => {
  // During streaming, `content` changes on every SSE chunk but we must NOT
  // block the re-render — the component needs to run the parsedContentRef
  // gate check (visibleChars >= parsed length?) to decide whether to advance.
  // If we short-circuit on content equality, the gate never runs and the
  // typer can't trigger a re-parse when it catches up.
  if (prev.streaming && next.streaming && prev.visibleChars !== undefined && next.visibleChars !== undefined) {
    // Only visibleChars changed (typer tick) — allow render so the
    // useLayoutEffect can clip. The parsedContentRef won't advance (visible
    // hasn't caught up yet), so no re-parse — just a clip.
    if (prev.content === next.content && prev.className === next.className && prev.noDebounce === next.noDebounce) {
      return false // allow render (visibleChars changed)
    }
  }
  return prev.content === next.content && prev.className === next.className &&
    prev.streaming === next.streaming && prev.noDebounce === next.noDebounce && prev.visibleChars === next.visibleChars
})
