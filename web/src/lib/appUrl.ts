import { backendUrl, getPreviewToken, previewEnabled } from './previewConnection'

/** Root-relative xbot paths use Vite's build base, including feature previews. */
export function appPath(path: string): string {
  return `${import.meta.env.BASE_URL}${path.slice(1)}`
}

/** Only xbot-owned URLs are rebased; user links and external media stay intact. */
export function appResourceUrl(url: string): string {
  if (/^\/(api|plugins)\//.test(url)) return backendUrl(url)
  return url.startsWith('/s/') ? appPath(url) : url
}

/** Subpath previews never install or update the production root worker. */
export function pwaEnabled(): boolean {
  return __PWA_ENABLED__ && import.meta.env.BASE_URL === '/'
}

/** Only stream transports may carry the short-lived preview token in a URL. */
export function backendStreamUrl(path: string): string {
  if (!/^\/(api\/sse|ws\/terminal)(?:\?|$)/.test(path)) throw new Error('invalid-stream-path')
  const url = backendUrl(path)
  if (!previewEnabled()) return url
  const token = getPreviewToken()
  if (!token) throw new Error('preview-not-connected')
  return `${url}${url.includes('?') ? '&' : '?'}preview_token=${encodeURIComponent(token)}`
}
