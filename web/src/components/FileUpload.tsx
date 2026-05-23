import { useRef, useState, useCallback, useReducer } from 'react'
import { useTranslation, type I18nKey } from '../i18n'
import type { ShowToastFn } from './settings/shared'

export interface PendingFile {
  id: string
  name: string
  size: number
  mime?: string
  uploadKey?: string
  isOSS?: boolean
}

const MAX_FILE_SIZE = 10 * 1024 * 1024 // 10MB

// ---------------------------------------------------------------------------
// File type icon mapping
// ---------------------------------------------------------------------------

export function getFileIcon(mime?: string, name?: string): string {
  if (mime) {
    if (mime.startsWith('image/')) return '🖼️'
    if (mime.startsWith('video/')) return '🎬'
    if (mime.startsWith('audio/')) return '🎵'
    if (mime === 'application/pdf') return '📄'
    if (mime === 'application/zip' || mime === 'application/x-zip-compressed') return '📦'
    if (mime.includes('word') || mime.includes('document')) return '📝'
    if (mime.includes('spreadsheet') || mime.includes('excel')) return '📊'
    if (mime.includes('presentation') || mime.includes('powerpoint')) return '📽️'
    if (mime.includes('json') || mime.includes('xml') || mime.includes('javascript') || mime.includes('typescript')) return '💻'
    if (mime.startsWith('text/')) return '📃'
  }
  // Fallback: guess from file extension
  const ext = name?.split('.').pop()?.toLowerCase()
  switch (ext) {
    case 'png': case 'jpg': case 'jpeg': case 'gif': case 'svg': case 'webp': case 'bmp': return '🖼️'
    case 'mp4': case 'avi': case 'mov': case 'mkv': case 'webm': return '🎬'
    case 'mp3': case 'wav': case 'flac': case 'ogg': case 'aac': return '🎵'
    case 'pdf': return '📄'
    case 'zip': case 'rar': case '7z': case 'tar': case 'gz': return '📦'
    case 'doc': case 'docx': return '📝'
    case 'xls': case 'xlsx': return '📊'
    case 'ppt': case 'pptx': return '📽️'
    case 'js': case 'ts': case 'jsx': case 'tsx': case 'py': case 'go': case 'rs': case 'java': case 'c': case 'cpp': case 'h': return '💻'
    case 'json': case 'xml': case 'yaml': case 'yml': case 'toml': return '💻'
    case 'md': case 'txt': case 'csv': case 'log': return '📃'
    default: return '📎'
  }
}

// ---------------------------------------------------------------------------
// Format file size
// ---------------------------------------------------------------------------

export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// ---------------------------------------------------------------------------
// XHR-based upload with progress
// ---------------------------------------------------------------------------

export function uploadFileWithProgress(
  file: File,
  onProgress?: (percent: number) => void,
): Promise<PendingFile & { ok: boolean; error?: string }> {
  return new Promise((resolve) => {
    if (file.size > MAX_FILE_SIZE) {
      resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__FILE_TOO_LARGE__' })
      return
    }

    const formData = new FormData()
    formData.append('file', file)

    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/files/upload')

    xhr.upload.addEventListener('progress', (e) => {
      if (e.lengthComputable && onProgress) {
        onProgress(Math.round((e.loaded / e.total) * 100))
      }
    })

    xhr.addEventListener('load', () => {
      try {
        const data = JSON.parse(xhr.responseText)
        if (!data.ok) {
          resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__UPLOAD_FAILED__' })
          return
        }
        if (data.upload_key) {
          resolve({
            ok: true,
            id: data.upload_key,
            name: data.name || file.name,
            size: data.size || file.size,
            mime: data.mime,
            uploadKey: data.upload_key,
            isOSS: true,
          })
        } else if (data.file_id) {
          resolve({
            ok: true,
            id: data.file_id,
            name: data.name || file.name,
            size: data.size || file.size,
            mime: data.mime,
          })
        } else {
          resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__UPLOAD_RESPONSE_ERROR__' })
        }
      } catch {
        resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__UPLOAD_RESPONSE_ERROR__' })
      }
    })

    xhr.addEventListener('error', () => {
      resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__UPLOAD_FAILED__' })
    })

    xhr.addEventListener('abort', () => {
      resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__UPLOAD_FAILED__' })
    })

    xhr.send(formData)
  })
}

/**
 * uploadFile — backward-compatible wrapper (no progress callback).
 * Internally delegates to uploadFileWithProgress.
 */
export function uploadFile(file: File): Promise<PendingFile & { ok: boolean; error?: string }> {
  return uploadFileWithProgress(file)
}

/** Map internal error codes to i18n keys */
const ERROR_KEY_MAP: Record<string, I18nKey> = {
  '__FILE_TOO_LARGE__': 'fileTooLarge',
  '__UPLOAD_FAILED__': 'uploadFailed',
  '__UPLOAD_RESPONSE_ERROR__': 'uploadResponseError',
}

/** Resolve an upload error to a user-facing i18n string */
export function resolveUploadError(error: string, t: (key: I18nKey) => string): string {
  const key = ERROR_KEY_MAP[error]
  return key ? t(key) : error
}

// ---------------------------------------------------------------------------
// Upload queue hook
// ---------------------------------------------------------------------------

export type UploadQueueItemStatus = 'pending' | 'uploading' | 'done' | 'error'

export interface UploadQueueItem {
  id: string          // unique queue item id (crypto.randomUUID)
  file: File
  status: UploadQueueItemStatus
  progress: number     // 0–100
  result?: PendingFile & { ok: boolean; error?: string }
}

type QueueAction =
  | { type: 'ADD'; files: File[] }
  | { type: 'UPDATE'; id: string; status: UploadQueueItemStatus; progress?: number; result?: PendingFile & { ok: boolean; error?: string } }
  | { type: 'REMOVE'; id: string }
  | { type: 'MOVE'; id: string; direction: 'up' | 'down' }
  | { type: 'RESET_FOR_RETRY'; id: string }

function queueReducer(state: UploadQueueItem[], action: QueueAction): UploadQueueItem[] {
  switch (action.type) {
    case 'ADD': {
      const newItems: UploadQueueItem[] = action.files.map((f) => ({
        id: crypto.randomUUID(),
        file: f,
        status: 'pending' as UploadQueueItemStatus,
        progress: 0,
      }))
      return [...state, ...newItems]
    }
    case 'UPDATE':
      return state.map((item) =>
        item.id === action.id
          ? { ...item, status: action.status, ...(action.progress !== undefined ? { progress: action.progress } : {}), ...(action.result !== undefined ? { result: action.result } : {}) }
          : item,
      )
    case 'REMOVE':
      return state.filter((item) => item.id !== action.id)
    case 'MOVE': {
      const idx = state.findIndex((item) => item.id === action.id)
      if (idx === -1) return state
      const target = action.direction === 'up' ? idx - 1 : idx + 1
      if (target < 0 || target >= state.length) return state
      const copy = [...state]
      ;[copy[idx], copy[target]] = [copy[target], copy[idx]]
      return copy
    }
    case 'RESET_FOR_RETRY':
      return state.map((item) =>
        item.id === action.id
          ? { ...item, status: 'pending', progress: 0, result: undefined }
          : item,
      )
    default:
      return state
  }
}

export interface UseUploadQueueReturn {
  queue: UploadQueueItem[]
  addToQueue: (files: File[]) => void
  removeItem: (id: string) => void
  retryItem: (id: string) => void
  moveItem: (id: string, direction: 'up' | 'down') => void
  /** Process all pending items. Returns completed items (ok only). */
  processQueue: () => Promise<PendingFile[]>
  hasPending: boolean
}

export function useUploadQueue(): UseUploadQueueReturn {
  const [queue, dispatch] = useReducer(queueReducer, [])

  const addToQueue = useCallback((files: File[]) => {
    dispatch({ type: 'ADD', files })
  }, [])

  const removeItem = useCallback((id: string) => {
    dispatch({ type: 'REMOVE', id })
  }, [])

  const retryItem = useCallback((id: string) => {
    dispatch({ type: 'RESET_FOR_RETRY', id })
  }, [])

  const moveItem = useCallback((id: string, direction: 'up' | 'down') => {
    dispatch({ type: 'MOVE', id, direction })
  }, [])

  const processQueue = useCallback(async (): Promise<PendingFile[]> => {
    const pending = queue.filter((item) => item.status === 'pending')
    const completed: PendingFile[] = []

    for (const item of pending) {
      dispatch({ type: 'UPDATE', id: item.id, status: 'uploading', progress: 0 })

      const result = await uploadFileWithProgress(item.file, (percent) => {
        dispatch({ type: 'UPDATE', id: item.id, status: 'uploading', progress: percent })
      })

      if (result.ok) {
        dispatch({ type: 'UPDATE', id: item.id, status: 'done', progress: 100, result })
        completed.push({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
      } else {
        dispatch({ type: 'UPDATE', id: item.id, status: 'error', result })
      }
    }

    return completed
  }, [queue])

  const hasPending = queue.some((item) => item.status === 'pending')

  return { queue, addToQueue, removeItem, retryItem, moveItem, processQueue, hasPending }
}

// ---------------------------------------------------------------------------
// Hook to handle files from paste events (unchanged interface)
// ---------------------------------------------------------------------------

// Hook to handle files from paste events
export function usePasteUpload(
  onUpload: (file: PendingFile) => void,
  disabled: boolean,
  showToast: ShowToastFn,
) {
  const handlePaste = async (e: React.ClipboardEvent | ClipboardEvent) => {
    if (disabled) return
    const clipboardEvent = e as ClipboardEvent
    const files = clipboardEvent.clipboardData?.files
    if (!files || files.length === 0) return

    // Only handle image pastes — let text pastes through
    const imageFile = Array.from(files).find((f) => f.type.startsWith('image/'))
    if (!imageFile) return

    e.preventDefault()
    const result = await uploadFile(imageFile)
    if (result.ok) {
      onUpload({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
    } else {
      // Error codes will be translated at call site via showToast
      showToast(result.error || 'Upload failed', 'error')
    }
  }
  return handlePaste
}

// ---------------------------------------------------------------------------
// FileUpload component (unchanged props interface)
// ---------------------------------------------------------------------------

interface FileUploadProps {
  onUpload: (file: PendingFile) => void
  disabled: boolean
  showToast: ShowToastFn
}

export default function FileUpload({ onUpload, disabled, showToast }: FileUploadProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const { t } = useTranslation()

  const handleFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return
    setUploading(true)

    try {
      for (const file of Array.from(files)) {
        const result = await uploadFile(file)
        if (result.ok) {
          onUpload({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
        } else {
          showToast(resolveUploadError(result.error || "", t), 'error')
        }
      }
    } finally {
      setUploading(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  return (
    <button
      className="file-upload-btn"
      data-testid="file-upload-btn"
      onClick={() => inputRef.current?.click()}
      disabled={disabled || uploading}
      title={t('uploadFile')}
    >
      {uploading ? (
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
      ) : (
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M12 5v14M5 12h14"/></svg>
      )}
      <input
        ref={inputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => handleFiles(e.target.files)}
      />
    </button>
  )
}
