/**
 * useFileSystem — thin API wrappers for the backend FS endpoints (Spec §2.2).
 *
 * Four operations backed by REST:
 *   listDir(path, showHidden)  → POST /api/fs/list
 *   readFile(path)             → POST /api/fs/read
 *   searchFiles(query, path)   → POST /api/fs/search
 *   statFile(path)             → parent listing lookup
 *
 * Directory listings are cached for 30s (CACHE_TTL) keyed by `path|showHidden`
 * so expanding/collapsing the tree doesn't hammer the server.
 *
 * All functions are plain async (no React state) — callers compose them into
 * hooks/components as needed. Errors are thrown, not silently swallowed.
 */

import { postAPI, postRawAPI } from '@/lib/api'

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

  const data = await postAPI<FsListResult>('/api/fs/list', {
    path,
    show_hidden: showHidden,
  }, { signal })
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
  return postAPI<FsReadResult>('/api/fs/read', { path }, { signal })
}

export async function searchFiles(
  query: string,
  path: string,
  limit = 50,
  signal?: AbortSignal,
): Promise<FsSearchEntry[]> {
  const data = await postAPI<FsSearchResult>('/api/fs/search', {
    query,
    path,
    limit,
  }, { signal })
  return data.results || []
}

export async function statFile(path: string, signal?: AbortSignal): Promise<FsStatResult> {
  const clean = path.replace(/\/+$/, '') || '/'
  if (clean === '/') return { name: '/', isDir: true, size: 0, modTime: '', mode: 'drwxr-xr-x' }
  const name = clean.slice(clean.lastIndexOf('/') + 1)
  const entry = (await listDir(parentPath(clean), true, signal)).find((item) => item.name === name)
  if (!entry) throw new Error(`path not found: ${path}`)
  return { ...entry, mode: '' }
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
  const res = await postRawAPI('/api/fs/read', { path, raw: true })
  const blob = await res.blob()
  return URL.createObjectURL(blob)
}
