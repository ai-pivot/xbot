import { backendUrl, getPreviewToken, previewEnabled } from './previewConnection'

/** Only canonical, owned API references are eligible for bearer authorization. */
export function isProtectedPreviewResource(src: string): boolean {
  return previewEnabled() && src.startsWith('/api/') && !src.includes('\\')
}

export const PREVIEW_RESOURCE_MAX_BYTES = 32 * 1024 * 1024

/** No redirects: even signed object-store redirects fail closed rather than risking credentials. */
export async function fetchPreviewResource(src: string, signal: AbortSignal): Promise<Blob> {
  if (!isProtectedPreviewResource(src)) throw new Error('Not an owned preview resource')
  const token = getPreviewToken()
  if (!token) throw new Error('Preview authorization is required')
  const url = backendUrl(src)
  // URL normalization must not allow traversal out of the protected API prefix.
  const apiPrefix = new URL(backendUrl('/api/')).pathname
  if (!new URL(url).pathname.startsWith(apiPrefix)) throw new Error('Invalid resource path')
  const response = await fetch(url, {
    headers: { Authorization: `Bearer ${token}` }, credentials: 'omit', redirect: 'error', signal,
  })
  if (!response.ok || response.redirected || response.type === 'opaqueredirect') {
    await response.body?.cancel()
    throw new Error(`Resource unavailable (${response.status}); redirects are not supported`)
  }
  const tooLarge = () => new Error('Resource exceeds the 32 MiB preview limit. Download it from the backend application instead.')
  if (Number(response.headers.get('Content-Length')) > PREVIEW_RESOURCE_MAX_BYTES) {
    await response.body?.cancel()
    throw tooLarge()
  }
  // Stream with a hard budget, including responses with missing/incorrect length headers.
  if (!response.body) throw new Error('Resource response has no readable body')
  const reader = response.body.getReader()
  const chunks: Uint8Array<ArrayBuffer>[] = []
  let size = 0
  try {
    while (true) {
      signal.throwIfAborted()
      const { done, value } = await reader.read()
      if (done) break
      size += value.byteLength
      if (size > PREVIEW_RESOURCE_MAX_BYTES) throw tooLarge()
      chunks.push(new Uint8Array(value))
    }
    signal.throwIfAborted()
    return new Blob(chunks, { type: response.headers.get('Content-Type') || 'application/octet-stream' })
  } catch (error) {
    await reader.cancel().catch(() => {})
    throw error
  } finally {
    reader.releaseLock()
  }
}
