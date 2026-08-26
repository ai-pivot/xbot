/**
 * MessageInput — the Agent panel composer (Spec C §1.1).
 *
 * Redesigned with a tiptap WYSIWYG editor:
 *   - Markdown shortcuts (typing **bold**, `code`, - lists, > quotes)
 *   - Code blocks with syntax highlighting
 *   - Tab completion (/ commands, @ file paths) via useCompletion
 *   - Configurable send key (Enter or Ctrl+Enter)
 *   - File-attach button + goal mode + cancel button
 *   - Draft persistence (localStorage, markdown serialized)
 *
 * The editor outputs markdown (via tiptap-markdown), so onSend still receives
 * a plain string — zero interface change for downstream consumers.
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { Placeholder } from '@tiptap/extension-placeholder'
import { Markdown } from 'tiptap-markdown'
import { Loader2, Paperclip, Send, Square, Target, X } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { useCwd } from '@/providers/CwdProvider'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useSendKeyMode, isSendKey } from '@/hooks/useSendKeyMode'
import type { Attachments } from '@/hooks/useChatMessages'
import { cn } from '@/lib/utils'
import { setChatInsertHandler } from '@/lib/chatInputBridge'
import { TodoPullOut } from './TodoPullOut'
import { GoalBanner } from './GoalBanner'
import { CompletionPopup } from './CompletionPopup'
import { useCompletion, type CompletionKeyEvent } from '@/hooks/useCompletion'
import type { TodoState } from '@/hooks/useTodos'
import type { GoalInfo } from '@/types/shared'

interface MessageInputProps {
  /** True while the agent is producing output; shows the cancel button. */
  busy: boolean
  /** True while cancel is in flight; shows spinner on cancel button. */
  cancelling?: boolean
  /** Send a message, optionally with uploaded attachments. */
  onSend: (content: string, attachments?: Attachments) => void
  /** Cancel the running agent. */
  onCancel: () => void
  /** Rewind to the latest user message, matching TUI /rewind intent in Web. */
  onRewindLatest?: () => void
  /** Open the right Tasks panel for the current session. */
  onOpenTasks?: () => void
  /** Upload a file; resolves with server metadata. */
  onUpload: (file: File) => Promise<{
    upload_key?: string
    name?: string
    size?: number
    mime?: string
  }>
  /** TODO state from the progress snapshot; null hides the inset TODO toolbar. */
  todoState?: TodoState | null
  /** Active goal from the progress snapshot; null hides the goal banner. */
  goal?: GoalInfo | null
  /** Edit the goal objective (direct RPC, does not trigger a Run). */
  onSetGoal?: (objective: string) => void
  /** Clear the active goal. */
  onClearGoal?: () => void
  /** Controls rendered immediately before the send/cancel button. */
  trailingControls?: ReactNode
  draft?: string
  onDraftConsumed?: () => void
  /** Session identifier for localStorage draft persistence. */
  sessionKey?: string
}

interface PendingAttachment {
  name: string
  size: number
  uploadKey: string
  mime: string
}

/** Module-level editor instance ref for test access. */
let __testEditor: import('@tiptap/react').Editor | null = null

export function MessageInput({ busy, cancelling = false, onSend, onCancel, onRewindLatest, onOpenTasks, onUpload, todoState, goal, onSetGoal, onClearGoal, trailingControls, draft, onDraftConsumed, sessionKey }: MessageInputProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const { cwd } = useCwd()
  const { mode: sendKeyMode } = useSendKeyMode()
  const [goalMode, setGoalMode] = useState(false)
  const [addingGoal, setAddingGoal] = useState(false)
  const [goalDraft, setGoalDraft] = useState('')
  const draftStorageKey = sessionKey ? `xbot:draft:${sessionKey}` : null
  const [pending, setPending] = useState<PendingAttachment[]>([])
  const [uploading, setUploading] = useState(false)
  const [focused, setFocused] = useState(false)
  const [hasContent, setHasContent] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  // Refs for stable callbacks inside editor's handleKeyDown (avoids stale closures)
  const completionHandlerRef = useRef<(e: CompletionKeyEvent) => boolean>(() => false)
  const submitRef = useRef<() => void>(() => {})
  const sendKeyModeRef = useRef(sendKeyMode)
  sendKeyModeRef.current = sendKeyMode

  // Dynamic placeholder text (updates with goalMode/sendKeyMode)
  const placeholderText = goalMode
    ? '🎯 输入目标描述，发送后将设为 Goal 并开始执行...'
    : t(sendKeyMode === 'enter' ? 'agent.inputPlaceholderEnter' : 'agent.inputPlaceholder')
  const placeholderRef = useRef(placeholderText)
  placeholderRef.current = placeholderText

  // Initial content (from draft prop or localStorage — computed once)
  const [initialContent] = useState(() => {
    if (draft !== undefined) return draft
    if (draftStorageKey) {
      try {
        return localStorage.getItem(draftStorageKey) ?? ''
      } catch { /* ignore */ }
    }
    return ''
  })

  // Draft save debounce timer
  const saveDraftTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  // --- tiptap editor ---
  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: false,
        horizontalRule: false,
      }),
      Placeholder.configure({
        placeholder: () => placeholderRef.current,
      }),
      Markdown.configure({
        html: false,
        tightLists: true,
        linkify: false,
        breaks: true,
      }),
    ],
    content: initialContent,
    editorProps: {
      attributes: {
        class: 'xbot-editor',
        'aria-label': 'Message input',
      },
      handleKeyDown: (_view, event) => {
        // Don't trigger during IME composition
        if (event.isComposing) return false
        // 1. Completion first (ArrowUp/Down, Tab, Escape, Enter for file completion)
        if (completionHandlerRef.current(event)) return true
        // 2. Send key (Enter or Ctrl+Enter depending on settings)
        if (isSendKey(event, sendKeyModeRef.current)) {
          event.preventDefault()
          submitRef.current()
          return true
        }
        return false
      },
    },
    onUpdate: ({ editor }) => {
      setHasContent(!editor.isEmpty)
      // Debounced draft save — 300ms delay gives Markdown extension time to
      // parse content before getMarkdown() is called. Immediate save would
      // call getMarkdown() before parsing completes → returns empty string →
      // localStorage.removeItem deletes the draft.
      if (saveDraftTimerRef.current) clearTimeout(saveDraftTimerRef.current)
      saveDraftTimerRef.current = setTimeout(() => {
        if (!draftStorageKey) return
        try {
          const md = (editor.storage as unknown as { markdown?: { getMarkdown?: () => string } }).markdown?.getMarkdown?.() ?? ''
          if (md) localStorage.setItem(draftStorageKey, md)
          else localStorage.removeItem(draftStorageKey)
        } catch { /* ignore */ }
      }, 300)
    },
    onFocus: () => setFocused(true),
    onBlur: () => setFocused(false),
  })

  // Expose editor for test access
  useEffect(() => {
    __testEditor = editor
    return () => { __testEditor = null }
  }, [editor])

  // --- Completion (wired to tiptap editor) ---
  const completion = useCompletion({ editor, ws, cwd })
  completionHandlerRef.current = completion.handleKeyDown

  // --- Get markdown text from editor ---
  const getText = useCallback(() => {
    if (!editor) return ''
    return (editor.storage as unknown as { markdown?: { getMarkdown?: () => string } }).markdown?.getMarkdown?.()?.trim() ?? editor.getText().trim()
  }, [editor])

  // --- Submit ---
  const submit = useCallback(() => {
    if (!editor) return
    const text = getText()
    if (!text && pending.length === 0) return
    if (text === '/rewind' && pending.length === 0 && onRewindLatest) {
      if (!busy) onRewindLatest()
      editor.commands.clearContent()
      return
    }
    if (text === '/cancel' && pending.length === 0) {
      if (busy) onCancel()
      editor.commands.clearContent()
      return
    }
    if (text === '/tasks' && pending.length === 0 && onOpenTasks) {
      onOpenTasks()
      editor.commands.clearContent()
      return
    }
    if (busy && text === '/new' && pending.length === 0) {
      toast.error(t('agent.busy'))
      return
    }
    const attachments: Attachments | undefined = pending.length
      ? {
          uploadKeys: pending.map((p) => p.uploadKey),
          fileNames: pending.map((p) => p.name),
          fileSizes: pending.map((p) => p.size),
          fileMimes: pending.map((p) => p.mime),
        }
      : undefined
    // When goalMode is on, send as /goal command (sets goal + starts working).
    const content = goalMode ? `/goal ${text}` : text
    onSend(content, attachments)
    setGoalMode(false)
    editor.commands.clearContent()
    setPending([])
  }, [editor, getText, pending, onCancel, onRewindLatest, onOpenTasks, onSend, busy, goalMode, t])

  // Update submit ref (so handleKeyDown always calls the latest submit)
  submitRef.current = submit

  // --- Draft prop changes (external session switch) ---
  useEffect(() => {
    if (draft === undefined || !editor) return
    editor.commands.setContent(draft)
    onDraftConsumed?.()
  }, [draft, onDraftConsumed, editor])

  // --- chatInputBridge: let file explorer inject text ---
  useEffect(() => {
    if (!editor) return
    const insertHandler = (text: string) => {
      const currentText = editor.getText()
      const sep = (!currentText || currentText.endsWith('\n')) ? '' : '\n'
      editor.chain().focus('end').insertContent(sep + text).run()
    }
    setChatInsertHandler(insertHandler)
    return () => setChatInsertHandler(null)
  }, [editor])

  // --- Update placeholder when goalMode changes ---
  useEffect(() => {
    if (!editor) return
    const ext = editor.extensionManager.extensions.find(e => e.name === 'placeholder')
    if (ext) {
      ext.options.placeholder = placeholderText
    }
    // Force placeholder re-evaluation if editor is empty
    if (editor.isEmpty) {
      editor.view.dispatch(editor.view.state.tr)
    }
  }, [placeholderText, editor])

  // --- Cleanup draft timer on unmount ---
  useEffect(() => {
    return () => {
      if (saveDraftTimerRef.current) clearTimeout(saveDraftTimerRef.current)
    }
  }, [])

  // --- File upload ---
  const onPickFiles = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0) return
      setUploading(true)
      try {
        const added: PendingAttachment[] = []
        for (const file of Array.from(files)) {
          const res = await onUpload(file)
          added.push({
            name: res.name ?? file.name,
            size: res.size ?? file.size,
            uploadKey: res.upload_key ?? '',
            mime: res.mime ?? file.type,
          })
        }
        setPending((prev) => [...prev, ...added])
      } catch (e) {
        toast.error(e instanceof Error ? e.message : t('agent.uploadFailed'))
      } finally {
        setUploading(false)
      }
    },
    [onUpload, t],
  )

  const canSend = hasContent || pending.length > 0

  return (
    <div className="border-t border-border bg-bg-primary px-3 py-2.5">
      {goal ? <GoalBanner goal={goal} onEdit={onSetGoal ?? (() => {})} onClear={onClearGoal ?? (() => {})} /> : null}
      {addingGoal && (
        <div className="mx-2 mb-1.5 flex items-center gap-2 rounded-md border border-accent/30 bg-accent/5 px-2.5 py-1.5">
          <Target className="size-3.5 shrink-0 text-accent" />
          <input
            autoFocus
            value={goalDraft}
            onChange={(e) => setGoalDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.nativeEvent.isComposing && goalDraft.trim()) {
                e.preventDefault()
                onSetGoal?.(goalDraft.trim())
                setGoalDraft('')
                setAddingGoal(false)
              } else if (e.key === 'Escape') {
                e.preventDefault()
                setGoalDraft('')
                setAddingGoal(false)
              }
            }}
            onBlur={() => {
              if (goalDraft.trim()) {
                onSetGoal?.(goalDraft.trim())
              }
              setGoalDraft('')
              setAddingGoal(false)
            }}
            className="min-w-0 flex-1 bg-transparent px-1 text-xs text-text-primary outline-none ring-1 ring-accent/40 rounded"
            placeholder="输入目标..."
          />
          <span className="shrink-0 text-[10px] text-text-muted">Enter 保存 · Esc 取消</span>
        </div>
      )}
      {todoState ? <TodoPullOut todoState={todoState} hasGoal={!!goal || addingGoal} onSetGoal={onSetGoal ? () => {
        setAddingGoal(true)
      } : undefined} /> : null}

      {/* Input container — single rounded box with chips, editor, and inline buttons */}
      <div
        className={cn(
          'rounded-xl border bg-bg-secondary px-3 py-2 transition-[border-color,box-shadow]',
          goalMode
            ? 'border-accent/50 ring-1 ring-accent/20'
            : focused
              ? 'border-accent ring-1 ring-accent/30'
              : 'border-border',
        )}
      >
        {/* Attachment chips (inside container, above editor) */}
        {pending.length > 0 && (
          <div className="mb-2 flex flex-wrap gap-1.5">
            {pending.map((p, i) => (
              <span
                key={`${p.uploadKey}-${i}`}
                className="inline-flex items-center gap-1 rounded-md bg-bg-tertiary px-2 py-1 text-xs text-text-secondary"
              >
                <Paperclip className="size-3" />
                <span className="max-w-[20ch] truncate">{p.name}</span>
                <button
                  type="button"
                  aria-label="remove"
                  onClick={() => setPending((prev) => prev.filter((_, idx) => idx !== i))}
                  className="text-text-muted hover:text-text-primary"
                >
                  <X className="size-3" />
                </button>
              </span>
            ))}
          </div>
        )}

        {/* tiptap WYSIWYG editor */}
        <div className="relative">
          <CompletionPopup
            candidates={completion.candidates}
            selectedIndex={completion.selectedIndex}
            visible={completion.visible}
            triggerType={completion.triggerType}
            onSelect={completion.completeCandidate}
          />
          <EditorContent editor={editor} />
        </div>

        {/* Bottom row: attach button (left) + goal toggle + send/cancel button (right) */}
        <div className="mt-2 flex min-w-0 items-center justify-between gap-2">
          <div className="flex items-center gap-1">
            <input
              ref={fileRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                onPickFiles(e.target.files)
                e.target.value = ''
              }}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t('agent.attach')}
              disabled={uploading}
              onClick={() => fileRef.current?.click()}
              className={cn('size-9 rounded-md', uploading && 'opacity-40')}
            >
              {uploading ? <Loader2 className="size-4 animate-spin" /> : <Paperclip className="size-4" />}
            </Button>
            {/* Goal toggle button — when active, message is sent as /goal */}
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="设为目标模式"
              onClick={() => setGoalMode((v) => !v)}
              className={cn(
                'size-9 rounded-md transition-all',
                goalMode
                  ? 'bg-accent/15 text-accent ring-1 ring-accent/40 shadow-[0_0_8px_rgba(var(--accent-rgb),0.3)]'
                  : 'text-text-muted hover:text-text-primary',
              )}
              title={goalMode ? '目标模式已开启（发送后将设为 Goal）' : '开启目标模式'}
            >
              <Target className={cn('size-4', goalMode && 'animate-pulse [animation-duration:2s]')} />
            </Button>
          </div>

          <div className="flex min-w-0 items-center gap-1">
            {trailingControls}
            {busy ? (
              <Button
                type="button"
                variant="destructive"
                size="icon-sm"
                aria-label={t('common.cancel')}
                onClick={onCancel}
                disabled={cancelling}
                className="size-9 rounded-md"
              >
                {cancelling ? <Loader2 className="size-4 animate-spin" /> : <Square className="size-4" />}
              </Button>
            ) : (
              <Button
                type="button"
                size="icon-sm"
                aria-label={goalMode ? '设为目标' : t('agent.send')}
                disabled={!canSend}
                onClick={submit}
                className={cn(
                  'size-9 rounded-md transition-all',
                  goalMode
                    ? 'bg-accent text-accent-foreground shadow-[0_0_12px_rgba(var(--accent-rgb),0.4)]'
                    : 'bg-accent text-accent-foreground',
                  !canSend && 'opacity-40',
                )}
              >
                {goalMode ? <Target className="size-4" /> : <Send className="size-4" />}
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/** Test-only: get the current tiptap editor instance for integration tests. */
export function __getTestEditor() {
  return __testEditor
}
