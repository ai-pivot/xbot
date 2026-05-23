import { REPLY_PREVIEW_LENGTH, REPLY_INDICATOR_LENGTH, NOTIFICATION_PREVIEW_LENGTH, PRESET_TOOLTIP_LENGTH, VIRTUAL_ROW_HEIGHT_USER, VIRTUAL_ROW_HEIGHT_ASSISTANT } from './constants'
import { useTranslation } from './i18n'
import { useEffect, useRef, useState, useCallback, useMemo, lazy, Suspense, memo } from 'react'
import { useVirtualizer, type Virtualizer } from '@tanstack/react-virtual'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { useWebSocket } from './hooks/useWebSocket'
import { useChatMessageHandler } from './hooks/useChatMessageHandler'
import { useToast } from './contexts/ToastContext'
import { useKeyboardShortcuts } from './hooks/useKeyboardShortcuts'
import { useNetworkStatus } from './hooks/useNetworkStatus'
import type { TiptapEditorHandle } from './components/TiptapEditor'
import type { PresetCommand, Message, Turn, ThreadMessage } from './types'
import { useNotification } from './hooks/useNotification'
import ReplyPreview from './components/ReplyPreview'
import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'
import type { WsSubAgent } from './components/ProgressPanel'
import { TodoBar } from './components/TodoBar'
import { SubAgentPanel } from './components/SubAgentPanel'
import { formatRelativeTime, formatFileSize, normalizeIterationHistory, createResetProgress, exportAsMarkdown, exportAsJSON, downloadFile } from './utils'
import { getCodeBlockProps } from './components/CodeBlock'
import ProgressPanel from './components/ProgressPanel'
import AssistantTurn from './components/AssistantTurn'
import ChatSidebar from './components/ChatSidebar'
import TiptapEditor from './components/TiptapEditor'
import SwipeableMessage from './components/SwipeableMessage'
import ContextMenu, { type ContextMenuItem } from './components/ContextMenu'
import AskUserPanel from './components/AskUserPanel'
import FileUpload, { uploadFile, usePasteUpload, type PendingFile } from './components/FileUpload'
import { AudioPlayer, VideoPlayer } from './components/MediaPlayer'

const SettingsPanel = lazy(() => import('./components/SettingsPanel'))
const CommandPalette = lazy(() => import('./components/CommandPalette'))
import OnboardingTip from './components/OnboardingTip'
import ThreadPanel from './components/ThreadPanel'
import NotificationPanel from './components/NotificationPanel'
import { useSoundFeedback } from './hooks/useSoundFeedback'
import { useNotificationContext } from './contexts/NotificationContext'

const codeBlockComponents = getCodeBlockProps()

interface ChatPageProps {
  onLogout: () => void
}



export function groupMessagesIntoTurns(messages: Message[]): Turn[] {
  const turns: Turn[] = []
  let currentAssistant: Message[] = []

  for (const msg of messages) {
    if (msg.type === 'user') {
      if (currentAssistant.length > 0) {
        turns.push({ type: 'assistant', messages: currentAssistant })
        currentAssistant = []
      }
      turns.push({ type: 'user', message: msg })
    } else {
      currentAssistant.push(msg)
    }
  }
  if (currentAssistant.length > 0) {
    turns.push({ type: 'assistant', messages: currentAssistant })
  }
  return turns
}

// --- Attachment parsing & rendering ---

export interface ParsedAttachment {
  type: 'image' | 'file'
  name: string
  url?: string
  size?: number
  raw: string
}

const reAttachment = /<(image|file)\s+([^>]*?)\/?>/gi

export function parseAttachments(content: string): { attachments: ParsedAttachment[]; cleanContent: string } {
  const attachments: ParsedAttachment[] = []
  let cleanContent = content

  // Remove duplicate markdown image syntax that follows <image> tags
  // (backend sends both XML and ![name](url))
  cleanContent = cleanContent.replace(/(<image\s[^>]*?\/?>)\s*\n?!?\[[^\]]*\]\([^)]+\)/gi, '$1')

  cleanContent = cleanContent.replace(reAttachment, (match, type, attrs) => {
    const nameMatch = attrs.match(/(?:name|filename)="([^"]*)"/)
    const urlMatch = attrs.match(/url="([^"]*)"/)
    const sizeMatch = attrs.match(/size="(\d+)"/)
    const name = nameMatch?.[1] || (type === 'image' ? '图片' : '文件')
    const url = urlMatch?.[1]
    if (url && !/^https?:\/\//i.test(url)) {
      // Skip non-HTTP(S) URLs (e.g. javascript:, data:, file:)
      return match
    }
    const size = sizeMatch ? parseInt(sizeMatch[1], 10) : undefined
    const idx = attachments.length
    attachments.push({ type: type as 'image' | 'file', name, url, size, raw: match })
    return `{{attachment-${idx}}}`
  })

  // Clean up empty lines left by removed tags
  cleanContent = cleanContent.replace(/\n{2,}/g, '\n\n').trim()

  return { attachments, cleanContent }
}

function AttachmentCard({ attachment, onPreview }: { attachment: ParsedAttachment; onPreview?: (url: string) => void }) {
  if (attachment.type === 'image' && attachment.url) {
    return (
      <div className="attachment-card attachment-image">
        <img
          src={attachment.url}
          alt={attachment.name}
          className="attachment-img"
          loading="lazy"
          onClick={() => onPreview?.(attachment.url!) || window.open(attachment.url, '_blank')}
        />
        <div className="attachment-meta">
          <span className="truncate">{attachment.name}</span>
          {attachment.size != null && <span>{formatFileSize(attachment.size)}</span>}
        </div>
      </div>
    )
  }

  // Audio media detection
  if (attachment.url && /\.(mp3|wav|ogg|m4a|aac|flac)$/i.test(attachment.name)) {
    return (
      <div className="attachment-card attachment-media">
        <AudioPlayer src={attachment.url} fileName={attachment.name} />
      </div>
    )
  }

  // Video media detection
  if (attachment.url && /\.(mp4|webm|mov|avi|mkv)$/i.test(attachment.name)) {
    return (
      <div className="attachment-card attachment-media">
        <VideoPlayer src={attachment.url} fileName={attachment.name} />
      </div>
    )
  }

  return (
    <a
      href={attachment.url || '#'}
      target="_blank"
      rel="noopener noreferrer"
      className="attachment-card attachment-file"
    >
      <div className="attachment-file-icon">
        {attachment.name.match(/\.(pdf)$/i) ? '📄' :
         attachment.name.match(/\.(doc|docx)$/i) ? '📝' :
         attachment.name.match(/\.(xls|xlsx|csv)$/i) ? '📊' :
         attachment.name.match(/\.(zip|tar|gz|rar|7z)$/i) ? '📦' :
         attachment.name.match(/\.(mp4|avi|mov|mkv)$/i) ? '🎬' :
         attachment.name.match(/\.(mp3|wav|flac)$/i) ? '🎵' :
         '📎'}
      </div>
      <div className="attachment-file-info">
        <span className="truncate">{attachment.name}</span>
        {attachment.size != null && <span>{formatFileSize(attachment.size)}</span>}
      </div>
    </a>
  )
}

const UserMessageContent = memo(function UserMessageContent({ content, onPreview }: { content: string; onPreview?: (url: string) => void }) {
  const { attachments, cleanContent } = parseAttachments(content)

  // If no attachments found, render as normal markdown
  if (attachments.length === 0) {
    return <Markdown remarkPlugins={[remarkGfm]} components={codeBlockComponents}>{content}</Markdown>
  }

  // Split clean content by attachment placeholders and render interleaved
  const parts = cleanContent.split(/(\{\{attachment-\d+\}\})/)
  const elements: React.ReactNode[] = []

  for (const part of parts) {
    const match = part.match(/^\{\{attachment-(\d+)\}\}$/)
    if (match) {
      const idx = parseInt(match[1], 10)
      if (idx < attachments.length) {
        elements.push(<AttachmentCard key={`att-${idx}`} attachment={attachments[idx]} onPreview={onPreview} />)
      }
    } else if (part.trim()) {
      elements.push(
        <Markdown key={`md-${elements.length}`} remarkPlugins={[remarkGfm]} components={codeBlockComponents}>
          {part}
        </Markdown>
      )
    }
  }

  return <>{elements}</>
})

export default function ChatPage({ onLogout }: ChatPageProps) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState<Message[]>([])
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState<WsProgressPayload | null>(null)
  const [liveIterations, _setLiveIterations] = useState<IterationSnapshot[]>([])
  const liveIterationsRef = useRef<IterationSnapshot[]>([])
  // Keep ref in sync so we can read the latest value synchronously
  // (React setState updater callbacks are async and cannot be relied upon).
  const setLiveIterationsSync = useCallback((updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => {
    _setLiveIterations(prev => {
      const next = typeof updater === 'function' ? updater(prev) : updater
      liveIterationsRef.current = next
      return next
    })
  }, [])
  const prevIterationRef = useRef<number>(-1)
  const progressRef = useRef<WsProgressPayload | null>(null) // sync ref to avoid stale closures
  const reasoningRef = useRef<string>('') // accumulated reasoning from stream_content
  const streamingContentRef = useRef<string>('') // accumulated content from stream_content
  const loadHistoryIdRef = useRef(0) // race protection for loadHistory
  const virtualizerRef = useRef<Virtualizer<HTMLDivElement, Element> | null>(null) // ref for scroll-to-bottom in loadHistory
  const turnsCountRef = useRef(0) // track turns count for scroll-to-bottom
  const resetProgress = createResetProgress({
    setProgress: (v) => setProgress(v),
    setLiveIterations: setLiveIterationsSync,
    prevIterationRef,
    progressRef,
    reasoningRef,
    streamingContentRef,
  })
  const [autoScroll, setAutoScroll] = useState(true)
  const autoScrollRef = useRef(true)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [dragActive, setDragActive] = useState(false)
  const dragCountRef = useRef(0)
  const [nickname, setNickname] = useState<string>(() => localStorage.getItem('xbot-nickname') || '')
  const editorRef = useRef<TiptapEditorHandle>(null)
  const [presets, setPresets] = useState<PresetCommand[]>([])
  const [askUser, setAskUser] = useState<{ questions: { question: string; options?: string[] }[]; answers: Record<string, string>; currentQ: number } | null>(null)
  const [currentModel, setCurrentModel] = useState('')
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [modelDropdownOpen, setModelDropdownOpen] = useState(false)
  const [currentChatID, setCurrentChatID] = useState<string>('')
  const currentChatIDRef = useRef(currentChatID)
  // Keep ref in sync with state
  currentChatIDRef.current = currentChatID
  const [replyingTo, setReplyingTo] = useState<{ id: string; content: string; type: 'user' | 'assistant' } | null>(null)
  const [contextInfo, setContextInfo] = useState<{ prompt_tokens: number; max_tokens: number; usage_pct: number; source: string } | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false)
  const [previewImage, setPreviewImage] = useState<string | null>(null)
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; items: ContextMenuItem[] } | null>(null)
  const [threadOpen, setThreadOpen] = useState(false)
  const [threadParentMsg, setThreadParentMsg] = useState<Message | null>(null)
  const [threadMessages, setThreadMessages] = useState<Record<string, ThreadMessage[]>>({})
  const [notificationOpen, setNotificationOpen] = useState(false)
  const [todos, setTodos] = useState<{ id: number; text: string; done: boolean }[]>([])
  const [subAgents, setSubAgents] = useState<WsSubAgent[]>([])
  const { play: playSound } = useSoundFeedback()
  const { addNotification } = useNotificationContext()

  // Unified toast via context
  const { showToast } = useToast()



  // --- Thread handlers ---
  const handleOpenThread = useCallback((msg: Message) => {
    setThreadParentMsg(msg)
    setThreadOpen(true)
  }, [])

  const handleSendThreadReply = useCallback((parentId: string, content: string) => {
    const newMsg: ThreadMessage = {
      id: `thread-${Date.now()}`,
      parentId,
      type: 'user',
      content,
      ts: Date.now() / 1000,
      author: 'You',
    }
    setThreadMessages(prev => ({
      ...prev,
      [parentId]: [...(prev[parentId] || []), newMsg],
    }))
  }, [])

  // --- Export callbacks ---
  const handleExportMarkdown = useCallback(() => {
    const md = exportAsMarkdown(messages)
    const date = new Date().toISOString().slice(0, 10)
    downloadFile(md, `chat-${date}.md`, 'text/markdown')
  }, [messages])

  const handleExportJSON = useCallback(() => {
    const json = exportAsJSON(messages)
    const date = new Date().toISOString().slice(0, 10)
    downloadFile(json, `chat-${date}.json`, 'application/json')
  }, [messages])

  const handleModelSwitch = useCallback(async (model: string) => {
    setModelDropdownOpen(false)
    if (model === currentModel) return
    try {
      const resp = await fetch('/api/llm-config/model', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model }),
      })
      const data = await resp.json()
      if (data.ok) {
        setCurrentModel(model)
        showToast(t('switchedToModel', { model }), 'success')
      } else {
        showToast(data.error || t('switchFailed'), 'error')
      }
    } catch {
      showToast(t('switchFailed'), 'error')
    }
  }, [currentModel, showToast])

  // --- Load available models on mount ---
  useEffect(() => {
    fetch('/api/llm-config')
      .then(r => r.json())
      .then(data => {
        if (data.ok) {
          setCurrentModel(data.model || '')
          setAvailableModels(data.models || [])
        }
      })
      .catch((err) => { console.warn('[ChatPage] failed to load LLM config:', err) })
  }, [])

  const messagesContainerRef = useRef<HTMLDivElement>(null)

  // --- Scroll management ---
  const isNearBottom = useCallback(() => {
    const el = messagesContainerRef.current
    if (!el) return true
    return el.scrollHeight - el.scrollTop - el.clientHeight <= 150
  }, [])

  const scrollToBottom = useCallback((behavior: ScrollBehavior = 'instant') => {
    const el = messagesContainerRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior })
  }, [])

  // Scroll handler: updates ref immediately (no re-render) for behavior control,
  // debounces state update (300ms) only for scroll-to-bottom button visibility.
  // CRITICAL: Never call setAutoScroll synchronously here — virtualizer's
  // resizeItem → _scrollToOffset triggers scroll events, and synchronous
  // setState causes re-render → resizeItem → infinite loop.
  // Also skips entirely during history load (virtualizer is actively measuring).
  const scrollDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const loadingHistoryRef = useRef(false)
  const handleContainerScroll = useCallback(() => {
    // Skip during history load — virtualizer is actively measuring items
    if (loadingHistoryRef.current) return
    // Update ref immediately — the auto-scroll effect reads from this
    const near = isNearBottom()
    autoScrollRef.current = near
    // Debounce state update for button visibility — only after scroll is stable for 300ms
    if (scrollDebounceRef.current) clearTimeout(scrollDebounceRef.current)
    scrollDebounceRef.current = setTimeout(() => {
      // Double-check we're still not loading history
      if (!loadingHistoryRef.current) {
        setAutoScroll(near)
      }
    }, 300)
  }, [isNearBottom])

  // Auto-scroll during streaming/progress updates — only when actively loading.
  // Reads from autoScrollRef (not state) to avoid re-render → resizeItem → infinite loop.
  const scrollRafRef = useRef<number>(0)
  const prevMsgCountRef = useRef(0)
  useEffect(() => {
    // Skip during initial history load — the virtualizer is actively measuring
    // items and our scrollToBottom fights its _scrollToOffset compensation
    if (loadingHistoryRef.current) return
    if (!autoScrollRef.current) return
    // Only auto-scroll when actively streaming (loading or progress) OR when new messages arrive
    const isActive = loading || progress !== null
    const msgCount = messages.length
    const hasNewMessages = msgCount > prevMsgCountRef.current
    prevMsgCountRef.current = msgCount
    // Skip if not active AND no new messages — prevents reflow-triggered scrolls
    if (!isActive && !hasNewMessages) return
    if (scrollRafRef.current) cancelAnimationFrame(scrollRafRef.current)
    scrollRafRef.current = requestAnimationFrame(() => scrollToBottom('instant'))
    return () => { if (scrollRafRef.current) cancelAnimationFrame(scrollRafRef.current) }
  }, [messages, progress, scrollToBottom, loading])

  // --- Fetch context info ---
  const fetchContextInfo = useCallback(() => {
    fetch('/api/context-info')
      .then(r => r.json())
      .then(data => {
        if (data.ok) {
          setContextInfo({
            prompt_tokens: data.prompt_tokens || 0,
            max_tokens: data.max_tokens || 0,
            usage_pct: data.usage_pct || 0,
            source: data.source || 'none',
          })
        }
      })
      .catch((err) => { console.warn('[ChatPage] failed to fetch context info:', err) })
  }, [])

  // --- WebSocket hook ---
  const lastSeqRef = useRef(0)
  const { onMessage } = useChatMessageHandler({
    setMessages, setLoading, setProgress, setAskUser,
    prevIterationRef, progressRef, reasoningRef, streamingContentRef, liveIterationsRef,
    fetchContextInfo, resetProgress, setLiveIterationsSync, showToast, lastSeqRef,
    setTodos, setSubAgents, currentChatIDRef,
  })
  const {
    connected,
    reconnecting,
    serverStopped,
    reconnectAttempt,
    nextReconnectIn,
    send: wsSend,
    disconnect: wsDisconnect,
  } = useWebSocket({ onMessage, lastSeqRef })



  // --- Network status (browser online/offline) ---
  const { online } = useNetworkStatus(connected, reconnecting, serverStopped, reconnectAttempt, nextReconnectIn)
  const { permission: notifPermission, requestPermission: requestNotifPermission, notify: sendNotif } = useNotification()
  // --- Desktop notification on new assistant message when backgrounded ---
  const prevMessageCountRef = useRef(0)
  useEffect(() => {
    const currentCount = messages.length
    if (currentCount > prevMessageCountRef.current && prevMessageCountRef.current > 0) {
      const lastMsg = messages[messages.length - 1]
      if (lastMsg?.type === 'assistant') {
        playSound('receive')
        addNotification({ type: 'message', title: 'New Reply', body: lastMsg.content.slice(0, 100), messageId: lastMsg.id })
      }
    }
    prevMessageCountRef.current = currentCount
  }, [messages.length, playSound, addNotification])

  // --- Desktop notification on new assistant message when backgrounded (original logic) ---
  const prevMsgCountRef2 = useRef(0)
  useEffect(() => {
    const currentCount = messages.length
    if (currentCount <= prevMsgCountRef2.current) {
      prevMessageCountRef.current = currentCount
      return
    }
    prevMessageCountRef.current = currentCount
    // Only notify for new assistant messages (not user messages or system messages)
    const lastMsg = messages[messages.length - 1]
    if (lastMsg && lastMsg.type === 'assistant' && !loading) {
      sendNotif(t('newMessageNotification'), {
        body: lastMsg.content.slice(0, NOTIFICATION_PREVIEW_LENGTH) || '...',
      })
    }
  }, [messages, loading, sendNotif])


  // --- Load history (extracted for reuse on chat switch) ---
  const loadHistory = useCallback(() => {
    const currentId = ++loadHistoryIdRef.current
    loadingHistoryRef.current = true
    fetch('/api/history')
      .then((r) => r.json())
      .then((data) => {
        if (loadHistoryIdRef.current !== currentId) return // race protection: discard stale response
        if (data.ok && data.messages) {
          // Recover currentChatID from backend on page refresh
          if (data.chat_id && !currentChatIDRef.current) {
            setCurrentChatID(data.chat_id)
          }
          const hist: Message[] = data.messages
            .filter((m: { role: string; content?: string; tool_calls?: string; detail?: string; display_only?: number }) => {
              if (m.role === 'tool') return false
              if (m.role === 'assistant' && m.tool_calls && !m.detail) return false
              if (m.role === 'assistant' && m.display_only && !m.content && !m.detail) return false
              return true
            })
            .map((m: { id: number; role: string; content: string; detail?: string; created_at?: string }) => {
              const msg: Message = {
                id: `hist-${m.id}`,
                type: m.role === 'user' ? 'user' : m.role === 'assistant' ? 'assistant' : 'system',
                content: m.content,
                ts: m.created_at ? Math.floor(new Date(m.created_at).getTime() / 1000) : undefined,
              }
              if (m.detail) {
                try {
                  msg.iterationHistory = normalizeIterationHistory(JSON.parse(m.detail))
                } catch { /* ignore */ }
              }
              return msg
            })
          setMessages(hist)
          const isProcessing = data.processing === true
          if (isProcessing) {
            setLoading(true)
          } else {
            // Backend is idle — force clear any stale loading state (e.g. after page refresh)
            setLoading(false)
            resetProgress()
            streamingContentRef.current = ''
          }
          if (isProcessing && data.active_progress) {
            const ap = data.active_progress
            progressRef.current = {
              phase: ap.phase || 'running',
              iteration: ap.iteration || 0,
              thinking: ap.thinking || '',
              active_tools: (ap.active_tools || []).map((t: { name: string; label: string; status: string; summary: string }) => ({
                name: t.name, label: t.label, status: t.status, summary: t.summary,
              })),
              completed_tools: (ap.completed_tools || []).map((t: { name: string; label: string; status: string; summary: string }) => ({
                name: t.name, label: t.label, status: t.status, summary: t.summary,
              })),
            }
            prevIterationRef.current = ap.iteration || 0
            if (ap.thinking) {
              reasoningRef.current = ap.thinking
            }
            setProgress(progressRef.current)
            if (ap.stream_content) {
              streamingContentRef.current = ap.stream_content
              setMessages(prev => [...prev, {
                id: '__streaming__',
                type: 'assistant' as const,
                content: ap.stream_content,
              }])
            }
            if (ap.iteration_history && ap.iteration_history.length > 0) {
              const restoredIterations: IterationSnapshot[] = ap.iteration_history.map(
                (iter: { iteration: number; thinking?: string; reasoning?: string; completed_tools?: { name: string; label?: string; status: string; summary?: string }[] }) => ({
                  iteration: iter.iteration,
                  thinking: iter.thinking || '',
                  reasoning: iter.reasoning || '',
                  tools: (iter.completed_tools || []).map(t => ({
                    name: t.name,
                    label: t.label,
                    status: t.status,
                    summary: t.summary,
                  })),
                })
              )
              setLiveIterationsSync(restoredIterations)
            }
          }
          if (data.last_seq) {
            lastSeqRef.current = data.last_seq
          }
          // Scroll to bottom after history loads. The virtualizer's total size starts
          // from estimates and grows as items are measured, so a single scrollTo may
          // not reach the true bottom. We retry until the scroll position stabilizes.
          let scrollAttempts = 0
          const maxScrollAttempts = 20
          const scrollHistoryToBottom = () => {
            const el = messagesContainerRef.current
            if (!el) return
            const prevTop = el.scrollTop
            el.scrollTo({ top: el.scrollHeight, behavior: 'instant' as ScrollBehavior })
            // If scrollTop didn't change (already at max) or we've tried enough, done
            scrollAttempts++
            if ((el.scrollTop === prevTop && el.scrollTop > 0) || scrollAttempts >= maxScrollAttempts) {
              loadingHistoryRef.current = false
              autoScrollRef.current = true
              setAutoScroll(true)
              return
            }
            requestAnimationFrame(scrollHistoryToBottom)
          }
          setTimeout(scrollHistoryToBottom, 300)
        }
      })
      .catch((err) => {
        if (loadHistoryIdRef.current !== currentId) return
        console.warn('[ChatPage] failed to load history:', err)
      })
    fetchContextInfo()
  }, [scrollToBottom, fetchContextInfo])

  // --- Load history on mount ---
  useEffect(() => {
    loadHistory()
  }, [loadHistory])

  // --- Send message ---
  const handleSend = useCallback((content: string) => {
    // Slash commands
    const trimmed = content.trim()
    if (trimmed.startsWith('/')) {
      const cmd = trimmed.toLowerCase()
      if (cmd === '/clear') {
        // Clear both frontend state and backend history
        setMessages([])
        resetProgress()
        setTodos([])
        setSubAgents([])
        setLoading(false)
        fetch('/api/history', { method: 'DELETE' }).catch((err) => { console.warn('[ChatPage] failed to clear history:', err) })
        showToast(t('conversationCleared'), 'info')
        return
      }
      if (cmd === '/new') {
        // Create a new chatroom via REST API (aligned with sidebar handleCreate)
        ;(async () => {
          try {
            const resp = await fetch('/api/chats', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ label: `Chat ${new Date().toLocaleDateString()} ${new Date().toLocaleTimeString()}` }),
            })
            const data = await resp.json()
            if (data.ok && data.chat_id) {
              await fetch(`/api/chats/${encodeURIComponent(data.chat_id)}/switch`, { method: 'POST' })
              setCurrentChatID(data.chat_id)
              setMessages([])
              resetProgress()
              setTodos([])
              setSubAgents([])
              setLoading(false)
              setContextInfo(null)
              streamingContentRef.current = ''
              reasoningRef.current = ''
            }
          } catch (err) { console.warn('[ChatPage] /new createChat failed:', err) }
        })()
        showToast(t('newConversation'), 'info')
        return
      }
      if (cmd === '/help') {
        const helpMsg: Message = {
          id: `help-${Date.now()}`,
          type: 'system',
          content: `## ${t("helpTitle")}\n\n| /clear | ${t("cmdClear")} |\n| /new | ${t("cmdNew")} |\n| /compact | ${t("cmdCompact")} |\n| /help | ${t("cmdHelp")} |\n| /cancel | ${t("cancelGeneration")} |`,
          ts: Math.floor(Date.now() / 1000),
        }
        setMessages(prev => [...prev, helpMsg])
        return
      }
      // For /compact, /cancel, /model, /models — send as normal message (backend handles them)
    }

    const userMsg: Message = {
      id: `user-${Date.now()}`,
      type: 'user',
      content,
      ts: Math.floor(Date.now() / 1000),
      ...(replyingTo ? { replyTo: { id: replyingTo.id, content: replyingTo.content.slice(0, REPLY_PREVIEW_LENGTH), type: replyingTo.type } } : {}),
    }
    setReplyingTo(null)
    setMessages((prev) => [...prev, userMsg])
    resetProgress()
    setTodos([])
    setSubAgents([])
    setLoading(true)
    setAutoScroll(true); autoScrollRef.current = true

    const payload: { type: string; content: string; channel?: string; chat_id?: string; file_ids?: string[]; file_names?: string[]; file_sizes?: number[]; upload_keys?: string[]; file_mimes?: string[] } = {
      type: 'message',
      content,
      channel: 'web',
      chat_id: currentChatIDRef.current || undefined,
    }
    if (pendingFiles.length > 0) {
      // Separate local files from OSS files
      const localFiles = pendingFiles.filter((f) => !f.isOSS)
      const ossFiles = pendingFiles.filter((f) => f.isOSS)

      if (localFiles.length > 0) {
        payload.file_ids = localFiles.map((f) => f.id)
        payload.file_names = localFiles.map((f) => f.name)
      }
      if (ossFiles.length > 0) {
        payload.upload_keys = ossFiles.map((f) => f.uploadKey!)
        payload.file_names = [...(payload.file_names || []), ...ossFiles.map((f) => f.name)]
        payload.file_sizes = [...(payload.file_sizes || []), ...ossFiles.map((f) => f.size)]
        payload.file_mimes = [...(payload.file_mimes || []), ...ossFiles.map((f) => f.mime || '')]
      }
      setPendingFiles([])
    }

    wsSend(payload)

    setTimeout(() => scrollToBottom(isNearBottom() ? 'instant' : 'smooth'), 50)
  }, [scrollToBottom, isNearBottom, pendingFiles, wsSend, resetProgress, showToast, replyingTo])

  // --- Cancel generation ---
  const handleCancel = useCallback(() => {
    wsSend({ type: 'cancel', channel: 'web', chat_id: currentChatIDRef.current || undefined })
    setLoading(false)
    resetProgress()
    // Remove streaming placeholder if present
    setMessages(prev => prev.filter(m => m.id !== '__streaming__'))
  }, [wsSend, resetProgress])


  // --- Delete message ---
  const handleDeleteMessage = useCallback((messageId: string) => {
    const idx = messages.findIndex(m => m.id === messageId)
    if (idx === -1) return
    // Remove this message and all after it
    setMessages(prev => prev.slice(0, idx))
    // TODO: Backend sync — fetch(`/api/messages/${messageId}`, { method: 'DELETE' })
    showToast(t('messageDeleted'), 'success')
  }, [messages, showToast])

  // --- Regenerate message ---
  const handleRegenerate = useCallback((messageId: string) => {
    const idx = messages.findIndex(m => m.id === messageId)
    if (idx === -1) return

    // Find the preceding user message
    let userIdx = -1
    for (let i = idx - 1; i >= 0; i--) {
      if (messages[i].type === 'user') {
        userIdx = i
        break
      }
    }
    if (userIdx === -1) {
      showToast(t('regenerateFailed'), 'error')
      return
    }

    const userContent = messages[userIdx].content
    // Remove everything from user message onward
    setMessages(prev => prev.slice(0, userIdx))
    resetProgress()
    setLoading(true)
    setAutoScroll(true); autoScrollRef.current = true
    // Resend the user message
    wsSend({ type: 'message', content: userContent })
  }, [messages, wsSend, resetProgress, showToast])

  // --- Reply helpers ---
  const handleReplyToMessage = useCallback((msgId: string, msgContent: string, msgType: 'user' | 'assistant') => {
    setReplyingTo({ id: msgId, content: msgContent, type: msgType })
    editorRef.current?.focus()
  }, [])

  const handleCancelReply = useCallback(() => {
    setReplyingTo(null)
  }, [])

  // --- Preset commands ---
  useEffect(() => {
    fetch('/api/settings')
      .then(r => r.json())
      .then(data => {
        if (data.ok && data.settings?.preset_commands) {
          try {
            const parsed = JSON.parse(data.settings.preset_commands)
            if (Array.isArray(parsed)) setPresets(parsed)
          } catch { /* ignore */ }
        }
      })
      .catch((err) => { console.warn('[ChatPage] failed to load settings:', err) })
  }, [])

  const handlePresetClick = useCallback((preset: PresetCommand) => {
    if (preset.fill) {
      editorRef.current?.setContent(preset.content)
    } else {
      handleSend(preset.content)
    }
  }, [handleSend])

  // --- Logout ---
  const handleLogout = async () => {
    wsDisconnect()
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch { /* best effort — proceed to logout anyway */ }
    onLogout()
  }

  // --- File upload handlers ---
  const handleFileUploaded = useCallback((file: PendingFile) => {
    setPendingFiles((prev) => [...prev, file])
  }, [])

  const handleFileRemove = useCallback((fileId: string) => {
    setPendingFiles((prev) => prev.filter((f) => f.id !== fileId))
  }, [])

  // --- Drag & drop handlers ---
  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    dragCountRef.current++
    setDragActive(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    dragCountRef.current--
    if (dragCountRef.current <= 0) {
      dragCountRef.current = 0
      setDragActive(false)
    }
  }, [])

  const handleDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    dragCountRef.current = 0
    setDragActive(false)

    const files = e.dataTransfer.files
    if (!files || files.length === 0) return

    for (const file of Array.from(files)) {
      const result = await uploadFile(file)
      if (result.ok) {
        handleFileUploaded({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
      } else {
        showToast(result.error || t('uploadFailed'), 'error')
      }
    }
  }, [handleFileUploaded, showToast])

  // --- AskUser callbacks ---
  const handleAskUserSubmit = useCallback((answers: Record<string, string>) => {
    wsSend({ type: 'ask_user_response', answers, cancelled: false, channel: 'web', chat_id: currentChatIDRef.current || undefined })
    setAskUser(null)
  }, [wsSend])

  const handleAskUserCancel = useCallback((answers: Record<string, string>) => {
    wsSend({
      type: 'ask_user_response',
      answers,
      cancelled: true,
      channel: 'web',
      chat_id: currentChatIDRef.current || undefined,
    })
    setAskUser(null)
  }, [wsSend])

  // --- Paste handler (for images) ---
  const handlePaste = usePasteUpload(handleFileUploaded, loading, showToast)


  // --- Global keyboard shortcuts ---
  useKeyboardShortcuts([
    {
      key: 'k',
      ctrl: true,
      handler: () => setCommandPaletteOpen(prev => !prev),
      description: 'Toggle command palette',
    },
    {
      key: 'Escape',
      enabled: askUser !== null,
      handler: () => { setAskUser(null); wsSend({ type: 'ask_user_response', answers: {}, cancelled: true }) },
      description: 'Cancel AskUser dialog',
    },
    {
      key: 'Escape',
      enabled: contextMenu !== null,
      handler: () => setContextMenu(null),
      description: 'Close context menu',
    },
    {
      key: 'Escape',
      enabled: searchOpen,
      handler: () => setSearchOpen(false),
      description: 'Close search panel',
    },
    {
      key: 'Escape',
      enabled: settingsOpen,
      handler: () => setSettingsOpen(false),
      description: 'Close settings panel',
    },
    {
      key: 'Escape',
      enabled: previewImage !== null,
      handler: () => setPreviewImage(null),
      description: 'Close image preview',
    },
  ])
  const turns = useMemo(() => groupMessagesIntoTurns(messages), [messages])
  turnsCountRef.current = turns.length

  // --- Virtual scrolling via @tanstack/react-virtual ---
  const virtualizer = useVirtualizer({
    count: turns.length,
    getScrollElement: () => messagesContainerRef.current,
    estimateSize: (index) => {
      const turn = turns[index]
      return turn?.type === 'user' ? VIRTUAL_ROW_HEIGHT_USER : VIRTUAL_ROW_HEIGHT_ASSISTANT
    },
    overscan: 5,
    // Override scrollToFn to suppress virtualizer's internal scroll compensation
    // during initial history load. Without this, resizeItem → _scrollToOffset
    // creates infinite oscillation as items measure to their real heights.
    scrollToFn: (offset, _defaultScrollToFn, ..._rest) => {
      if (loadingHistoryRef.current) return // suppress during measurement phase
      const el = messagesContainerRef.current
      if (el) el.scrollTo({ top: offset, behavior: 'instant' as ScrollBehavior })
    },
  })
  virtualizerRef.current = virtualizer

  // --- Command palette items ---
  const commandItems = useMemo(() => [
    { id: 'clear', label: '/clear', icon: '🗑️', description: t('cmdClear'), action: () => { handleSend('/clear') } },
    { id: 'new', label: '/new', icon: '✨', description: t('cmdNew'), action: () => { handleSend('/new') } },
    { id: 'help', label: '/help', icon: '❓', description: t('cmdHelp'), action: () => { handleSend('/help') } },
    { id: 'settings', label: t('settings'), icon: '⚙️', description: t('openSettings'), action: () => { setSettingsOpen(true) } },
    { id: 'search', label: t('searchHistory'), icon: '🔍', description: t('searchHistory'), action: () => {} },
  ], [t, handleSend])

  // --- Scroll to message (for reply navigation) ---
  const handleScrollToMessage = useCallback((msgId: string) => {
    const turnIndex = turns.findIndex(t => {
      if (t.type === 'user') return t.message.id === msgId
      return t.messages.some(m => m.id === msgId)
    })
    if (turnIndex >= 0) {
      virtualizer.scrollToIndex(turnIndex, { align: 'center', behavior: 'smooth' })
      // Flash highlight
      const el = messagesContainerRef.current?.querySelector(`[data-msg-id="${msgId}"]`)
      if (el) {
        el.classList.add('search-highlight')
        setTimeout(() => el.classList.remove('search-highlight'), 2000)
      }
    }
  }, [turns, virtualizer])

  return (
    <div className="flex flex-col h-screen bg-slate-900 chat-app"
         onDragOver={handleDragOver}
         onDragLeave={handleDragLeave}
         onDrop={handleDrop}
         onPaste={handlePaste}
    >

      <a href="#messages-container" className="skip-to-content">{t('skipToContent')}</a>
      {/* Drag overlay */}
      {dragActive && (
        <div className="drag-overlay" data-testid="drag-overlay">
          <div className="drag-overlay-content">
            <span className="text-4xl">📂</span>
            <span className="text-lg font-medium mt-2">{t('dragToUpload')}</span>
            <span className="text-sm opacity-60 mt-1">{t('dragSupportedTypes')}</span>
          </div>
        </div>
      )}
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-3 bg-slate-800 border-b border-slate-700 header-bar">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-bold text-white">🤖 xbot{nickname ? ` · ${nickname}` : ''}</h1>
          <span className={`text-xs px-2 py-0.5 rounded-full ${
            connected
              ? 'bg-green-900/50 text-green-400'
              : reconnecting
                ? 'bg-yellow-900/50 text-yellow-400'
                : 'bg-red-900/50 text-red-400'
          }`} role="status">
            {connected ? t('connected') : reconnecting
              ? `${t('reconnecting')} ${reconnectAttempt > 0 ? `(attempt ${reconnectAttempt}${nextReconnectIn > 0 ? `, ${nextReconnectIn}s` : ''})` : ''}`
              : t('disconnected')}
          </span>
          {/* Context indicator */}
          {contextInfo && contextInfo.max_tokens > 0 && (
            <span
              className={`text-xs px-2 py-0.5 rounded-full ${
                contextInfo.usage_pct > 80 ? 'bg-red-900/50 text-red-400' :
                contextInfo.usage_pct > 50 ? 'bg-yellow-900/50 text-yellow-400' :
                'bg-green-900/50 text-green-400'
              }`}
              title={t("contextUsageTitle", { prompt: contextInfo.prompt_tokens.toLocaleString(), max: contextInfo.max_tokens.toLocaleString() })}
            >
              📊 {(contextInfo.prompt_tokens / 1000).toFixed(1)}K/{(contextInfo.max_tokens / 1000).toFixed(0)}K ({contextInfo.usage_pct.toFixed(1)}%)
            </span>
          )}
          {/* Model selector */}
          {availableModels.length > 0 && (
            <div className="relative">
              <button
                onClick={() => setModelDropdownOpen(!modelDropdownOpen)}
                className="text-xs px-2 py-0.5 rounded-full bg-slate-700/50 text-slate-300 hover:bg-slate-700 hover:text-white transition-colors flex items-center gap-1"
                title={t('switchModel')}
              >
                🧠 {currentModel || 'default'}
                <span className="text-[10px]">▾</span>
              </button>
              {modelDropdownOpen && (
                <>
                  <div className="fixed inset-0 z-40" onClick={() => setModelDropdownOpen(false)} />
                  <div className="absolute top-full left-0 mt-1 bg-slate-800 border border-slate-600 rounded-lg shadow-xl z-50 py-1 min-w-[200px] max-h-64 overflow-y-auto" role="listbox" aria-label={t('modelSelect')}>
                    {availableModels.map(model => (
                      <button
                        key={model}
                        onClick={() => handleModelSwitch(model)}
                        role="option"
                        className={`w-full text-left px-3 py-2 text-sm hover:bg-slate-700 transition-colors ${
                          model === currentModel ? 'text-blue-400 bg-blue-500/10' : 'text-slate-300'
                        }`}
                      >
                        {model === currentModel && '✓ '}{model}
                      </button>
                    ))}
                  </div>
                </>
              )}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => { setCommandPaletteOpen(true) }}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title={t('searchKbHint')} aria-label={t('searchHistory')}
          >
            🔍
          </button>
          <button
            onClick={() => setSettingsOpen(true)}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title={t('settings')} aria-label={t('openSettings')}
          >
            ⚙️
          </button>
          {notifPermission === 'default' && (
            <button
              onClick={() => requestNotifPermission().then(ok => {
                showToast(ok ? t('notificationEnabled') : t('notificationDenied'), ok ? 'success' : 'error')
              })}
              className="text-sm text-slate-400 hover:text-white transition-colors p-1"
              title={t('enableNotification')}
              aria-label={t('enableNotification')}
            >
              🔔
            </button>
          )}
          <button
            onClick={handleLogout}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
          >
            {t('logoutBtn')}
          </button>
        </div>
      </header>

      {/* Command palette (replaces SearchPanel) */}
      <Suspense fallback={null}>
        <CommandPalette
          open={commandPaletteOpen}
          onClose={() => setCommandPaletteOpen(false)}
          commands={commandItems}
          messagesContainerRef={messagesContainerRef}
          virtualizer={virtualizer}
          turns={turns}
        />
      </Suspense>

      {/* Disconnected / Reconnecting banner */}
      {!connected && serverStopped && (
        <div className="bg-red-900/40 border-b border-red-800/50 px-4 py-2 text-center text-sm text-red-400">
          {t("serverDisconnected")}
        </div>
      )}
      {reconnecting && !connected && (
        <div className="bg-yellow-900/40 border-b border-yellow-800/50 px-4 py-2 text-center text-sm text-yellow-400">
          {t("reconnecting")} {reconnectAttempt > 0 && `(attempt ${reconnectAttempt}${nextReconnectIn > 0 ? `, ${nextReconnectIn}s` : ''})`}
          <div className="text-xs text-yellow-500/70 mt-0.5">{t('reconnectSyncHint')}</div>
        </div>
      )}
      {/* Offline banner */}
      {!online && (
        <div className="bg-gray-900/60 border-b border-gray-700/50 px-4 py-2 text-center text-sm text-gray-400" role="status" aria-live="polite">
          {t("offlineMessage")}
        </div>
      )}

      {/* Main content: ChatSidebar + messages */}
      <div className="flex flex-1 min-h-0 app-body">
        <ChatSidebar
          onSwitchChat={(chatID: string) => {
            setCurrentChatID(chatID)
            setMessages([])
            resetProgress()
            setTodos([])
            setSubAgents([])
            setLoading(false)
            streamingContentRef.current = ''
            reasoningRef.current = ''
            loadHistory()
          }}
          onNewChat={() => {
            setCurrentChatID('')
            setMessages([])
            resetProgress()
            setTodos([])
            setSubAgents([])
            setLoading(false)
            setContextInfo(null)
            streamingContentRef.current = ''
            reasoningRef.current = ''
          }}
          currentChatID={currentChatID}
          onExportMarkdown={handleExportMarkdown}
          onExportJSON={handleExportJSON}
        />
        <div className="flex flex-col flex-1 min-w-0">

      {/* Persistent TodoBar — visible across turns, cleared on new user message */}
      {todos.length > 0 && <TodoBar todos={todos} />}

      {/* Persistent SubAgentPanel — visible across turns, cleared on new user message */}
      {subAgents.length > 0 && <SubAgentPanel agents={subAgents} />}

      {/* Messages */}
      <div
        ref={messagesContainerRef}
        onScroll={handleContainerScroll}
        className="flex-1 overflow-y-auto px-4 py-4 chat-messages messages-area"
        role="main"
        aria-label={t('messagesAriaLabel')}
        data-testid="messages-container"
      >
        {messages.length === 0 && !loading && (
          <div className="text-center py-20 animate-fade-in select-none empty-state-container">
            <div className="empty-state-icon-wrapper">
              <div className="empty-state-icon-bg"></div>
              <div className="empty-state-icon">🤖</div>
            </div>
            <p className="text-xl font-semibold mb-3" style={{ color: 'var(--text-primary)', letterSpacing: '-0.02em' }}>{t('startConversation')}</p>
            <p className="text-sm mb-12" style={{ color: 'var(--text-tertiary)', maxWidth: 320, margin: '0 auto 48px' }}>{t('sendFirstMessage')}</p>
            <div className="flex flex-col items-center gap-3">
              <div className="empty-state-action-btn">
               <span>⌨️</span> {t("searchKbHint")}
              </div>
              <div className="empty-state-action-btn">
               <span>⚡</span> {t("commandHint")}
              </div>
            </div>
          </div>
         )}

        {/* Virtualized message list */}
        {turns.length > 0 && (
          <div
            style={{
              height: virtualizer.getTotalSize(),
              width: '100%',
              position: 'relative',
            }}
          >
            {virtualizer.getVirtualItems().map((virtualItem) => {
              const turn = turns[virtualItem.index]
              const isLatestTurn = virtualItem.index === turns.length - 1
              const isActive = loading || progress !== null

              return (
                <div
                  key={turn.type === 'user' ? turn.message.id : turn.messages[0].id}
                  data-index={virtualItem.index}
                  ref={virtualizer.measureElement}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    transform: `translateY(${virtualItem.start}px)`,
                  }}
                >
                  {turn.type === 'user' ? (
                    <SwipeableMessage
                      onSwipeLeft={() => handleDeleteMessage(turn.message.id)}
                      onSwipeRight={() => handleReplyToMessage(turn.message.id, turn.message.content, 'user')}
                      className="flex justify-end mb-4"
                    >
                      <div data-msg-id={turn.message.id}>
                        {turn.message.replyTo && (
                          <div className="flex justify-end mb-1">
                            <div className="max-w-full">
                              <ReplyPreview
                                replyTo={turn.message.replyTo}
                                onClick={() => handleScrollToMessage(turn.message.replyTo!.id)}
                              />
                            </div>
                          </div>
                        )}
                        <div className="rounded-xl px-4 py-3 bg-blue-600 text-white markdown-body text-sm relative ml-auto user-msg"
                             style={{ width: 'fit-content', overflowWrap: 'break-word' }}
                             onContextMenu={(e) => {
                               e.preventDefault()
                               setContextMenu({
                                 x: e.clientX,
                                 y: e.clientY,
                                 items: [
                                   { label: t('replyMessage'), icon: '↩️', onClick: () => handleReplyToMessage(turn.message.id, turn.message.content, 'user') },
                                   { label: t('replyInThread'), icon: '💬', onClick: () => handleOpenThread(turn.message) },
                                   { label: t('deleteMessage'), icon: '🗑️', onClick: () => handleDeleteMessage(turn.message.id), danger: true },
                                 ],
                               })
                             }}
                        >
                          <UserMessageContent content={turn.message.content} onPreview={(url) => setPreviewImage(url)} />
                          {turn.message.ts && (
                           <div className="text-xs mt-1 text-right text-blue-200/50 flex items-center justify-end gap-1">
                             <span>{formatRelativeTime(turn.message.ts * 1000)}</span>
                             {turn.message.status === 'sending' && <span className="animate-pulse">⏳</span>}
                             {turn.message.status === 'failed' && <span className="text-red-300">❌ {t('sendFailed')}</span>}
                             {turn.message.edited && <span className="italic">{t('edited')}</span>}
                           </div>
                          )}
                        </div>
                      </div>
                    </SwipeableMessage>
                  ) : (
                    <div className="mb-4" data-msg-id={turn.messages[0].id}
                      onContextMenu={(e) => {
                        e.preventDefault()
                        const last = turn.messages[turn.messages.length - 1]
                        setContextMenu({
                          x: e.clientX,
                          y: e.clientY,
                          items: [
                            { label: t('replyMessage'), icon: '↩️', onClick: () => handleReplyToMessage(last.id, last.content, 'assistant') },
                            { label: t('replyInThread'), icon: '💬', onClick: () => handleOpenThread(last) },
                            { label: t('regenerate'), icon: '🔄', onClick: () => handleRegenerate(turn.messages[0].id) },
                            { label: t('copyContent'), icon: '📋', onClick: () => { navigator.clipboard.writeText(turn.messages.map(m => m.content).join('\n\n')) } },
                            { label: t('deleteMessage'), icon: '🗑️', onClick: () => handleDeleteMessage(turn.messages[0].id), danger: true },
                          ],
                        })
                      }}
                    >
                      <AssistantTurn
                        messages={turn.messages}
                        progress={isLatestTurn && isActive ? progress : null}
                        liveIterations={isLatestTurn && isActive ? liveIterations : undefined}
                        loading={isLatestTurn && isActive && loading}
                        savedProgress={turn.messages[turn.messages.length - 1]?.savedProgress ?? null}
                        onDelete={() => handleDeleteMessage(turn.messages[0].id)}
                        onRegenerate={() => handleRegenerate(turn.messages[0].id)}
                        onReply={() => {
                          const last = turn.messages[turn.messages.length - 1]
                          handleReplyToMessage(last.id, last.content, 'assistant')
                        }}
                        onDoubleClickReply={() => {
                          const last = turn.messages[turn.messages.length - 1]
                          handleReplyToMessage(last.id, last.content, 'assistant')
                        }}
                        onScrollToMessage={handleScrollToMessage}
                        streamingLength={isLatestTurn && isActive ? streamingContentRef.current.length : undefined}
                        onToggleReaction={(emoji: string) => {
                          const last = turn.messages[turn.messages.length - 1]
                          if (!last) return
                          const existing = last.reactions || []
                          const reaction = existing.find(r => r.emoji === emoji)
                          let updated: typeof existing
                          if (reaction) {
                            if (reaction.byMe) {
                              const newUsers = reaction.users.filter(u => u !== 'me')
                              updated = newUsers.length === 0
                                ? existing.filter(r => r.emoji !== emoji)
                                : existing.map(r => r.emoji === emoji ? { ...r, users: newUsers, byMe: false } : r)
                            } else {
                              updated = existing.map(r => r.emoji === emoji ? { ...r, users: [...r.users, 'me'], byMe: true } : r)
                            }
                          } else {
                            updated = [...existing, { id: `r-${last.id}-${emoji}`, emoji, users: ['me'], byMe: true }]
                          }
                          setMessages(prev => prev.map(m => m.id === last.id ? { ...m, reactions: updated } : m))
                        }}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}

        {/* Standalone progress when no assistant turn exists yet (e.g. right after user sends a message) */}
        {messages.length > 0 && messages[messages.length - 1].type === 'user' && (progress || loading) && (
          <ProgressPanel progress={progress} liveIterations={liveIterations} loading={loading} />
        )}
      </div>

      {/* Scroll to bottom button */}
      {!autoScroll && (messages.length > 0 || loading) && (
        <button
          onClick={() => { setAutoScroll(true); autoScrollRef.current = true; requestAnimationFrame(() => scrollToBottom('smooth')) }}
          className="scroll-to-bottom-btn"
          aria-label={t('scrollToBottom')}
          data-testid="scroll-to-bottom-btn"
        >
          ↓ {t('newMessages')}
        </button>
      )}

      {/* Preset commands bar */}
      {presets.length > 0 && (
        <div className="preset-bar">
          {[...presets]
            .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
            .map((p) => (
              <button
                key={p.id}
                className="preset-chip"
                onClick={() => handlePresetClick(p)}
                disabled={loading || !connected}
                title={p.content.length > PRESET_TOOLTIP_LENGTH ? p.content.slice(0, PRESET_TOOLTIP_LENGTH) + '...' : p.content}
              >
                {p.icon || '⚡'} {p.label}
              </button>
            ))}
        </div>
      )}

      {/* Reply indicator */}
      {replyingTo && (
        <div className="reply-indicator px-4" data-testid="reply-indicator">
          <div className="reply-indicator-content">
            <span>↩️ {t('replyingTo')}:</span>
            <span className="reply-indicator-preview">
              {replyingTo.content.length > REPLY_INDICATOR_LENGTH ? replyingTo.content.slice(0, REPLY_INDICATOR_LENGTH) + '...' : replyingTo.content}
            </span>
          </div>
          <button className="reply-indicator-cancel" onClick={handleCancelReply}>
            ✕
          </button>
        </div>
      )}

      {/* Input area */}
      <div className={`px-4 py-3 bg-slate-800 border-t border-slate-700 input-bar ${replyingTo ? 'border-t-0' : ''}`}>
        <div className="flex items-end gap-3 max-w-4xl mx-auto">
          <div className="flex-1">
            {/* Pending files preview */}
            {pendingFiles.length > 0 && (
              <div className="flex flex-wrap gap-2 mb-2">
                {pendingFiles.map((f) => (
                  <div key={f.id} className="file-tag">
                    <span className="file-tag-name">{f.name}</span>
                    <button
                      className="file-tag-remove"
                      onClick={() => handleFileRemove(f.id)}
                      title={t("remove")}
                    >
                      ✕
                    </button>
                  </div>
                ))}
              </div>
            )}
            <TiptapEditor
              ref={editorRef}
              onSend={handleSend}
              disabled={loading}
              connected={connected}
              currentModel={currentModel}
              onCancel={handleCancel}
            />
          </div>
          <FileUpload
            onUpload={handleFileUploaded}
            disabled={loading}
            showToast={showToast}
          />
          {loading && (
            <button
              onClick={handleCancel}
              className="cancel-btn"
              title={t("stopGeneration")}
              data-testid="cancel-btn"
            >
              ⏹
            </button>
          )}
        </div>
	      </div>
	      </div>{/* end flex-1 inner column */}
	      </div>{/* end ChatSidebar + content row */}

	      {/* AskUser interaction panel */}
	      {askUser && (
	        <AskUserPanel
	          askUser={askUser}
	          onSubmit={handleAskUserSubmit}
	          onCancel={handleAskUserCancel}
	        />
	      )}

	      {/* Settings panel */}
	      <Suspense fallback={null}>
	        <SettingsPanel
	          open={settingsOpen}
	          onClose={() => setSettingsOpen(false)}
	          onNicknameChange={(n) => setNickname(n)}
	          onPresetsChange={setPresets}
	        />
	      </Suspense>

      {/* Image preview lightbox */}
      {/* Context menu for right-click / long-press */}
      <ContextMenu
        x={contextMenu?.x ?? 0}
        y={contextMenu?.y ?? 0}
        items={contextMenu?.items ?? []}
        visible={contextMenu !== null}
        onClose={() => setContextMenu(null)}
      />
      {previewImage && (
        <div
          className="image-preview-overlay"
          onClick={() => setPreviewImage(null)}
          role="dialog"
          aria-label={t('imagePreview')}
        >
          <button
            className="image-preview-close"
            onClick={() => setPreviewImage(null)}
            aria-label={t('closePreview')}
          >✕</button>
          <img
            src={previewImage}
            alt={t("imagePreviewAlt")}
            className="image-preview-img"
            onClick={e => e.stopPropagation()}
          />
        </div>
      )}
      <OnboardingTip />

      {/* Thread panel */}
      <ThreadPanel
        open={threadOpen}
        onClose={() => setThreadOpen(false)}
        parentMessage={threadParentMsg}
        threadMessages={threadParentMsg ? (threadMessages[threadParentMsg.id] || []) : []}
        onSendReply={(content) => threadParentMsg && handleSendThreadReply(threadParentMsg.id, content)}
      />

      {/* Notification center panel */}
      <NotificationPanel
        open={notificationOpen}
        onClose={() => setNotificationOpen(false)}
      />
    </div>
  )
}
