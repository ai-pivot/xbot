import { useState, useMemo, useEffect, memo } from 'react'
import { hljs, ensureLanguage, isLanguageRegistered, escapeHtml } from '../highlight'
import { MermaidBlock } from './MermaidBlock'
import { useTranslation } from '../i18n'
import { IconCopy, IconCheck } from './Icons'

interface CodeBlockProps {
  className?: string
  children?: string
}

const CodeBlock = memo(function CodeBlock({ className, children }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)
  const [langReady, setLangReady] = useState(false)
  const { t } = useTranslation()

  const codeText = typeof children === 'string' ? children.trim() : String(children ?? '')

  // Extract language from className (react-markdown passes "language-xxx")
  const langMatch = className?.match(/language-(\w+)/)
  const lang = langMatch ? langMatch[1] : ''

  const lines = codeText.split('\n')

  // Track whether the language has been loaded (for lazy languages)
  useEffect(() => {
    if (!lang || codeText.length > 5000) {
      setLangReady(false)
      return
    }
    if (isLanguageRegistered(lang)) {
      setLangReady(true)
      return
    }
    // Lazy-load the language
    setLangReady(false)
    let cancelled = false
    ensureLanguage(lang).then((ok) => {
      if (!cancelled && ok) setLangReady(true)
    })
    return () => { cancelled = true }
  }, [lang, codeText.length])

  const highlighted = useMemo(() => {
    // Skip highlighting for long code or missing language
    if (!lang || codeText.length > 5000) {
      return escapeHtml(codeText)
    }
    if (!langReady && !isLanguageRegistered(lang)) {
      return escapeHtml(codeText)
    }
    try {
      if (hljs.getLanguage(lang)) {
        return hljs.highlight(codeText, { language: lang }).value
      }
    } catch {
      // fallback to escaped plain text
    }
    return escapeHtml(codeText)
  }, [codeText, lang, langReady])

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(codeText)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // fallback
      const ta = document.createElement('textarea')
      ta.value = codeText
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <div className="xbot-codeblock">
      <div className="xbot-codeblock-header">
        <span className="xbot-codeblock-lang">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>
          {lang || 'code'}
        </span>
        <div className="xbot-codeblock-actions">
          <button onClick={handleCopy} className="xbot-codeblock-copy" aria-label={t('copyCode')} data-testid="codeblock-copy-btn">
            {copied ? <IconCheck /> : <IconCopy />}
          </button>
        </div>
      </div>
      <div className="xbot-codeblock-body">
        <pre className="xbot-codeblock-pre">
          {/* Line numbers column */}
          <code className="xbot-codeblock-linenums" aria-hidden="true">
            {lines.map((_, i) => (
              <div key={i}>{i + 1}</div>
            ))}
          </code>
          {/* Code column */}
          <code className="xbot-codeblock-code" dangerouslySetInnerHTML={{ __html: highlighted }} />
        </pre>
      </div>
    </div>
  )
})

// Check if a React node tree contains a checkbox element
function containsCheckbox(children: React.ReactNode): boolean {
  if (!children) return false
  if (Array.isArray(children)) return children.some(containsCheckbox)
  if (typeof children === 'object' && children !== null && 'type' in children) {
    const child = children as { type: string | symbol; props?: Record<string, unknown> }
    if (child.type === 'input') return true
    const childProps = (children as React.ReactElement).props
    if (childProps && typeof childProps === 'object' && 'children' in childProps) {
      return containsCheckbox(childProps.children as React.ReactNode)
    }
  }
  return false
}

/** Interactive checkbox — local toggle state, purely visual */
const InteractiveCheckbox = memo(function InteractiveCheckbox({ checked: initialChecked }: { checked?: boolean }) {
  const [checked, setChecked] = useState(!!initialChecked)
  return (
    <input
      type="checkbox"
      checked={checked}
      onChange={() => setChecked(!checked)}
      className="xbot-checkbox xbot-checkbox-interactive"
    />
  )
})

// Returns components for react-markdown's components prop
export function getCodeBlockProps(onImageClick?: (src: string, alt: string) => void) {
  return {
    code(props: { className?: string; children?: React.ReactNode; inline?: boolean }) {
      const lang = props.className?.replace('language-', '')
      const codeStr = String(props.children ?? '')

      // Inline code (no className or in a span)
      if (!lang && !codeStr.includes('\n')) {
        return (
          <code className="xbot-inline-code">
            {props.children}
          </code>
        )
      }

      // Mermaid diagram — render as SVG instead of code block
      if (lang === 'mermaid') {
        return <MermaidBlock code={codeStr} />
      }

      // Latex/math — skip code block, render as plain text (KaTeX handles $...$ syntax)
      if (lang === 'latex' || lang === 'math') {
        return <code className="xbot-inline-code">{codeStr}</code>
      }

      return <CodeBlock className={props.className}>{codeStr}</CodeBlock>
    },
    checkbox(props: { checked?: boolean }) {
      return <InteractiveCheckbox checked={props.checked} />
    },
    img(props: { src?: string; alt?: string }) {
      return (
        <img
          src={props.src}
          alt={props.alt || ''}
          loading="lazy"
          className="xbot-lazy-img"
          onClick={() => {
            if (props.src && onImageClick) onImageClick(props.src, props.alt || '')
          }}
          style={{ cursor: 'zoom-in' }}
        />
      )
    },
    li(props: { children?: React.ReactNode; className?: string }) {
      const hasCheckbox = containsCheckbox(props.children)

      if (hasCheckbox) {
        return (
          <li className="xbot-task-item">
            {props.children}
          </li>
        )
      }

      if (props.className && /task-list-item/.test(props.className)) {
        return (
          <li
            className={`xbot-task-list-item ${props.className || ''}`}
          >
            {props.children}
          </li>
        )
      }

      return <li>{props.children}</li>
    },
    a(props: { href?: string; children?: React.ReactNode }) {
      return <a href={props.href} target="_blank" rel="noopener noreferrer">{props.children}</a>
    },
    pre(props: { children?: React.ReactNode }) {
      return <>{props.children}</>
    },
    table(props: { children?: React.ReactNode }) {
      return (
        <div className="overflow-x-auto my-2">
          <table>
            {props.children}
          </table>
        </div>
      )
    },
    th(props: { children?: React.ReactNode }) {
      return <th className="px-3 py-2 text-left font-semibold">{props.children}</th>
    },
    td(props: { children?: React.ReactNode }) {
      return <td className="px-3 py-2">{props.children}</td>
    },
  }
}
