/**
 * useFileContent — file content loader via the REST API.
 *
 * Uses GET /api/fs/read to load file content for the editor/preview
 * components. The file path must be relative to the agent's working
 * directory (CWD), and is joined with CWD to form an absolute path.
 *
 * State shape:
 *   - `content`  — current text (editable; FilePanel writes back via setContent)
 *   - `loading`  — true during the async load
 *   - `setContent` — imperative setter for the editor's onChange path
 *   - `imageUrl` — blob URL for image files (null for non-images)
 */
import { useCallback, useEffect, useState } from 'react'

import { isImageFile } from '@/components/file/fileTypes'
import type { WSConnection } from '@/types/ws'
import { readFile, joinPath, fetchImageBlobUrl } from '@/hooks/useFileSystem'

export interface UseFileContentResult {
  content: string
  loading: boolean
  error: string | null
  setContent: (next: string) => void
  imageUrl: string | null
}

interface UseFileContentOptions {
  filePath: string
  /** The WS connection (injected from DockviewContext for isolated roots). */
  ws: WSConnection
  /** Current working directory (injected from DockviewContext). */
  cwd: string | null
}

export function useFileContent({ filePath, ws, cwd }: UseFileContentOptions): UseFileContentResult {
  const [content, setContent] = useState('')
  const [imageUrl, setImageUrl] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    if (!filePath || !cwd) {
      setLoading(false)
      setError(null)
      return
    }

    // Resolve absolute path: join CWD with relative filePath
    const absPath = filePath.startsWith('/') ? filePath : joinPath(cwd, filePath)

    // Image files: load as blob URL instead of text
    if (isImageFile(filePath)) {
      setLoading(true)
      setError(null)
      fetchImageBlobUrl(absPath)
        .then((url) => {
          if (cancelled) return
          setImageUrl(url)
          setContent('')
          setLoading(false)
        })
        .catch((e) => {
          if (cancelled) return
          setError(e instanceof Error ? e.message : String(e))
          setContent('')
          setLoading(false)
        })
      return () => {
        cancelled = true
      }
    }

    setLoading(true)
    setError(null)
    readFile(absPath)
      .then((res) => {
        if (cancelled) return
        if (res.isBinary) {
          setError('Binary file')
          setContent('')
        } else {
          setContent(res.content)
        }
      })
      .catch((e) => {
        if (cancelled) return
        setError(e instanceof Error ? e.message : String(e))
        setContent('')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [filePath, ws, ws.connected, cwd])

  const setContentFn = useCallback((next: string) => setContent(next), [])

  return { content, loading, error, setContent: setContentFn, imageUrl }
}
