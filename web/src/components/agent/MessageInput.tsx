/**
 * MessageInput — the Agent panel composer (Spec 4 §3.7).
 *
 * Multi-line textarea (Ctrl/Cmd+Enter to send), a file-attach button (uploads
 * via POST /api/files/upload and stashes the returned key to attach to the next
 * message), and a cancel button shown while the agent is busy (sends a WS
 * `cancel`). Pending uploads show a small chip list.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Loader2, Paperclip, Send, Square, X } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { useCwd } from '@/providers/CwdProvider'
import { useWSConnection } from '@/hooks/useWSConnection'
import type { Attachments } from '@/hooks/useChatMessages'
import { cn } from '@/lib/utils'
import { TodoPullOut } from './TodoPullOut'
import { CompletionPopup } from './CompletionPopup'
import { useCompletion } from '@/hooks/useCompletion'
import type { TodoState } from '@/hooks/useTodos'

interface MessageInputProps {
  /** True while the agent is producing output; shows the cancel button. */
  busy: boolean
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
  /** TODO state from the progress snapshot; null hides the pull-out panel. */
  todoState?: TodoState | null
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

export function MessageInput({ busy, onSend, onCancel, onRewindLatest, onOpenTasks, onUpload, todoState, draft, onDraftConsumed, sessionKey }: MessageInputProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const { cwd } = useCwd()
  const draftStorageKey = sessionKey ? `xbot:draft:${sessionKey}` : null
  const [value, setValue] = useState(() => {
    if (draft !== undefined) return draft
    if (draftStorageKey) {
      try {
        const saved = localStorage.getItem(draftStorageKey)
        if (saved) return saved
      } catch { /* ignore */ }
    }
    return ''
  })
  const [pending, setPending] = useState<PendingAttachment[]>([])
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const completion = useCompletion({ value, setValue, textareaRef, ws, cwd })

  // Persist draft to localStorage so input survives refresh / session switch.
  useEffect(() => {
    if (!draftStorageKey) return
    try {
      if (value) localStorage.setItem(draftStorageKey, value)
      else localStorage.removeItem(draftStorageKey)
    } catch { /* ignore */ }
  }, [value, draftStorageKey])

  // Auto-grow the textarea up to a max height.
  const resize = useCallback(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`
  }, [])

  useEffect(() => {
    if (draft === undefined) return
    setValue(draft)
    onDraftConsumed?.()
    return scheduleTextareaResize(() => {
      resize()
      textareaRef.current?.focus()
    })
  }, [draft, onDraftConsumed, resize])

  const submit = useCallback(() => {
    const text = value.trim()
    if (!text && pending.length === 0) return
    if (text === '/rewind' && pending.length === 0 && onRewindLatest) {
      if (!busy) onRewindLatest()
      setValue('')
      scheduleTextareaResize(resize)
      return
    }
    if (text === '/cancel' && pending.length === 0) {
      if (busy) onCancel()
      setValue('')
      scheduleTextareaResize(resize)
      return
    }
    if (text === '/tasks' && pending.length === 0 && onOpenTasks) {
      onOpenTasks()
      setValue('')
      scheduleTextareaResize(resize)
      return
    }
    if (busy && text === '/new' && pending.length === 0) {
      toast.error('Agent is busy')
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
    onSend(text, attachments)
    setValue('')
    setPending([])
    scheduleTextareaResize(resize)
  }, [busy, value, pending, onCancel, onRewindLatest, onOpenTasks, onSend, resize])

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Let completion handle navigation keys first
    if (completion.handleKeyDown(e)) return
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

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

  const canSend = value.trim().length > 0 || pending.length > 0

  return (
    <div className="border-t border-border bg-bg-primary px-3 py-2.5">
      {todoState && <TodoPullOut todoState={todoState} />}
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

      <div className="relative flex items-end gap-2">
        <CompletionPopup
          candidates={completion.candidates}
          selectedIndex={completion.selectedIndex}
          visible={completion.visible}
          triggerType={completion.triggerType}
          onSelect={completion.completeCandidate}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={t('agent.attach')}
          disabled={uploading}
          onClick={() => fileRef.current?.click()}
        >
          {uploading ? <Loader2 className="size-4 animate-spin" /> : <Paperclip className="size-4" />}
        </Button>
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

        <textarea
          ref={textareaRef}
          value={value}
          onChange={(e) => {
            setValue(e.target.value)
            resize()
          }}
          onKeyDown={onKeyDown}
          rows={1}
          placeholder={t('agent.inputPlaceholder')}
          className={cn(
            'max-h-[200px] flex-1 resize-none rounded-lg border border-border bg-bg-secondary px-3 py-2',
            'text-sm text-text-primary placeholder:text-text-muted',
            'focus-visible:border-accent focus-visible:outline-none',
          )}
        />

        {busy ? (
          <Button
            type="button"
            variant="destructive"
            size="icon-sm"
            aria-label={t('common.cancel')}
            onClick={onCancel}
          >
            <Square className="size-4" />
          </Button>
        ) : (
          <Button
            type="button"
            size="icon-sm"
            aria-label={t('agent.send')}
            disabled={!canSend}
            onClick={submit}
          >
            <Send className="size-4" />
          </Button>
        )}
      </div>
    </div>
  )
}

function scheduleTextareaResize(fn: () => void): () => void {
  let cancelled = false
  const timers: number[] = []
  const run = () => {
    if (!cancelled) fn()
  }
  run()
  let raf = requestAnimationFrame(() => {
    run()
    raf = requestAnimationFrame(() => {
      run()
      raf = requestAnimationFrame(run)
      timers.push(raf)
    })
    timers.push(raf)
  })
  timers.push(raf)
  for (const delay of [80, 180]) {
    timers.push(window.setTimeout(run, delay))
  }
  return () => {
    cancelled = true
    for (const id of timers) {
      cancelAnimationFrame(id)
      clearTimeout(id)
    }
  }
}
