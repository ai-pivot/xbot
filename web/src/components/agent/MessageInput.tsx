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
import { useEditor, EditorContent, type Editor } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Link from '@tiptap/extension-link'
import Image from '@tiptap/extension-image'
import { Placeholder } from '@tiptap/extension-placeholder'
import { Markdown } from 'tiptap-markdown'
import { Loader2, Mail, Paperclip, Send, Square, Target, X, Zap, Clock } from 'lucide-react'
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
import { SelectionToolbar } from './SelectionToolbar'
import { useCompletion, type CompletionKeyEvent } from '@/hooks/useCompletion'
import type { TodoState } from '@/hooks/useTodos'
import type { GoalInfo } from '@/types/shared'

interface MessageInputProps {
  /** True while the agent is producing output; shows the cancel button. */
  busy: boolean
  /** True while cancel is in flight; shows spinner on cancel button. */
  cancelling?: boolean
  /** Send a message, optionally with uploaded attachments.
   *  interrupt=true: ⚡ interject — deliver into the active turn (no new turn). */
  onSend: (content: string, attachments?: Attachments, interrupt?: boolean) => void
  /** Cancel the running agent. */
  onCancel: () => void
  /** Rewind to the latest user message, matching TUI /rewind intent in Web. */
  onRewindLatest?: () => void
  /** Open the right Tasks panel for the current session. */
  onOpenTasks?: () => void
  /** Upload a file; resolves with server metadata. */
  onUpload: (file: File, onProgress?: (loaded: number, total: number) => void) => Promise<{
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
  /** ⚡ Interject mode: when true, onSend is called with interrupt=true. */
  interruptMode?: boolean
  /** Callback when interject mode is toggled. */
  onInterruptModeChange?: (mode: boolean) => void
  /** Current model's vision (multimodal image input) switch — purely manual
   * per-model config (NO whitelist). undefined = unknown (no hint shown);
   * false = attachments with images get the "vision not enabled" advisory
   * bar; true = image attachments send a confirmation toast. */
  modelVision?: boolean
}

interface PendingAttachment {
  name: string
  size: number
  uploadKey: string
  mime: string
  /** Upload-in-progress state（乐观 chip：上传中渲染进度条，完成前 uploadKey 为空）。 */
  uploading?: boolean
  /** 0..1 — XHR upload.onprogress（驱动 chip 进度条）。 */
  progress?: number
}

/** Image attachment detection (matches insertUploadedMedia's mime/extension
 * test — an attachment is an image when the pending chip carries it). */
function isImageAttachment(p: PendingAttachment): boolean {
  return p.mime.startsWith('image/') || /\.(png|jpe?g|gif|webp|avif|svg|bmp|ico)$/i.test(p.name)
}

/** Module-level editor instance ref for test access. */
let __testEditor: import('@tiptap/react').Editor | null = null

/**
 * Chat-input Link extension — reconfigured for an INPUT box, not a document editor:
 *
 *  - inclusive:false      → typed text NEVER merges into an existing link
 *                            (upstream default `inclusive = autolink = true` is the
 *                            "意料之外的文字变成超链接" root cause; autolink re-scans on
 *                            whitespace so it is unaffected by inclusivity)
 *  - openOnClick:false    → editing, not navigating (upstream default true opens
 *                            a new tab when a link inside the input is clicked)
 *  - linkOnPaste:false    → pasting a URL never converts selected text into a
 *                            link (predictable paste semantics)
 *  - markdownLinks:true   → typing `[text](url)` renders as a live link (WYSIWYG
 *                            — otherwise the syntax stays literal and invisible)
 *  - shouldAutoLink       → strict: only http(s):// or www. — bare domains like
 *                            `file.tar.gz` (`.gz` is a valid TLD!) are NOT
 *                            auto-linked (upstream linkifies them → false positives)
 *  - autolink:true        → typing a full URL then whitespace still auto-links
 *  - target:null          → no target=_blank in the editing surface
 */
const EditorLink = Link.extend({
  inclusive() {
    return false
  },
}).configure({
  openOnClick: false,
  enableClickSelection: false,
  linkOnPaste: false,
  autolink: true,
  markdownLinks: true,
  shouldAutoLink: (url: string) => /^(https?:\/\/|www\.)/i.test(url),
  HTMLAttributes: { target: null, rel: 'noopener noreferrer nofollow' },
})

/** Select the word at the cursor (used by Ctrl/Cmd+K with an empty selection). */
function selectWordAtCursor(editor: Editor): boolean {
  const { selection } = editor.state
  const { $from } = selection
  if (!$from.parent.isTextblock) return false
  const text = $from.parent.textContent
  const offset = $from.parentOffset
  const isBoundary = (ch: string) => /\s/.test(ch)
  let start = offset
  let end = offset
  while (start > 0 && !isBoundary(text[start - 1])) start -= 1
  while (end < text.length && !isBoundary(text[end])) end += 1
  if (start >= end) return false
  const base = $from.pos - offset
  return editor.commands.setTextSelection({ from: base + start, to: base + end })
}

export function MessageInput({ busy, cancelling = false, onSend, onCancel, onRewindLatest, onOpenTasks, onUpload, todoState, goal, onSetGoal, onClearGoal, trailingControls, draft, onDraftConsumed, sessionKey, interruptMode = false, onInterruptModeChange, modelVision }: MessageInputProps) {
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
  // File-pick/upload handler ref — editorProps closures capture this once, the
  // ref always points at the latest onPickFiles (which depends on onUpload).
  const onPickFilesRef = useRef<(files: FileList | null) => void>(() => {})
  // Ctrl/Cmd+K → open the selection toolbar's link editor (SelectionToolbar)
  const [linkEditSignal, setLinkEditSignal] = useState(0)
  // File drag-over highlight (dragenter/dragleave depth counted — children fire both)
  const [dragOverFiles, setDragOverFiles] = useState(false)
  const dragDepthRef = useRef(0)

  // Dynamic placeholder text (updates with goalMode/sendKeyMode/interruptMode/busy)
  const placeholderText = goalMode
    ? t('agent.goal.inputPlaceholderMain')
    : interruptMode
      ? t('agent.inputPlaceholderInterject')
      : busy
        ? t('agent.inputPlaceholderBusy')
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
  // Latest content ref — updated on every onUpdate, used for unmount flush
  const latestContentRef = useRef('')
  // Keep editor ref for unmount flush (editor may be destroyed by useEditor cleanup)
  const editorRef = useRef<Editor | null>(null)

  // --- tiptap editor ---
  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: false,
        horizontalRule: false,
        // StarterKit ships the Link extension with document-editor defaults
        // (openOnClick, inclusive marks, aggressive autolink) — replaced by
        // our chat-input-tuned EditorLink below.
        link: false,
        dropcursor: { color: 'var(--accent, #6366f1)', width: 2 },
      }),
      // Paste-markdown image rendering: pasted/uploaded images insert as ![name](url)
      // markdown — this extension makes tiptap render them inline in the composer
      // (tiptap-markdown parses ![alt](src) into image nodes when the schema has one).
      Image.configure({ inline: true }),
      EditorLink,
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
        // 2. Link editor shortcut (Ctrl/Cmd+K) — before the send key so it
        //    never collides with Enter / Ctrl+Enter modes
        if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'k') {
          event.preventDefault()
          const ed = editorRef.current
          if (ed) {
            // Sync PM state from the DOM before word detection — the browser may
            // have just moved the caret (Home/End/arrows) and prosemirror-view
            // reads DOM selection on the async 'selectionchange' task, which
            // can lag a same-frame keychord (PM itself flushes on mousedown).
            ;(ed.view as unknown as { domObserver: { flush: () => void } }).domObserver.flush()
            // Select the word at the cursor (or the whole link under it) so the
            // toolbar has a target even when nothing is selected
            if (ed.state.selection.empty) {
              if (ed.isActive('link')) ed.chain().extendMarkRange('link').focus().run()
              else selectWordAtCursor(ed)
            }
            setLinkEditSignal((n) => n + 1)
          }
          return true
        }
        // 3. Send key (Enter or Ctrl+Enter depending on settings)
        if (isSendKey(event, sendKeyModeRef.current)) {
          event.preventDefault()
          submitRef.current()
          return true
        }
        return false
      },
      // Paste files/images (screenshots) → upload as attachments instead of
      // letting the clipboard content hit the editor
      handlePaste: (_view, event) => {
        const files = event.clipboardData?.files
        if (files && files.length > 0) {
          event.preventDefault()
          onPickFilesRef.current(files)
          return true
        }
        return false
      },
      // Drag-and-drop files onto the editor → upload as attachments
      handleDrop: (_view, event, _slice, moved) => {
        if (moved) return false
        const files = event.dataTransfer?.files
        if (!files || files.length === 0) return false
        event.preventDefault()
        onPickFilesRef.current(files)
        return true
      },
      // Drag-over highlight (depth-counted — dragenter/leave fire per child element)
      handleDOMEvents: {
        dragenter: () => {
          dragDepthRef.current += 1
          setDragOverFiles(true)
          return false
        },
        dragleave: () => {
          dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
          if (dragDepthRef.current === 0) setDragOverFiles(false)
          return false
        },
        dragover: () => false,
        drop: () => {
          dragDepthRef.current = 0
          setDragOverFiles(false)
          return false
        },
      },
    },
    onUpdate: ({ editor }) => {
      setHasContent(!editor.isEmpty)
      // IMMEDIATE save on every keystroke — no debounce.
      // Previous debounce approach failed on mobile: if user switches sessions
      // within 300ms, clearTimeout cancels the timer and the draft is lost.
      // Use editor.getText() (always available, no extension dependency)
      // as fallback when getMarkdown() returns empty (before Markdown ext parses).
      if (draftStorageKey) {
        try {
          const md = (editor.storage as unknown as { markdown?: { getMarkdown?: () => string } }).markdown?.getMarkdown?.() ?? ''
          if (md) {
            latestContentRef.current = md
            localStorage.setItem(draftStorageKey, md)
          } else if (!editor.isEmpty) {
            // getMarkdown() returned empty but editor has content —
            // use getText() as fallback (plain text, better than losing draft)
            const text = editor.getText()
            if (text) {
              latestContentRef.current = text
              localStorage.setItem(draftStorageKey, text)
            }
          } else {
            // Editor is empty (e.g. after sending a message) — clear the draft
            latestContentRef.current = ''
            localStorage.removeItem(draftStorageKey)
          }
        } catch { /* ignore */ }
      }
    },
    onFocus: () => setFocused(true),
    onBlur: () => setFocused(false),
  })

  // Keep editor ref for unmount flush
  editorRef.current = editor

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
    // 只发完成态的附件（uploading 中的 chip uploadKey 为空——乐观 chip 上传期间不可发送）
    const completed = pending.filter((p) => !p.uploading && p.uploadKey)
    const attachments: Attachments | undefined = completed.length
      ? {
          uploadKeys: completed.map((p) => p.uploadKey),
          fileNames: completed.map((p) => p.name),
          fileSizes: completed.map((p) => p.size),
          fileMimes: completed.map((p) => p.mime),
        }
      : undefined
    // Vision confirmation toast: image attachments + model vision ON → tell
    // the user the images are actually sent to the model as multimodal input.
    if (modelVision && completed.some(isImageAttachment)) {
      const n = completed.filter(isImageAttachment).length
      toast.success(t('agent.visionImagesAttached', { count: String(n) }))
    }
    // When goalMode is on, send as /goal command (sets goal + starts working).
    // When interruptMode is on, pass interrupt=true (⚡ interject into active turn).
    const content = goalMode ? `/goal ${text}` : text
    onSend(content, attachments, interruptMode || undefined)
    setGoalMode(false)
    if (interruptMode) onInterruptModeChange?.(false)
    editor.commands.clearContent()
    setPending([])
  }, [editor, getText, pending, onCancel, onRewindLatest, onOpenTasks, onSend, busy, goalMode, interruptMode, onInterruptModeChange, modelVision, t])

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

  // Auto-reset interject mode when the session leaves busy — the ⚡/queue toggle
  // is meaningless while idle, and a stale interruptMode=true keeps the composer
  // in 插话 UI (violet send button + interject placeholder) after busy→idle
  // (user report: "插话/排队 UI 不会自动转变普通发送 UI")，and a queued
  // message would carry interrupt=true against an idle session. Resetting also
  // makes the next busy period start from the default queue mode (反之亦然).
  useEffect(() => {
    if (!busy && interruptMode) onInterruptModeChange?.(false)
  }, [busy, interruptMode, onInterruptModeChange])

  // --- Cleanup draft timer on unmount + flush draft synchronously ---
  useEffect(() => {
    return () => {
      if (saveDraftTimerRef.current) clearTimeout(saveDraftTimerRef.current)
      if (!draftStorageKey) return
      // Priority 1: latestContentRef (updated on every onUpdate, always current)
      if (latestContentRef.current) {
        try { localStorage.setItem(draftStorageKey, latestContentRef.current) } catch { /* ignore */ }
        return
      }
      // Priority 2: editorRef (editor might still be alive — React runs cleanups
      // in reverse registration order, so useEditor's destroy runs AFTER ours)
      const ed = editorRef.current
      if (!ed || ed.isEmpty) return
      try {
        // Try markdown first, fall back to HTML
        const md = (ed.storage as unknown as { markdown?: { getMarkdown?: () => string } }).markdown?.getMarkdown?.() ?? ''
        if (md) {
          localStorage.setItem(draftStorageKey, md)
        } else {
          localStorage.setItem(draftStorageKey, ed.getHTML())
        }
      } catch { /* ignore */ }
    }
  }, [])

  // --- File upload ---
  // Server-side size cap (web_file.go maxFileSize = 10 << 20). Client-side
  // pre-check gives instant feedback — no upload round-trip just to be
  // rejected with 413 (CR note: handleDrop/handlePaste previously uploaded
  // oversized files blindly). Size-only: uploads stay type-unrestricted.
  const MAX_UPLOAD_BYTES = 10 * 1024 * 1024

  /** 上传成功后向编辑器插入媒体引用（用户需求：粘贴的文件/图片在 tiptap 里可见）：
   *  - 图片（image/* 或图片扩展名）→ markdown 图片 ![name](view-url)，
   *    经 /api/files/download?inline=1 在编辑器内 <img> 渲染（tiptap Image 扩展）
   *  - 其他文件 → markdown 引用链接 [name](download-url)（点击下载，attachment 语义）
   * URL 是同源相对路径（cookie 认证）—— 302 到签名 OSS URL；attachment upload_key
   * 随消息单独发送（agent 的语义载荷），编辑器里的链接仅供用户浏览。 */
  const insertUploadedMedia = useCallback(
    (res: { upload_key?: string; name?: string }, file: File) => {
      if (!editor || !res.upload_key) return
      const fileName = res.name ?? file.name
      const isImage = file.type.startsWith('image/') || /\.(png|jpe?g|gif|webp|avif|svg|bmp|ico)$/i.test(fileName)
      const url = `/api/files/download?key=${encodeURIComponent(res.upload_key)}${isImage ? '&inline=1' : ''}`
      // alt 文本里的 markdown 特殊字符（] / 换行）会破坏解析 —— 清洗
      const safeAlt = fileName.replace(/[[\]\n]/g, ' ')
      const md = isImage ? `![${safeAlt}](${url})` : `[${safeAlt}](${url})`
      const current = editor.getText()
      const sep = !current || current.endsWith('\n') ? '' : '\n'
      editor.chain().focus().insertContent(sep + md + '\n').run()
    },
    [editor],
  )

  const onPickFiles = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0) return
      // Client-side size pre-check (faster feedback than the server's 413)
      const all = Array.from(files)
      const oversized = all.filter((f) => f.size > MAX_UPLOAD_BYTES)
      const valid = all.filter((f) => f.size <= MAX_UPLOAD_BYTES)
      if (oversized.length > 0) {
        toast.error(t('agent.uploadTooLarge', { names: oversized.map((f) => f.name).join('、'), size: '10MB' }))
      }
      if (valid.length === 0) return
      setUploading(true)
      try {
        // 顺序上传（每个文件一个乐观 chip：上传中渲染进度条，完成后写 uploadKey + 插入编辑器媒体引用）
        for (const file of valid) {
          setPending((prev) => [...prev, {
            name: file.name,
            size: file.size,
            uploadKey: '',
            mime: file.type,
            uploading: true,
            progress: 0,
          }])
          try {
            const res = await onUpload(file, (loaded, total) => {
              setPending((prev) => prev.map((p) => (p.uploading && p.name === file.name)
                ? { ...p, progress: total > 0 ? loaded / total : 0 }
                : p))
            })
            // finalize：chip 落定（uploadKey 填充，进度 100%）
            setPending((prev) => prev.map((p) => (p.uploading && p.name === file.name)
              ? {
                ...p,
                uploading: false,
                progress: 1,
                uploadKey: res.upload_key ?? '',
                name: res.name ?? file.name,
                size: res.size ?? file.size,
                mime: res.mime ?? file.type,
              }
              : p))
            // 编辑器插入媒体引用（图片 → 内联渲染；文件 → 引用链接）
            insertUploadedMedia(res, file)
          } catch (e) {
            // 失败：移除该文件的乐观 chip + toast（其他文件继续）
            setPending((prev) => prev.filter((p) => !(p.uploading && p.name === file.name)))
            toast.error(`${file.name}: ${e instanceof Error ? e.message : t('agent.uploadFailed')}`)
          }
        }
      } finally {
        setUploading(false)
      }
    },
    [onUpload, t, insertUploadedMedia],
  )

  const canSend = hasContent || pending.some((p) => !p.uploading && p.uploadKey)

  // Keep the ref current — editorProps closures capture it once (paste/drop).
  onPickFilesRef.current = onPickFiles

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
            placeholder={t('agent.goal.inputPlaceholder')}
          />
          <span className="shrink-0 text-[10px] text-text-muted">{t('agent.goal.enterSaveHint')}</span>
        </div>
      )}
      {todoState ? <TodoPullOut todoState={todoState} hasGoal={!!goal || addingGoal} onSetGoal={onSetGoal ? () => {
        setAddingGoal(true)
      } : undefined} /> : null}

      {/* Input container — single rounded box with chips, editor, and inline buttons */}
      <div
        className={cn(
          'relative rounded-xl border bg-bg-secondary px-3 py-2 transition-[border-color,box-shadow]',
          goalMode
            ? 'border-accent/50 ring-1 ring-accent/20'
            : interruptMode
              ? 'border-violet-400/60 ring-1 ring-violet-500/30 shadow-[0_0_12px_rgba(139,92,246,0.15)]'
              : focused
                ? 'border-accent ring-1 ring-accent/30'
                : 'border-border',
          dragOverFiles && 'border-accent/70 ring-2 ring-accent/25',
        )}
      >
        {/* File drag-over hint — files dropped on the editor auto-upload as attachments */}
        {dragOverFiles && (
          <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-xl bg-bg-primary/75 backdrop-blur-[2px]">
            <span className="flex items-center gap-2 text-sm font-medium text-accent">
              <Paperclip className="size-4" />
              {t('agent.dropToUpload')}
            </span>
          </div>
        )}
        {/* Vision advisory: image attachments + current model's vision switch OFF →
            non-blocking hint (the images still send as name-only placeholders;
            the model will tell the user it can't see them). modelVision ===
            undefined = unknown state → no hint. */}
        {modelVision === false && pending.some(isImageAttachment) && (
          <div className="mb-2 flex items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-2.5 py-1.5" data-testid="vision-off-hint">
            <span className="shrink-0 text-[13px]" aria-hidden>👁</span>
            <span className="min-w-0 flex-1 text-[11px] leading-snug text-amber-600 dark:text-amber-400">
              {t('agent.visionOffHint')}
            </span>
          </div>
        )}
        {/* Attachment chips (inside container, above editor) */}
        {pending.length > 0 && (
          <div className="mb-2 flex flex-wrap gap-1.5">
            {pending.map((p, i) => (              <span
                key={`${p.uploadKey}-${i}`}
                className="inline-flex items-center gap-1 rounded-md bg-bg-tertiary px-2 py-1 text-xs text-text-secondary"
              >
                {p.uploading
                  ? <Loader2 className="size-3 animate-spin shrink-0" />
                  : <Paperclip className="size-3 shrink-0" />}
                <span className="max-w-[20ch] truncate">{p.name}</span>
                {p.uploading && (
                  <span
                    data-testid={`upload-progress-${p.name}`}
                    className="flex w-16 shrink-0 select-none items-center gap-1"
                    aria-label={`uploading ${(p.progress ?? 0)}`}
                  >
                    <span className="h-1 flex-1 overflow-hidden rounded-full bg-text-muted/20">
                      <span
                        className="block h-full rounded-full bg-accent transition-[width] duration-150"
                        style={{ width: `${Math.round((p.progress ?? 0) * 100)}%` }}
                      />
                    </span>
                    <span className="w-7 text-right text-[10px] tabular-nums text-text-muted">
                      {Math.round((p.progress ?? 0) * 100)}%
                    </span>
                  </span>
                )}
                <button
                  type="button"
                  aria-label="remove"
                  disabled={p.uploading}
                  onClick={() => setPending((prev) => prev.filter((_, idx) => idx !== i))}
                  className="text-text-muted hover:text-text-primary disabled:opacity-30"
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
          {/* Floating selection toolbar — link add/edit/unlink + inline marks.
              Hidden while the completion popup is open (it takes priority). */}
          <SelectionToolbar editor={editor} hidden={completion.visible} linkEditSignal={linkEditSignal} />
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
              aria-label={t('agent.goalModeAria')}
              onClick={() => setGoalMode((v) => !v)}
              className={cn(
                'size-9 rounded-md transition-all',
                goalMode
                  ? 'bg-accent/15 text-accent ring-1 ring-accent/40 shadow-[0_0_8px_rgba(var(--accent-rgb),0.3)]'
                  : 'text-text-muted hover:text-text-primary',
              )}
              title={goalMode ? t('agent.goalModeOn') : t('agent.goalModeOff')}
            >
              <Target className={cn('size-4', goalMode && 'animate-pulse [animation-duration:2s]')} />
            </Button>
            {/* ⚡ Interject / 💬 Queue mode toggle — icon-only on narrow screens
                (text overflows on mobile). Title attr carries the full label. */}
            {busy && onInterruptModeChange && (
              <button
                type="button"
                aria-label={interruptMode ? t('agent.switchToQueueMode') : t('agent.switchToInterjectMode')}
                onClick={() => onInterruptModeChange(!interruptMode)}
                className={cn(
                  'flex h-9 w-9 shrink-0 items-center justify-center rounded-md transition-all',
                  interruptMode
                    ? 'bg-violet-500/15 text-violet-600 ring-1 ring-violet-500/40 shadow-[0_0_8px_rgba(139,92,246,0.3)] dark:text-violet-400'
                    : 'bg-indigo-500/10 text-indigo-600 ring-1 ring-indigo-500/20 dark:text-indigo-400',
                  'hover:opacity-80',
                )}
                title={interruptMode ? t('agent.interjectModeHint') : t('agent.queueModeHint')}
              >
                {interruptMode ? <Zap className="size-4" /> : <Clock className="size-4" />}
              </button>
            )}
          </div>

          {/* Send / Cancel button: empty input + busy → Cancel (stop agent);
              has input → Send (queue/interject depending on mode). Single slot,
              no overflow — icon only. */}
          <div className="flex min-w-0 items-center gap-1">
            {trailingControls}
            <Button
              type="button"
              size="icon-sm"
              aria-label={hasContent && busy ? (interruptMode ? t('agent.interjectSend') : t('agent.queuedSend')) : busy ? t('common.cancel') : goalMode ? t('agent.setAsGoal') : t('agent.send')}
              disabled={hasContent ? !canSend : (busy ? cancelling : !canSend)}
              onClick={hasContent || !busy ? submit : onCancel}
              className={cn(
                'size-9 shrink-0 rounded-md transition-all duration-150 active:scale-95',
                hasContent || !busy
                  ? goalMode
                    ? 'bg-accent text-accent-foreground shadow-[0_0_12px_rgba(var(--accent-rgb),0.4)]'
                    : interruptMode
                      ? 'bg-violet-600 text-white shadow-[0_0_12px_rgba(139,92,246,0.4)] dark:bg-violet-500'
                      : busy
                        ? 'bg-indigo-500/80 text-white shadow-[0_0_8px_rgba(99,102,241,0.3)] dark:bg-indigo-500'
                        : 'bg-accent text-accent-foreground'
                  : 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
                (hasContent ? !canSend : (busy ? cancelling : !canSend)) && 'opacity-40',
              )}
            >
              {(cancelling && !hasContent && busy) ? (
                <Loader2 className="size-4 animate-spin" />
              ) : hasContent || !busy ? (
                goalMode ? <Target className="size-4" /> : interruptMode ? <Zap className="size-4" /> : busy ? <Mail className="size-4" /> : <Send className="size-4" />
              ) : (
                <Square className="size-4" />
              )}
            </Button>
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
