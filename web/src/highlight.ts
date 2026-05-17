/**
 * Lazy highlight.js language registration.
 *
 * High-frequency languages (js/ts/go/python/bash/json) are pre-registered synchronously.
 * Low-frequency languages are registered on-demand when first encountered.
 */
import hljs from 'highlight.js/lib/core'
import 'highlight.js/styles/github-dark.css'

// --- Pre-register high-frequency languages ---
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('go', go)
hljs.registerLanguage('python', python)
hljs.registerLanguage('py', python)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('sh', bash)
hljs.registerLanguage('shell', bash)
hljs.registerLanguage('json', json)

// --- Lazy language map for low-frequency languages ---
const lazyLanguages: Record<string, () => Promise<unknown>> = {
  yaml: () => import('highlight.js/lib/languages/yaml'),
  css: () => import('highlight.js/lib/languages/css'),
  xml: () => import('highlight.js/lib/languages/xml'),
  markdown: () => import('highlight.js/lib/languages/markdown'),
  sql: () => import('highlight.js/lib/languages/sql'),
  rust: () => import('highlight.js/lib/languages/rust'),
  java: () => import('highlight.js/lib/languages/java'),
  cpp: () => import('highlight.js/lib/languages/cpp'),
  diff: () => import('highlight.js/lib/languages/diff'),
}

// Alias map: alias → canonical language name
const aliasMap: Record<string, string> = {
  yml: 'yaml',
  html: 'xml',
  svg: 'xml',
  md: 'markdown',
  rs: 'rust',
  c: 'cpp',
}

// Track registered low-frequency languages
const registered = new Set<string>()

/**
 * Ensure a language is registered. Returns true if the language is available.
 * For pre-registered languages, returns true immediately.
 * For lazy languages, triggers async import and returns false on first call.
 */
export async function ensureLanguage(lang: string): Promise<boolean> {
  // Resolve alias
  const resolved = aliasMap[lang] || lang

  // Already registered (pre-registered or previously loaded)
  if (hljs.getLanguage(resolved)) return true

  // Check if it's a lazy-loadable language
  const loader = lazyLanguages[resolved]
  if (!loader) return false

  // Already in the process of registering or registered
  if (registered.has(resolved)) return hljs.getLanguage(resolved) !== undefined

  registered.add(resolved)
  const mod = await loader()
  // Dynamic imports return { default: LanguageFn }
  const langDef = (mod as { default?: unknown }).default || mod
  hljs.registerLanguage(resolved, langDef as Parameters<typeof hljs.registerLanguage>[1])

  // Also register common aliases for the lazy-loaded language
  for (const [alias, canonical] of Object.entries(aliasMap)) {
    if (canonical === resolved && !hljs.getLanguage(alias)) {
      hljs.registerLanguage(alias, langDef as Parameters<typeof hljs.registerLanguage>[1])
    }
  }

  return true
}

/**
 * Check if a language is currently registered (synchronous).
 */
export function isLanguageRegistered(lang: string): boolean {
  const resolved = aliasMap[lang] || lang
  return hljs.getLanguage(resolved) !== undefined
}

/**
 * Get the hljs core instance for direct highlight calls.
 */
export { hljs }

export function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;')
}
