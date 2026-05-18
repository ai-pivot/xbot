import { ERROR_PREVIEW_LENGTH } from '../constants'
import { useEffect, useId, useState } from 'react'
import DOMPurify from 'dompurify'
import { useTranslation } from '../i18n'

let mermaidModule: typeof import('mermaid').default | null = null
let mermaidLoadPromise: Promise<typeof import('mermaid').default> | null = null
let lastMermaidTheme: string | null = null

const PURIFY_CONFIG = {
  USE_PROFILES: { svg: true },
  ADD_TAGS: ['foreignObject'],
}

async function getMermaid(): Promise<typeof import('mermaid').default> {
  if (!mermaidLoadPromise) {
    mermaidLoadPromise = import('mermaid').then((mod) => {
      mermaidModule = mod.default
      return mermaidModule
    })
  }
  const mod = await mermaidLoadPromise
  const theme = document.documentElement.getAttribute('data-theme') === 'light' ? 'default' : 'dark'
  if (theme !== lastMermaidTheme) {
    mod.initialize({
      startOnLoad: false,
      theme: theme as 'dark' | 'default',
      securityLevel: 'strict',
      fontFamily: 'ui-sans-serif, system-ui, sans-serif',
      themeVariables: theme === 'dark' ? {
        primaryColor: '#6366f1',
        primaryTextColor: '#e2e8f0',
        primaryBorderColor: '#4f46e5',
        lineColor: '#64748b',
        secondaryColor: '#1e293b',
        tertiaryColor: '#0f172a',
        background: '#1e293b',
        mainBkg: '#1e293b',
        nodeBorder: '#4f46e5',
        clusterBkg: '#1e293b80',
        titleColor: '#e2e8f0',
        edgeLabelBackground: '#1e293b',
      } : {
        primaryColor: '#6366f1',
        primaryTextColor: '#1c1917',
        primaryBorderColor: '#4f46e5',
        lineColor: '#78716c',
        secondaryColor: '#e2dfda',
        tertiaryColor: '#f7f5f2',
        background: '#f7f5f2',
        mainBkg: '#f7f5f2',
        nodeBorder: '#4f46e5',
        clusterBkg: '#f7f5f280',
        titleColor: '#1c1917',
        edgeLabelBackground: '#f7f5f2',
      },
    })
    lastMermaidTheme = theme
  }
  return mod
}

/** Returns the current theme key ('light' or 'dark') from the DOM. */
function useTheme() {
  const [theme, setTheme] = useState(
    () => document.documentElement.getAttribute('data-theme') || 'dark'
  )
  useEffect(() => {
    const observer = new MutationObserver(() => {
      setTheme(document.documentElement.getAttribute('data-theme') || 'dark')
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])
  return theme
}

export function MermaidBlock({ code }: { code: string }) {
  // containerRef removed — not needed for dangerouslySetInnerHTML rendering
  const uniqueId = useId().replace(/:/g, '_')
  const [error, setError] = useState<string | null>(null)
  const [svg, setSvg] = useState<string>('')
  const theme = useTheme()
  const { t } = useTranslation()

  useEffect(() => {
    let cancelled = false
    const id = `mermaid_${uniqueId}`

    getMermaid()
      .then((mermaid) => mermaid.render(id, code.trim()))
      .then(({ svg }) => {
        if (!cancelled) setSvg(DOMPurify.sanitize(svg, PURIFY_CONFIG) as string)
      })
      .catch((err) => {
        if (!cancelled) {
          const msg = err?.message || String(err)
          setError(msg.length > ERROR_PREVIEW_LENGTH ? msg.slice(0, ERROR_PREVIEW_LENGTH) + '...' : msg)
        }
      })

    return () => {
      cancelled = true
      const ids = [id, `${id}-d`]
      ids.forEach((eid) => {
        const el = document.getElementById(eid)
        if (el) el.remove()
      })
    }
  }, [code, uniqueId, theme])

  if (error) {
    return (
      <div className="rounded-lg bg-red-900/20 border border-red-800/40 p-3 text-sm text-red-400">
        <div className="font-semibold mb-1">{t('mermaidRenderFailed')}</div>
        <pre className="text-xs overflow-x-auto whitespace-pre-wrap">{error}</pre>
      </div>
    )
  }

  if (!svg) {
    return (
      <div className="mermaid-wrapper animate-pulse">
        <div className="text-sm text-slate-400">{t('rendering')}</div>
      </div>
    )
  }

  return (
    <div
      className="mermaid-wrapper"
      
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
