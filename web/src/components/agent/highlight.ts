/**
 * Code highlighting helper for the Markdown renderer (Spec 4).
 *
 * Uses highlight.js with a curated subset of common languages registered once
 * at module load (KISS — avoids bundling the full 190-language auto-detect
 * weight while covering what an agent actually emits). Falls back to plain text
 * when the language is unknown or highlighting throws.
 */
import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import go from 'highlight.js/lib/languages/go'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import markdown from 'highlight.js/lib/languages/markdown'
import python from 'highlight.js/lib/languages/python'
import shell from 'highlight.js/lib/languages/shell'
import sql from 'highlight.js/lib/languages/sql'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

let registered = false
function ensureRegistered(): void {
  if (registered) return
  hljs.registerLanguage('bash', bash)
  hljs.registerLanguage('sh', shell)
  hljs.registerLanguage('go', go)
  hljs.registerLanguage('javascript', javascript)
  hljs.registerLanguage('js', javascript)
  hljs.registerLanguage('json', json)
  hljs.registerLanguage('markdown', markdown)
  hljs.registerLanguage('python', python)
  hljs.registerLanguage('py', python)
  hljs.registerLanguage('shell', shell)
  hljs.registerLanguage('sql', sql)
  hljs.registerLanguage('typescript', typescript)
  hljs.registerLanguage('ts', typescript)
  hljs.registerLanguage('xml', xml)
  hljs.registerLanguage('html', xml)
  hljs.registerLanguage('yaml', yaml)
  hljs.registerLanguage('yml', yaml)
  // Register a few common aliases so fenced blocks with minor name variants resolve.
  hljs.registerAliases(['go'], { languageName: 'go' })
  registered = true
}

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
    // Move to end (most-recently-used) by re-inserting.
    hlCache.delete(key)
    hlCache.set(key, val)
  }
  return val
}

function cacheSet(key: string, value: string | null): void {
  if (hlCache.size >= CACHE_LIMIT) {
    // Evict the oldest entry (first in insertion order).
    const oldest = hlCache.keys().next().value
    if (oldest !== undefined) hlCache.delete(oldest)
  }
  hlCache.set(key, value)
}

/**
 * Highlight `code` for `language`, returning an HTML string of <span> tokens.
 * Returns null when the language is unknown so the caller can render plain text.
 */
export function highlightCode(code: string, language: string | undefined): string | null {
  const lang = normalizeLanguage(language)
  if (!lang) return null
  const cacheKey = `${lang}::${code}`
  const cached = cacheGet(cacheKey)
  if (cached !== undefined) return cached
  try {
    ensureRegistered()
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
export function highlightAuto(code: string): string | null {
  const cached = cacheGet(`auto::${code}`)
  if (cached !== undefined) return cached
  try {
    ensureRegistered()
    const result = hljs.highlightAuto(code)
    // highlightAuto always returns something; only treat as "no language" if no relevance.
    cacheSet(`auto::${code}`, result.value)
    return result.value
  } catch {
    return null
  }
}
