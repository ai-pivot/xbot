import { useState, useMemo, useEffect, memo } from 'react'
import { hljs, ensureLanguage, isLanguageRegistered, escapeHtml } from '../highlight'
import { MermaidBlock } from './MermaidBlock'
import { useTranslation } from '../i18n'
import { CODEBLOCK_COLLAPSE_LINES } from '../constants'

interface CodeBlockProps {
  className?: string
  children?: string
}

const CodeBlock = memo(function CodeBlock({ className, children }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)
  const [langReady, setLangReady] = useState(false)
  const [collapsed, setCollapsed] = useState(true)
  const { t } = useTranslation()

  const codeText = typeof children === 'string' ? children.trim() : String(children ?? '')

  // Extract language from className (react-markdown passes "language-xxx")
  const langMatch = className?.match(/language-(\w+)/)
  const lang = langMatch ? langMatch[1] : ''

  const lines = codeText.split('\n')
  const lineCount = lines.length
  const shouldCollapse = lineCount > CODEBLOCK_COLLAPSE_LINES

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
        <span>
          {lang || 'code'}
          {lineCount > 1 && <span className="xbot-codeblock-linecount">{lineCount} lines</span>}
        </span>
        <div className="xbot-codeblock-actions">
          {shouldCollapse && (
            <button
              onClick={() => setCollapsed(!collapsed)}
              className="xbot-codeblock-collapse-btn"
              aria-expanded={!collapsed}
              data-testid="codeblock-collapse-btn"
            >
              {collapsed ? t('expandCodeBlock', { lines: String(lineCount) }) : t('collapseCodeBlock')}
            </button>
          )}
          <button onClick={handleCopy} className="xbot-codeblock-copy" aria-label={t('copyCode')} data-testid="codeblock-copy-btn">
            {copied ? t('copied') : 'Copy'}
          </button>
        </div>
      </div>
      <div className={`xbot-codeblock-body ${collapsed && shouldCollapse ? 'xbot-codebody-collapsed' : ''}`}>
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
        {collapsed && shouldCollapse && <div className="xbot-codeblock-collapsed-mask" />}
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

// Returns components for react-markdown's components prop
export function getCodeBlockProps() {
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

      return <CodeBlock className={props.className}>{codeStr}</CodeBlock>
    },
    checkbox(props: { checked?: boolean }) {
      return (
        <input
          type="checkbox"
          disabled
          checked={!!props.checked}
          className="xbot-checkbox"
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
          <table className="min-w-full text-sm border-collapse">
            {props.children}
          </table>
        </div>
      )
    },
    th(props: { children?: React.ReactNode }) {
      return <th className="border border-slate-600 px-3 py-1.5 text-left text-xs font-medium text-slate-300 bg-slate-700/50">{props.children}</th>
    },
    td(props: { children?: React.ReactNode }) {
      return <td className="border border-slate-600 px-3 py-1.5 text-xs text-slate-300">{props.children}</td>
    },
  }
}
