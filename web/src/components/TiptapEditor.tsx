import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Placeholder from '@tiptap/extension-placeholder'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import { Markdown } from 'tiptap-markdown'
import { common, createLowlight } from 'lowlight'
import { useEffect, useRef, useState, useImperativeHandle, useCallback, forwardRef } from 'react'
import { useTranslation } from '../i18n'

const lowlight = createLowlight(common)

const MAX_HISTORY = 100

interface TiptapEditorProps {
  onSend: (content: string) => void
  disabled: boolean
  connected: boolean
  currentModel?: string
  onCancel?: () => void
}

export interface TiptapEditorHandle {
  setContent: (md: string) => void
  focus: () => void
}

const TiptapEditor = forwardRef<TiptapEditorHandle, TiptapEditorProps>(
  function TiptapEditor({ onSend, disabled, connected, currentModel, onCancel }, ref) {
  const [hasContent, setHasContent] = useState(false)
  const [previewMode, setPreviewMode] = useState(false)
  const connectedRef = useRef(connected)
  connectedRef.current = connected  // Direct render-time assignment (React 19 pattern)
  const { t } = useTranslation()

  // Input history (newest first, like bash history)
  const inputHistoryRef = useRef<string[]>([])
  const historyIndexRef = useRef(-1)

  // Stable ref for handleSend to avoid stale closure in editor handleKeyDown.
  const handleSendRef = useRef<() => void>(() => {})

  // Refs to access t() in Tiptap editor config (created once)
  const tRef = useRef(t)
  tRef.current = t  // Direct render-time assignment

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ codeBlock: false }),
      Placeholder.configure({
        placeholder: () => connectedRef.current ? tRef.current('inputPlaceholder') : tRef.current('connecting'),
      }),
      CodeBlockLowlight.configure({ lowlight }),
      TaskList,
      TaskItem.configure({ nested: true }),
      Markdown.configure({
        html: false,
        breaks: true,
        transformPastedText: true,
        transformCopiedText: true,
      }),
    ],
    content: '',
    editorProps: {
      attributes: {
        class: 'tiptap-editor max-w-none focus:outline-none',
      },
      handleKeyDown: (_view, event) => {
        // Enter = send (without Shift/Ctrl/Cmd)
        if (event.key === 'Enter' && !event.shiftKey && !(event.ctrlKey || event.metaKey)) {
          event.preventDefault()
          handleSendRef.current()
          return true
        }

        // Ctrl/Cmd+Enter = newline (let tiptap handle natively)
        if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
          return false
        }

        // Escape = cancel or clear
        if (event.key === 'Escape') {
          if (onCancel) {
            onCancel()
          } else {
            editor?.commands.clearContent()
            setHasContent(false)
          }
          return true
        }

        // ArrowUp = browse history (when editor is empty)
        if (event.key === 'ArrowUp' && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
          if (editor) {
            const text = editor.getText().trim()
            if (text === '' && inputHistoryRef.current.length > 0) {
              const idx = historyIndexRef.current + 1
              if (idx < inputHistoryRef.current.length) {
                historyIndexRef.current = idx
                editor.commands.setContent(inputHistoryRef.current[idx])
                setHasContent(true)
                editor.commands.focus('end')
                return true
              }
            }
          }
        }

        // ArrowDown = browse history forward
        if (event.key === 'ArrowDown' && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
          if (editor && historyIndexRef.current >= 0) {
            const idx = historyIndexRef.current - 1
            if (idx >= 0) {
              historyIndexRef.current = idx
              editor.commands.setContent(inputHistoryRef.current[idx])
              editor.commands.focus('end')
              return true
            } else {
              historyIndexRef.current = -1
              editor.commands.clearContent()
              setHasContent(false)
              return true
            }
          }
        }

        return false
      },
    },
    editable: !disabled,
    immediatelyRender: false,
    onUpdate: ({ editor: ed }) => {
      setHasContent(ed.getText().trim().length > 0)
      // Exit history browsing mode on any content change
      historyIndexRef.current = -1
    },
  })

  useEffect(() => {
    if (!editor) return
    if (previewMode) {
      editor.setEditable(false)
    } else {
      editor.setEditable(!disabled && connected)
    }
  }, [editor, previewMode, disabled, connected])

  useImperativeHandle(ref, () => ({
    setContent: (md: string) => {
      if (!editor) return
      editor.commands.setContent(md)
      editor.commands.focus('end')
    },
    focus: () => {
      editor?.commands.focus()
    },
  }), [editor])

  const handleSend = useCallback(() => {
    if (!editor) return
    const md = (editor.storage as { markdown?: { getMarkdown: () => string } }).markdown?.getMarkdown() ?? ''
    if (!md.trim()) return

    // Add to history (deduplicate with first entry)
    const history = inputHistoryRef.current
    if (history.length === 0 || history[0] !== md) {
      history.unshift(md)
      if (history.length > MAX_HISTORY) {
        history.length = MAX_HISTORY
      }
    }
    historyIndexRef.current = -1

    onSend(md)
    editor.commands.clearContent()
    setHasContent(false)
    editor.commands.focus()
  }, [editor, onSend])

  // Keep ref in sync with latest handleSend
  handleSendRef.current = handleSend

  return (
    <div className="tiptap-wrapper relative">
      {/* Edit/Preview toggle */}
      <div className="absolute top-1 right-12 flex gap-0.5 z-10">
        <button
          onClick={() => setPreviewMode(false)}
          className={`px-2 py-0.5 text-[10px] rounded-t ${!previewMode ? 'bg-slate-700 text-white' : 'text-slate-500 hover:text-slate-300'}`}
          title={t('edit')}
        >
          {t('edit')}
        </button>
        <button
          onClick={() => setPreviewMode(true)}
          className={`px-2 py-0.5 text-[10px] rounded-t ${previewMode ? 'bg-slate-700 text-white' : 'text-slate-500 hover:text-slate-300'}`}
          title={t('preview')}
        >
          {t('preview')}
        </button>
      </div>
      <div style={{ flex: 1, minWidth: 0 }} className={previewMode ? 'tiptap-preview-mode' : ''}>
        <EditorContent editor={editor} />
      </div>
      <button
        onClick={handleSend}
        disabled={!connected || disabled || !hasContent}
        className="tiptap-send-btn"
        title={t('send')}
        aria-label={t('sendMessage')}
      >
        ➤
      </button>
      {currentModel && (
        <span className="absolute bottom-1 left-3 text-[11px] text-slate-500 truncate pointer-events-none select-none">
          {currentModel}
        </span>
      )}
    </div>
  )
})

export default TiptapEditor
