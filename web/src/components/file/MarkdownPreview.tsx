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
import { memo, useEffect, useState, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'

import { highlightCode as highlightCodeAsync, highlightAuto as highlightAutoAsync, normalizeLanguage } from '@/components/agent/highlight'
import { joinPath } from '@/hooks/useFileSystem'

import 'katex/dist/katex.min.css'
import './markdown-preview.css'

export interface MarkdownPreviewProps {
  /** Markdown source. */
  source: string
  /** Directory of the markdown file, used to resolve relative image paths. */
  baseDir?: string
  /** Extra className on the scroll container. */
  className?: string
}

function extractLanguage(className?: string): string | null {
  if (!className) return null
  const m = /language-([\w-]+)/.exec(className)
  return m ? m[1] : null
}

/** Highlight a fenced code block; returns HTML to render via dangerouslySetInnerHTML. */
/** Async code block: renders plain text first, highlights after highlight.js loads. */
function FileCodeBlock({ text, lang }: { text: string; lang: string | null }) {
  const [html, setHtml] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    const run = async () => {
      const normalized = normalizeLanguage(lang ?? undefined)
      const result = (normalized ? await highlightCodeAsync(text, normalized) : null) ?? await highlightAutoAsync(text)
      if (!cancelled && result) setHtml(result)
    }
    void run()
    return () => { cancelled = true }
  }, [text, lang])
  if (html) {
    return <code className={`hljs language-${lang ?? 'auto'}`} dangerouslySetInnerHTML={{ __html: html }} />
  }
  return <code className={`hljs language-${lang ?? 'auto'}`}>{text}</code>
}

/** True when this `code` node is a fenced block (has a language class or multiline). */
function isCodeBlock(className: string | undefined, children: ReactNode): boolean {
  if (extractLanguage(className)) return true
  const text = String(children ?? '')
  return text.includes('\n')
}

/**
 * Resolve a markdown image `src` against the markdown file's directory.
 * Absolute URLs (http/https/data/blob) are passed through unchanged.
 * Relative paths are joined with `baseDir` and rewritten to `/api/fs/raw?path=...`.
 */
function resolveImgSrc(src: string | undefined, baseDir?: string): string | undefined {
  if (!src) return src
  // Already a full URL or data/blob URI — leave as-is.
  if (/^(https?:|data:|blob:|\/api\/)/.test(src)) return src
  if (!baseDir) return src

  // Normalize: strip leading "./" but keep subdirectory paths intact.
  const cleanSrc = src.replace(/^\.\//, '')
  const absPath = src.startsWith('/') ? src : joinPath(baseDir, cleanSrc)
  return `/api/fs/raw?path=${encodeURIComponent(absPath)}`
}

export const MarkdownPreview = memo(function MarkdownPreview({
  source,
  baseDir,
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
              return <FileCodeBlock text={text.replace(/\n$/, '')} lang={lang} />
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
          img: ({ node: _node, alt, src, ...props }) => (
            <img
              alt={alt ?? ''}
              loading="lazy"
              src={resolveImgSrc(src, baseDir)}
              {...props}
            />
          ),
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  )
})
