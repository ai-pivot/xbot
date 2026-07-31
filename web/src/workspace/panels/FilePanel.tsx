/**
 * FilePanel — file editor/preview panel (Spec 5).
 *
 * Decides how a file renders from its name:
 *
 *   - Markdown (.md/.markdown) → default preview, toggle to editor.
 *   - Image (.png/.jpg/.gif/.webp/.svg) → image preview, no toggle.
 *   - Everything else → Monaco editor, no toggle (only markdown is previewable).
 *
 * Content is loaded via the `read_file` WS RPC (through useFileContent).
 * Edits live in component state and are not persisted.
 */
import { useEffect, useMemo, useState } from 'react'
import { Loader2 } from 'lucide-react'

import { MonacoEditor } from '@/components/file/MonacoEditor'
import { MarkdownPreview } from '@/components/file/MarkdownPreview'
import { ImagePreview } from '@/components/file/ImagePreview'
import { FileToolbar } from '@/components/file/FileToolbar'
import {
  canTogglePreview,
  defaultViewMode,
  isImageFile,
  isHtmlFile,
  languageOf,
  type FileViewMode,
} from '@/components/file/fileTypes'
import { useFileContent } from '@/hooks/useFileContent'
import { joinPath, parentPath } from '@/hooks/useFileSystem'
import { useI18n } from '@/providers/i18n'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'

/** "basename" of a posix path, defensive against undefined. */
function baseName(filePath?: string): string {
  if (!filePath) return 'untitled'
  const parts = filePath.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? filePath
}

export function FilePanel({ params }: PanelProps) {
  const { ws, cwd } = useDockviewContext()
  const filePath = params.filePath ?? ''
  const fileName = useMemo(() => baseName(filePath), [filePath])
  const isImage = isImageFile(fileName)
  const canToggle = canTogglePreview(fileName)
  const language = useMemo(() => languageOf(fileName), [fileName])

  const { content, loading, error, setContent, imageUrl } = useFileContent({ filePath, ws, cwd: cwd.cwd })
  const [mode, setMode] = useState<FileViewMode>(() => defaultViewMode(fileName))

  // Directory of the markdown file — used to resolve relative image paths.
  const baseDir = useMemo(() => {
    if (!filePath) return undefined
    const absPath = filePath.startsWith('/') ? filePath : (cwd.cwd ? joinPath(cwd.cwd, filePath) : filePath)
    return parentPath(absPath)
  }, [filePath, cwd.cwd])

  // Re-seed the view mode if the file ever changes (dockview reuses a panel
  // instance when its params update). Image files ignore `mode` entirely.
  useEffect(() => {
    setMode(defaultViewMode(fileName))
  }, [fileName])

  // Image files are preview-only and have no text content.
  if (isImage) {
    return (
      <div className="flex h-full flex-col bg-bg-primary">
        <FileToolbar fileName={fileName} mode="preview" canToggle={false} />
        {loading ? (
          <PanelLoading />
        ) : imageUrl ? (
          <ImagePreview src={imageUrl} fileName={fileName} className="flex-1" />
        ) : (
          <div className="flex h-full items-center justify-center text-sm text-text-secondary">
            {fileName}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col bg-bg-primary">
      <FileToolbar
        fileName={fileName}
        mode={mode}
        onModeChange={canToggle ? setMode : undefined}
        canToggle={canToggle}
      />
      <div className="min-h-0 flex-1">
        {loading ? (
          <PanelLoading />
        ) : error ? (
          <div className="flex h-full items-center justify-center px-6 text-center text-sm text-text-secondary">
            {error}
          </div>
        ) : canToggle && mode === 'preview' ? (
          isHtmlFile(fileName) ? (
            <iframe
              srcDoc={content}
              className="h-full w-full border-0"
              title={fileName}
              sandbox="allow-scripts"
            />
          ) : (
            <MarkdownPreview source={content} baseDir={baseDir} />
          )
        ) : (
          <MonacoEditor value={content} language={language} onChange={setContent} />
        )}
      </div>
    </div>
  )
}

function PanelLoading() {
  const { t } = useI18n()
  return (
    <div className="flex h-full items-center justify-center gap-2 text-text-secondary">
      <Loader2 className="size-4 animate-spin" />
      <span className="text-sm">{t('common.loading')}</span>
    </div>
  )
}
