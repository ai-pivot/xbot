/** Backend transport configuration. Frontend navigation must keep using appPath. */
export const DEFAULT_PREVIEW_BACKEND = 'http://localhost:8082'
export const PREVIEW_BACKEND_STORAGE_KEY = 'xbot-preview-backend'
export const PREVIEW_TEST_TIMEOUT_MS = 8_000

let activeConnection: { base: string; token: string } | null = null

/** Called only after the gate verifies explicit, short-lived authorization. */
export function activatePreviewConnection(base: string, token: string): void {
  if (!token.trim() || /[\r\n]/.test(token)) throw new Error('invalid-preview-token')
  activeConnection = { base: normalizeBackendUrl(base), token: token.trim() }
}

export function getPreviewToken(): string {
  return activeConnection?.token ?? ''
}

/** Best effort only; server expiry remains the security boundary on unload. */
export function revokePreviewConnection(): void {
  if (!activeConnection) return
  const { base, token } = activeConnection
  try {
    void fetch(backendUrl('/api/preview/revoke', base), {
      method: 'POST', body: '{}', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      credentials: 'omit', mode: 'cors', redirect: 'error', keepalive: true,
    }).catch(() => {})
  } catch { /* Browser teardown or unavailable fetch cannot prevent reset. */ }
}

export function clearPreviewConnection(): void {
  activeConnection = null
}

export function getPreviewBackendUrl(): string {
  return activeConnection?.base ?? readPreviewBackend()
}

export function previewEnabled(): boolean {
  return typeof __PREVIEW_ENABLED__ !== 'undefined' && __PREVIEW_ENABLED__
}

/** A backend subpath is a prefix: https://host/xbot + /api → /xbot/api. */
export function normalizeBackendUrl(input: string): string {
  const value = input.trim()
  // Reject even empty ?/# delimiters, which URL.search/hash otherwise hide.
  if (!/^https?:\/\//i.test(value) || /[?#\\\s]/.test(value)) throw new Error('invalid-backend-url')
  const url = new URL(value)
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || /https?:\/\/[^/]*@/i.test(value)) {
    throw new Error('invalid-backend-url')
  }
  return `${url.origin}${url.pathname.replace(/\/+$/, '')}`
}

/** Storage may throw even on access (sandbox/private browsing/security policy). */
export function readPreviewBackend(): string {
  try {
    const saved = localStorage.getItem(PREVIEW_BACKEND_STORAGE_KEY)
    if (saved) return normalizeBackendUrl(saved)
  } catch { /* Invalid or unavailable storage: keep an actual editable default. */ }
  return DEFAULT_PREVIEW_BACKEND
}

/** Returns false when persistence is unavailable; validation errors still throw. */
export function savePreviewBackend(input: string): boolean {
  const address = normalizeBackendUrl(input)
  try {
    localStorage.setItem(PREVIEW_BACKEND_STORAGE_KEY, address)
    return true
  } catch {
    return false
  }
}

export function clearPreviewBackend(): boolean {
  try {
    localStorage.removeItem(PREVIEW_BACKEND_STORAGE_KEY)
    return true
  } catch {
    return false
  }
}

/** Transport only, never use for router links. No auth token is stored here. */
export function getBackendBaseUrl(): string {
  return previewEnabled() ? getPreviewBackendUrl() : import.meta.env.BASE_URL.replace(/\/$/, '')
}

/** Accept only owned root-relative paths, not arbitrary external/user URLs. */
export function backendUrl(path: string, base = getBackendBaseUrl()): string {
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('\\')) throw new Error('invalid-backend-path')
  return `${base}${path}`
}

export function backendWebSocketUrl(path: string): string {
  const url = new URL(backendUrl(path), window.location.origin)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

export class PreviewConnectionError extends Error {
  readonly reason: 'timeout' | 'network' | 'http' | 'response'
  readonly status?: number
  constructor(reason: 'timeout' | 'network' | 'http' | 'response', status?: number) {
    super(reason)
    this.reason = reason
    this.status = status
  }
}

/**
 * Reachability only: public auth config is the existing non-model endpoint.
 * Omit credentials and refuse redirects; success does NOT authorize the app.
 * Race cancellation explicitly so a stalled/mocked fetch cannot leave UI busy.
 */
export async function testPreviewConnection(input: string, signal?: AbortSignal, timeoutMs = PREVIEW_TEST_TIMEOUT_MS): Promise<void> {
  await probeConnection(input, signal, timeoutMs)
}

/** Explicit token verification, not activation; caller still requires danger consent. */
export async function verifyPreviewToken(input: string, token: string, signal?: AbortSignal, timeoutMs = PREVIEW_TEST_TIMEOUT_MS): Promise<string> {
  if (!token.trim() || /[\r\n]/.test(token)) throw new Error('invalid-preview-token')
  const expiry = await probeConnection(input, signal, timeoutMs, token.trim())
  return expiry!
}

async function probeConnection(input: string, signal: AbortSignal | undefined, timeoutMs: number, token?: string): Promise<string | undefined> {
  const base = normalizeBackendUrl(input)
  const controller = new AbortController()
  let timedOut = false
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, { once: true })
  if (signal?.aborted) abort()
  const timer = setTimeout(() => { timedOut = true; abort() }, timeoutMs)
  let onAbort: (() => void) | undefined
  const cancelled = new Promise<never>((_, reject) => {
    onAbort = () => reject(new DOMException('Aborted', 'AbortError'))
    controller.signal.addEventListener('abort', onAbort, { once: true })
    if (controller.signal.aborted) onAbort()
  })
  try {
    return await Promise.race([
      (async () => {
        const response = await fetch(backendUrl(token ? '/api/preview/session' : '/api/auth/config', base), {
          method: 'POST', body: '{}', headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
          credentials: 'omit', mode: 'cors', cache: 'no-store', redirect: 'error', signal: controller.signal,
        })
        if (!response.ok) throw new PreviewConnectionError('http', response.status)
        const body: unknown = await response.json().catch(() => { throw new PreviewConnectionError('response') })
        if (token) {
          if (!body || typeof body !== 'object' || !('ok' in body) || body.ok !== true || !('data' in body) || !body.data || typeof body.data !== 'object') throw new PreviewConnectionError('response')
          const data = body.data
          if (!('origin' in data) || data.origin !== window.location.origin || !('expires_at' in data) || typeof data.expires_at !== 'string' || !(Date.parse(data.expires_at) > Date.now())) throw new PreviewConnectionError('response')
          return data.expires_at
        }
        if (!body || typeof body !== 'object' || !('ok' in body) || body.ok !== true || !('data' in body) || !body.data || typeof body.data !== 'object' || !('invite_only' in body.data) || typeof body.data.invite_only !== 'boolean') {
          throw new PreviewConnectionError('response')
        }
      })(),
      cancelled,
    ])
  } catch (error) {
    if (timedOut) throw new PreviewConnectionError('timeout')
    if (controller.signal.aborted) throw new DOMException('Aborted', 'AbortError')
    if (error instanceof PreviewConnectionError) throw error
    throw new PreviewConnectionError('network')
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener('abort', abort)
    if (onAbort) controller.signal.removeEventListener('abort', onAbort)
  }
}
