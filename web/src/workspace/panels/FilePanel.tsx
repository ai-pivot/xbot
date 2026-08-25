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
 *
 * 插件控制：params.editorId 存在时挂载 EditorController 到 editorRegistry
 * （plugin-runtime/editorRegistry.ts）——ctx.ui.openFileTab 返回的 EditorHandle
 * 的跳行/高亮/选区/语言/内容/视图方法都路由到这里。
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import type * as monacoNs from 'monaco-editor'
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
import { attachEditor } from '@/plugin-runtime/editorRegistry'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'

/** "basename" of a posix path, defensive against undefined. */
function baseName(filePath?: string): string {
  if (!filePath) return 'untitled'
  const parts = filePath.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? filePath
}

export function FilePanel({ params, api }: PanelProps) {
  const { ws, cwd } = useDockviewContext()
  const filePath = params.filePath ?? ''
  const fileName = useMemo(() => baseName(filePath), [filePath])
  const isImage = isImageFile(fileName)
  const canToggle = canTogglePreview(fileName)

  // 插件可覆盖语言（params.fileLanguage / handle.setLanguage）。
  const [languageOverride, setLanguageOverride] = useState(params.fileLanguage)
  const language = languageOverride ?? languageOf(fileName)

  const { content, loading, error, setContent, imageUrl } = useFileContent({ filePath, ws, cwd: cwd.cwd })
  const [mode, setMode] = useState<FileViewMode>(
    () => params.fileViewMode ?? defaultViewMode(fileName),
  )

  // Monaco 实例 + 插件高亮 collection（EditorController 的执行基础）。
  // monaco 命名空间从 onEditorMount 回调取得（顶层仅类型导入，避免把完整
  // monaco bundle 拉进测试环境）。
  const editorRef = useRef<monacoNs.editor.IStandaloneCodeEditor | null>(null)
  const monacoRef = useRef<typeof monacoNs | null>(null)
  const decorationsRef = useRef<monacoNs.editor.IEditorDecorationsCollection | null>(null)
  const initialAppliedRef = useRef<string | null>(null)

  // Directory of the markdown file — used to resolve relative image paths.
  const baseDir = useMemo(() => {
    if (!filePath) return undefined
    // Embedded skill paths ("embedded:xxx/file") are virtual — pass through as-is.
    if (filePath.startsWith('embedded:')) {
      const idx = filePath.lastIndexOf('/')
      return idx > 0 ? filePath.slice(0, idx) : filePath
    }
    const absPath = filePath.startsWith('/') ? filePath : (cwd.cwd ? joinPath(cwd.cwd, filePath) : filePath)
    return parentPath(absPath)
  }, [filePath, cwd.cwd])

  // Re-seed the view mode if the file ever changes (dockview reuses a panel
  // instance when its params update). Image files ignore `mode` entirely.
  useEffect(() => {
    setMode(params.fileViewMode ?? defaultViewMode(fileName))
    setLanguageOverride(params.fileLanguage)
  }, [fileName, params.fileViewMode, params.fileLanguage])

  const editorId = params.editorId
  const contentRef = useRef(content)
  contentRef.current = content

  // 注册插件控制器（挂载→registry attach；卸载→detach + onClose 广播）。
  useEffect(() => {
    if (!editorId || isImage) return
    const controller = {
      revealLine: (line: number, center?: boolean) => {
        const ed = editorRef.current
        if (!ed) return
        if (center) ed.revealLineInCenter(line)
        else ed.revealLine(line)
      },
      revealRange: (s: number, e: number) => editorRef.current?.revealLinesInCenter(s, e),
      setSelection: (sl: number, sc: number, el: number, ec: number) => {
        const ed = editorRef.current
        const monaco = monacoRef.current
        if (!ed || !monaco) return
        const range = new monaco.Range(sl, sc, el, ec)
        ed.setSelection(range)
        ed.revealRangeInCenter(range)
      },
      setCursorPosition: (line: number, column: number) => {
        const ed = editorRef.current
        if (!ed) return
        ed.setPosition({ lineNumber: line, column })
      },
      highlightLines: (s: number, e: number, className?: string) => {
        const ed = editorRef.current
        if (!ed) return
        const monaco = monacoRef.current
        if (!monaco) return
        const col = decorationsRef.current ?? ed.createDecorationsCollection([])
        decorationsRef.current = col
        col.set([
          {
            range: new monaco.Range(s, 1, e, 1),
            options: { isWholeLine: true, className: className ?? 'plugin-line-highlight' },
          },
        ])
      },
      clearHighlights: () => decorationsRef.current?.set([]),
      getContent: () => contentRef.current,
      setContent: (text: string) => setContent(text),
      setLanguage: (lang: string) => setLanguageOverride(lang),
      setTitle: (title: string) => api?.setTitle?.(title),
      setViewMode: (m: 'editor' | 'preview') => {
        if (canTogglePreview(fileName)) setMode(m)
      },
      close: () => api?.close?.(),
    }
    return attachEditor(editorId, controller)
  }, [editorId, isImage, fileName, setContent, api])

  // 初始定位（opts.line / opts.highlight）：内容加载 + editor 挂载后执行一次。
  useEffect(() => {
    if (!editorId || loading || !editorRef.current) return
    const mark = `${editorId}:${filePath}`
    if (initialAppliedRef.current === mark) return
    initialAppliedRef.current = mark
    const ed = editorRef.current
    const monaco = monacoRef.current
    if (!monaco) return
    if (params.initialLine && params.initialLine > 0) {
      ed.revealLineInCenter(params.initialLine)
      ed.setPosition({ lineNumber: params.initialLine, column: 1 })
    }
    if (params.initialHighlight) {
      const { startLine, endLine } = params.initialHighlight
      const col = ed.createDecorationsCollection([
        {
          range: new monaco.Range(startLine, 1, endLine ?? startLine, 1),
          options: { isWholeLine: true, className: 'plugin-line-highlight' },
        },
      ])
      decorationsRef.current = col
      if (!params.initialLine) ed.revealLineInCenter(startLine)
    }
  }, [editorId, loading, filePath, params.initialLine, params.initialHighlight])

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
          <MonacoEditor
            value={content}
            language={language}
            onChange={setContent}
            onEditorMount={(ed, monaco) => {
              editorRef.current = ed
              monacoRef.current = monaco
            }}
          />
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
