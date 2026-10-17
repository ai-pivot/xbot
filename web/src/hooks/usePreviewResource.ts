import { useEffect, useState } from 'react'
import { appResourceUrl } from '@/lib/appUrl'
import { getPreviewBackendUrl, getPreviewToken } from '@/lib/previewConnection'
import { fetchPreviewResource, isProtectedPreviewResource } from '@/lib/previewResource'

/** Never expose a protected backend URL to an element, including the first render. */
export function usePreviewResource(original: string, enabled = true) {
  const protectedResource = isProtectedPreviewResource(original)
  const key = protectedResource ? `${getPreviewBackendUrl()}\n${getPreviewToken()}\n${original}` : original
  const [result, setResult] = useState<{ key: string; src?: string; error?: string }>()
  useEffect(() => {
    if (!protectedResource || !enabled) return
    const controller = new AbortController()
    let objectUrl: string | undefined
    setResult(undefined)
    void fetchPreviewResource(original, controller.signal).then(blob => {
      if (controller.signal.aborted) return
      objectUrl = URL.createObjectURL(blob)
      setResult({ key, src: objectUrl })
    }).catch(error => {
      if (!controller.signal.aborted) setResult({ key, error: error instanceof Error ? error.message : 'Resource fetch failed' })
    })
    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [original, key, protectedResource, enabled])
  const current = result?.key === key ? result : undefined
  return {
    src: protectedResource ? (enabled ? current?.src : undefined) : appResourceUrl(original),
    error: enabled ? current?.error : undefined,
    loading: protectedResource && enabled && !current,
    protectedResource,
  }
}

/** Downloads are opt-in and bounded; a second click saves the prepared Blob. */
export function usePreviewResourceLink(original: string) {
  const [requested, setRequested] = useState(false)
  const resource = usePreviewResource(original, requested)
  return { ...resource, prepare: () => setRequested(true) }
}
