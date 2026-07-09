/**
 * useFileSystem — thin API wrappers for the backend FS endpoints (Spec §2.2).
 *
 * Four operations backed by REST:
 *   listDir(path, showHidden)  → GET /api/fs/list
 *   readFile(path)             → GET /api/fs/read
 *   searchFiles(query, path)   → GET /api/fs/search
 *   statFile(path)             → GET /api/fs/stat
 *
 * Directory listings are cached for 30s (CACHE_TTL) keyed by `path|showHidden`
 * so expanding/collapsing the tree doesn't hammer the server.
 *
 * All functions are plain async (no React state) — callers compose them into
 * hooks/components as needed. Errors are thrown, not silently swallowed.
 */

/* ── Types ──────────────────────────────────────────────────────────────── */

export interface FsEntry {
  name: string
  isDir: boolean
  size: number
  modTime: string
}

export interface FsListResult {
  entries: FsEntry[]
}

export interface FsReadResult {
  content: string
  language: string
  size: number
  isBinary: boolean
}

export interface FsSearchEntry {
  path: string
  name: string
  isDir: boolean
}

export interface FsSearchResult {
  results: FsSearchEntry[]
}

export interface FsStatResult {
  name: string
  isDir: boolean
  size: number
  modTime: string
  mode: string   // e.g. "-rw-r--r--" from os.FileMode.String()
}

/* ── Cache ───────────────────────────────────────────────────────────────── */

const CACHE_TTL = 30_000 // 30 seconds

interface CacheEntry {
  entries: FsEntry[]
  timestamp: number
}

const listCache = new Map<string, CacheEntry>()

function cacheKey(path: string, showHidden: boolean): string {
  return `${path}|${showHidden}`
}

/** Invalidate all cached directory listings (e.g. after CWD change). */
export function invalidateFsCache(): void {
  listCache.clear()
}

/* ── API functions ────────────────────────────────────────────────────────── */

export async function listDir(
  path: string,
  showHidden = false,
  signal?: AbortSignal,
): Promise<FsEntry[]> {
  const key = cacheKey(path, showHidden)
  const cached = listCache.get(key)
  if (cached && Date.now() - cached.timestamp < CACHE_TTL) {
    return cached.entries
  }

  const params = new URLSearchParams({
    path,
    showHidden: String(showHidden),
  })
  const res = await fetch(`/api/fs/list?${params}`, { signal })
  if (!res.ok) {
    throw new Error(`fs/list failed: ${res.status} ${res.statusText}`)
  }
  const data = (await res.json()) as FsListResult
  const entries = data.entries || []
  // Sort: directories first, then alphabetical.
  entries.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
  listCache.set(key, { entries, timestamp: Date.now() })
  return entries
}

export async function readFile(path: string, signal?: AbortSignal): Promise<FsReadResult> {
  const params = new URLSearchParams({ path })
  const res = await fetch(`/api/fs/read?${params}`, { signal })
  if (!res.ok) {
    throw new Error(`fs/read failed: ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as FsReadResult
}

export async function searchFiles(
  query: string,
  path: string,
  limit = 50,
  signal?: AbortSignal,
): Promise<FsSearchEntry[]> {
  const params = new URLSearchParams({ q: query, path, limit: String(limit) })
  const res = await fetch(`/api/fs/search?${params}`, { signal })
  if (!res.ok) {
    throw new Error(`fs/search failed: ${res.status} ${res.statusText}`)
  }
  const data = (await res.json()) as FsSearchResult
  return data.results || []
}

export async function statFile(path: string, signal?: AbortSignal): Promise<FsStatResult> {
  const params = new URLSearchParams({ path })
  const res = await fetch(`/api/fs/stat?${params}`, { signal })
  if (!res.ok) {
    throw new Error(`fs/stat failed: ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as FsStatResult
}

/* ── Utility ────────────────────────────────────────────────────────────────── */

/** Join path segments safely (handles trailing slashes). */
export function joinPath(base: string, name: string): string {
  if (base.endsWith('/')) return `${base}${name}`
  return `${base}/${name}`
}

/** Get the parent directory of a path. Returns '/' for root. */
export function parentPath(path: string): string {
  const trimmed = path.replace(/\/+$/, '')
  const idx = trimmed.lastIndexOf('/')
  if (idx <= 0) return '/'
  return trimmed.slice(0, idx)
}

/** Fetch an image file as a blob URL (for ImagePreview). */
export async function fetchImageBlobUrl(path: string): Promise<string> {
  const params = new URLSearchParams({ path })
  const res = await fetch(`/api/fs/read?${params}`)
  if (!res.ok) throw new Error(`fs/read failed: ${res.status}`)
  const blob = await res.blob()
  return URL.createObjectURL(blob)
}
