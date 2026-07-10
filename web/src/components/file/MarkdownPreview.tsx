/**
 * MarkdownPreview — GFM + KaTeX + code-highlight preview (Spec 5 §3.4).
 *
 * Reuses react-markdown with remark-gfm (tables/task-lists/strikethrough) and
 * rehype-katex (math). Code blocks are syntax-highlighted via highlight.js
 * through a custom `code` component — there is no `rehype-highlight` dep, so we
 * highlight in-place and fall back to auto-detection for unknown languages.
 *
 * KaTeX styles are imported here (once) so the rendered math is styled; links
 * open in a new tab. The container is scrollable by the panel, not internally.
 */
import { memo, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import hljs from 'highlight.js'

import 'katex/dist/katex.min.css'
import './markdown-preview.css'

export interface MarkdownPreviewProps {
  /** Markdown source. */
  source: string
  /** Extra className on the scroll container. */
  className?: string
}

function extractLanguage(className?: string): string | null {
  if (!className) return null
  const m = /language-([\w-]+)/.exec(className)
  return m ? m[1] : null
}

/** Highlight a fenced code block; returns HTML to render via dangerouslySetInnerHTML. */
function highlightCode(code: string, lang: string | null): { html: string; __html: string } {
  try {
    if (lang && hljs.getLanguage(lang)) {
      const res = hljs.highlight(code, { language: lang })
      return { html: res.value, __html: res.value }
    }
  } catch {
    /* fall through to auto */
  }
  const auto = hljs.highlightAuto(code)
  return { html: auto.value, __html: auto.value }
}

/** True when this `code` node is a fenced block (has a language class or multiline). */
function isCodeBlock(className: string | undefined, children: ReactNode): boolean {
  if (extractLanguage(className)) return true
  const text = String(children ?? '')
  return text.includes('\n')
}

export const MarkdownPreview = memo(function MarkdownPreview({
  source,
  className,
}: MarkdownPreviewProps) {
  return (
    <div className={`md-body h-full overflow-auto px-4 py-3 ${className ?? ''}`}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkMath]}
        rehypePlugins={[rehypeKatex]}
        components={{
          // Fenced code → highlighted <pre><code>; inline → plain <code>.
          // `node` is react-markdown's HAST node — never forward it to the DOM.
          code({ node: _node, className, children, ...props }) {
            const text = String(children ?? '')
            if (isCodeBlock(className, children)) {
              const lang = extractLanguage(className)
              const { __html } = highlightCode(text.replace(/\n$/, ''), lang)
              return (
                <code
                  className={`hljs language-${lang ?? 'auto'}`}
                  dangerouslySetInnerHTML={{ __html }}
                  {...props}
                />
              )
            }
            return (
              <code className="md-inline-code" {...props}>
                {children}
              </code>
            )
          },
          a: ({ node: _node, children, ...props }) => (
            <a target="_blank" rel="noopener noreferrer" {...props}>
              {children}
            </a>
          ),
          img: ({ node: _node, alt, ...props }) => (
            <img alt={alt ?? ''} loading="lazy" {...props} />
          ),
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  )
})
