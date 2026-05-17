import { useState, useMemo, useEffect, memo } from 'react'
import { hljs, ensureLanguage, isLanguageRegistered, escapeHtml } from '../highlight'
import { MermaidBlock } from './MermaidBlock'

interface CodeBlockProps {
  className?: string
  children?: string
}

const CodeBlock = memo(function CodeBlock({ className, children }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)
  const [langReady, setLangReady] = useState(false)

  const codeText = typeof children === 'string' ? children.trim() : String(children ?? '')

  // Extract language from className (react-markdown passes "language-xxx")
  const langMatch = className?.match(/language-(\w+)/)
  const lang = langMatch ? langMatch[1] : ''

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

  const lineCount = codeText.split('\n').length

  return (
    <div className="xbot-codeblock">
      <div className="xbot-codeblock-header">
        <span>{lang || 'code'}{lineCount > 1 && <span className="ml-2 text-slate-600 text-[10px]">{lineCount} lines</span>}</span>
        <button onClick={handleCopy} className="xbot-codeblock-copy">
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
      <pre className="xbot-codeblock-pre">
        <code dangerouslySetInnerHTML={{ __html: highlighted }} />
      </pre>
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
        // Dynamic import to avoid loading mermaid bundle unless needed
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
          style={{ margin: '0 6px 0 0', accentColor: '#3b82f6', cursor: 'default', pointerEvents: 'none' }}
        />
      )
    },
    li(props: { children?: React.ReactNode; className?: string }) {
      const hasCheckbox = containsCheckbox(props.children)

      if (hasCheckbox) {
        return (
          <li style={{ display: 'flex', alignItems: 'flex-start', gap: 0 }}>
            {props.children}
          </li>
        )
      }

      // react-markdown checkbox plugin uses className "task-list-item checked" for [x]
      if (props.className && /task-list-item/.test(props.className)) {
        return (
          <li
            style={{
              display: 'flex',
              alignItems: 'flex-start',
              listStyle: 'none',
              marginLeft: '-1.5em',
            }}
            className={props.className}
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
      // Unwrap react-markdown's outer <pre> — the code component already
      // renders its own container (.xbot-codeblock / .mermaid-wrapper / etc.)
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
