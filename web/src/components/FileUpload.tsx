import { useRef, useState } from 'react'
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

export function uploadFile(file: File): Promise<PendingFile & { ok: boolean; error?: string }> {
  return new Promise((resolve) => {
    if (file.size > MAX_FILE_SIZE) {
      resolve({ ok: false, id: '', name: file.name, size: file.size, error: '__FILE_TOO_LARGE__' })
      return
    }

    const formData = new FormData()
    formData.append('file', file)

    fetch('/api/files/upload', { method: 'POST', body: formData })
      .then((r) => r.json())
      .then((data) => {
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
      })
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : String(err)
        resolve({ ok: false, id: '', name: file.name, size: file.size, error: msg })
      })
  })
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
      {uploading ? '⏳' : '📎'}
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
