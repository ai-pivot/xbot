/**
 * Code highlighting helper for the Markdown renderer (Spec 4).
 *
 * Uses highlight.js with a curated subset of common languages. All highlight.js
 * modules are dynamically imported so the ~300KB library is NOT in the initial
 * bundle — code blocks render as plain text first (instant LCP), then get
 * highlighted after the lazy chunk loads.
 *
 * Synchronous API: once highlight.js is loaded (first code block triggers the
 * dynamic import), all subsequent highlights are SYNCHRONOUS via
 * `highlightSync()`. This lets CodeBlock compute the highlighted HTML during
 * render (useMemo) instead of a post-render useEffect — the highlight result
 * is part of the same React render pass as the markdown parse, sharing the
 * streaming debounce + typewriter clip optimizations.
 *
 * Falls back to plain text when the language is unknown or highlighting throws.
 */

import { useSyncExternalStore } from 'react'

/** Normalize a fenced-block info string ("ts", "typescript", "  ts x") to a language id. */
export function normalizeLanguage(lang: string | undefined): string | undefined {
  if (!lang) return undefined
  const trimmed = lang.trim().split(/\s+/)[0]?.toLowerCase()
  return trimmed || undefined
}

/** LRU cache for highlight results — committed messages re-render frequently
 * (scroll, collapse toggles) so cache hits approach 100%. limit=200 prevents
 * unbounded growth in long sessions. */
const hlCache = new Map<string, string | null>()
const CACHE_LIMIT = 200

function cacheGet(key: string): string | null | undefined {
  const val = hlCache.get(key)
  if (val !== undefined) {
    hlCache.delete(key)
    hlCache.set(key, val)
  }
  return val
}

function cacheSet(key: string, value: string | null): void {
  if (hlCache.size >= CACHE_LIMIT) {
    const oldest = hlCache.keys().next().value
    if (oldest !== undefined) hlCache.delete(oldest)
  }
  hlCache.set(key, value)
}

// Lazy-loaded highlight.js instance. All imports are dynamic so highlight.js
// stays in a separate chunk that loads on first code block render, not on
// initial page load.
import type HLJSApi from 'highlight.js/lib/core'

let hljsInstance: typeof HLJSApi | null = null
let loadPromise: Promise<typeof HLJSApi> | null = null

/** Kick off the dynamic import of highlight.js (fire-and-forget).
 *  Called on first code block render so the chunk starts loading immediately.
 *  Subsequent calls are no-ops if already loaded or loading.
 *  Notifies subscribers when loading completes (for useSyncExternalStore). */
export function ensureHljsLoaded(): void {
  if (hljsInstance || loadPromise) return
  loadPromise = loadHljs().then((hljs) => {
    notifyHljsReady()
    return hljs
  })
}

/** Synchronously get the hljs instance, or null if not yet loaded. */
export function getHljsSync(): typeof HLJSApi | null {
  return hljsInstance
}

async function loadHljs(): Promise<typeof HLJSApi> {
  if (hljsInstance) return hljsInstance
  if (loadPromise) return loadPromise
  loadPromise = (async () => {
    const [
      { default: hljs },
      { default: bash },
      { default: c },
      { default: cpp },
      { default: csharp },
      { default: css },
      { default: diff },
      { default: dockerfile },
      { default: go },
      { default: ini },
      { default: java },
      { default: javascript },
      { default: json },
      { default: kotlin },
      { default: lua },
      { default: markdown },
      { default: perl },
      { default: php },
      { default: python },
      { default: ruby },
      { default: rust },
      { default: shell },
      { default: sql },
      { default: swift },
      { default: typescript },
      { default: xml },
      { default: yaml },
    ] = await Promise.all([
      import('highlight.js/lib/core'),
      import('highlight.js/lib/languages/bash'),
      import('highlight.js/lib/languages/c'),
      import('highlight.js/lib/languages/cpp'),
      import('highlight.js/lib/languages/csharp'),
      import('highlight.js/lib/languages/css'),
      import('highlight.js/lib/languages/diff'),
      import('highlight.js/lib/languages/dockerfile'),
      import('highlight.js/lib/languages/go'),
      import('highlight.js/lib/languages/ini'),
      import('highlight.js/lib/languages/java'),
      import('highlight.js/lib/languages/javascript'),
      import('highlight.js/lib/languages/json'),
      import('highlight.js/lib/languages/kotlin'),
      import('highlight.js/lib/languages/lua'),
      import('highlight.js/lib/languages/markdown'),
      import('highlight.js/lib/languages/perl'),
      import('highlight.js/lib/languages/php'),
      import('highlight.js/lib/languages/python'),
      import('highlight.js/lib/languages/ruby'),
      import('highlight.js/lib/languages/rust'),
      import('highlight.js/lib/languages/shell'),
      import('highlight.js/lib/languages/sql'),
      import('highlight.js/lib/languages/swift'),
      import('highlight.js/lib/languages/typescript'),
      import('highlight.js/lib/languages/xml'),
      import('highlight.js/lib/languages/yaml'),
    ])
    hljs.registerLanguage('bash', bash)
    hljs.registerLanguage('sh', shell)
    hljs.registerLanguage('c', c)
    hljs.registerLanguage('h', c)
    hljs.registerLanguage('cpp', cpp)
    hljs.registerLanguage('c++', cpp)
    hljs.registerLanguage('hpp', cpp)
    hljs.registerLanguage('cc', cpp)
    hljs.registerLanguage('cxx', cpp)
    hljs.registerLanguage('csharp', csharp)
    hljs.registerLanguage('cs', csharp)
    hljs.registerLanguage('css', css)
    hljs.registerLanguage('scss', css)
    hljs.registerLanguage('diff', diff)
    hljs.registerLanguage('patch', diff)
    hljs.registerLanguage('dockerfile', dockerfile)
    hljs.registerLanguage('docker', dockerfile)
    hljs.registerLanguage('go', go)
    hljs.registerLanguage('golang', go)
    hljs.registerLanguage('ini', ini)
    hljs.registerLanguage('conf', ini)
    hljs.registerLanguage('config', ini)
    hljs.registerLanguage('java', java)
    hljs.registerLanguage('javascript', javascript)
    hljs.registerLanguage('js', javascript)
    hljs.registerLanguage('jsx', javascript)
    hljs.registerLanguage('json', json)
    hljs.registerLanguage('jsonc', json)
    hljs.registerLanguage('kotlin', kotlin)
    hljs.registerLanguage('kt', kotlin)
    hljs.registerLanguage('lua', lua)
    hljs.registerLanguage('markdown', markdown)
    hljs.registerLanguage('md', markdown)
    hljs.registerLanguage('perl', perl)
    hljs.registerLanguage('pl', perl)
    hljs.registerLanguage('php', php)
    hljs.registerLanguage('python', python)
    hljs.registerLanguage('py', python)
    hljs.registerLanguage('ruby', ruby)
    hljs.registerLanguage('rb', ruby)
    hljs.registerLanguage('rust', rust)
    hljs.registerLanguage('rs', rust)
    hljs.registerLanguage('shell', shell)
    hljs.registerLanguage('sql', sql)
    hljs.registerLanguage('swift', swift)
    hljs.registerLanguage('typescript', typescript)
    hljs.registerLanguage('ts', typescript)
    hljs.registerLanguage('tsx', typescript)
    hljs.registerLanguage('xml', xml)
    hljs.registerLanguage('html', xml)
    hljs.registerLanguage('xhtml', xml)
    hljs.registerLanguage('svg', xml)
    hljs.registerLanguage('yaml', yaml)
    hljs.registerLanguage('yml', yaml)
    hljs.registerAliases(['go'], { languageName: 'go' })
    hljsInstance = hljs
    return hljs
  })()
  return loadPromise
}

/**
 * Highlight `code` for `language`, returning an HTML string of <span> tokens.
 * Returns null when the language is unknown or highlighting throws.
 *
 * Async: the first call triggers a dynamic import of highlight.js. Subsequent
 * calls use the cached module instance.
 */
export async function highlightCode(code: string, language: string | undefined): Promise<string | null> {
  const lang = normalizeLanguage(language)
  if (!lang) return null
  const cacheKey = `${lang}::${code}`
  const cached = cacheGet(cacheKey)
  if (cached !== undefined) return cached
  try {
    const hljs = await loadHljs()
    if (!hljs.getLanguage(lang)) {
      cacheSet(cacheKey, null)
      return null
    }
    const result = hljs.highlight(code, { language: lang }).value
    cacheSet(cacheKey, result)
    return result
  } catch {
    return null
  }
}

/** Best-effort auto-highlight when no language is given; null if nothing matched. */
export async function highlightAuto(code: string): Promise<string | null> {
  const cached = cacheGet(`auto::${code}`)
  if (cached !== undefined) return cached
  try {
    const hljs = await loadHljs()
    const result = hljs.highlightAuto(code)
    cacheSet(`auto::${code}`, result.value)
    return result.value
  } catch {
    return null
  }
}

/**
 * Synchronous highlight: returns highlighted HTML string, or null if hljs
 * is not yet loaded (first render) or the language is unknown.
 *
 * After the first code block triggers `ensureHljsLoaded()`, the dynamic
 * import resolves and `hljsInstance` is set. All subsequent calls return
 * synchronously from the cache or by running hljs.highlight() — no async
 * gap, no post-render useEffect, no "plain text → highlighted" flash.
 *
 * The result is cached (same LRU as the async path) so re-renders during
 * typewriter clip are instant cache hits.
 */
export function highlightSync(code: string, language: string | undefined): string | null {
  const hljs = hljsInstance
  if (!hljs) return null
  const lang = normalizeLanguage(language)
  if (lang) {
    const cacheKey = `${lang}::${code}`
    const cached = cacheGet(cacheKey)
    if (cached !== undefined) return cached
    try {
      if (hljs.getLanguage(lang)) {
        const result = hljs.highlight(code, { language: lang }).value
        cacheSet(cacheKey, result)
        return result
      }
      // Language not registered → fall through to auto-highlight below.
      // DO NOT cache null here — auto-highlight has its own cache key.
    } catch {
      // highlight threw → fall through to auto-highlight
    }
  }
  // Auto-highlight: when no language given, or the requested language
  // is not registered (e.g. "rust" before it was added, or an obscure
  // language). This ensures ALL code blocks get syntax colors — never
  // plain white text.
  const autoKey = `auto::${code}`
  const autoCached = cacheGet(autoKey)
  if (autoCached !== undefined) return autoCached
  try {
    const result = hljs.highlightAuto(code)
    cacheSet(autoKey, result.value)
    return result.value
  } catch {
    return null
  }
}

/**
 * Returns the highlight.js load status. Used by CodeBlock to know when
 * hljs becomes available so it can re-render with highlighting.
 */
export function isHljsLoaded(): boolean {
  return hljsInstance !== null
}

// ── External store for hljs load status ──
// useSyncExternalStore lets CodeBlock re-render when hljs finishes loading
// without wrapping the entire app in a provider.
const hljsListeners = new Set<() => void>()

function notifyHljsReady() {
  hljsListeners.forEach((fn) => fn())
}

/** React hook: subscribes to hljs load status via useSyncExternalStore.
 *  Returns true once highlight.js is loaded and ready for synchronous use. */
export function useHljsReady(): boolean {
  return useSyncExternalStore(
    (callback) => {
      hljsListeners.add(callback)
      return () => { hljsListeners.delete(callback) }
    },
    () => hljsInstance !== null,
    () => false, // SSR: assume not loaded
  )
}

