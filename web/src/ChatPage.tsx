import { useEffect, useRef, useState, useCallback, useMemo, lazy, Suspense } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { useWebSocket } from './hooks/useWebSocket'
import { useChatMessageHandler } from './hooks/useChatMessageHandler'
import type { TiptapEditorHandle } from './components/TiptapEditor'
import type { PresetCommand, Message, Turn } from './types'
import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'
import { formatTime, formatFileSize, normalizeIterationHistory, createResetProgress } from './utils'
import { getCodeBlockProps } from './components/CodeBlock'
import ProgressPanel from './components/ProgressPanel'
import AssistantTurn from './components/AssistantTurn'
import ChatSidebar from './components/ChatSidebar'
import TiptapEditor from './components/TiptapEditor'
import AskUserPanel from './components/AskUserPanel'
import FileUpload, { uploadFile, usePasteUpload, type PendingFile } from './components/FileUpload'

const SettingsPanel = lazy(() => import('./components/SettingsPanel'))
const SearchPanel = lazy(() => import('./components/SearchPanel'))

const codeBlockComponents = getCodeBlockProps()

interface ChatPageProps {
  onLogout: () => void
}



function groupMessagesIntoTurns(messages: Message[]): Turn[] {
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

interface ParsedAttachment {
  type: 'image' | 'file'
  name: string
  url?: string
  size?: number
  raw: string
}

const reAttachment = /<(image|file)\s+([^>]*?)\/?>/gi

function parseAttachments(content: string): { attachments: ParsedAttachment[]; cleanContent: string } {
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

function AttachmentCard({ attachment }: { attachment: ParsedAttachment }) {
  if (attachment.type === 'image' && attachment.url) {
    return (
      <div className="attachment-card attachment-image">
        <img
          src={attachment.url}
          alt={attachment.name}
          className="attachment-img"
          loading="lazy"
          onClick={() => window.open(attachment.url, '_blank')}
        />
        <div className="attachment-meta">
          <span className="truncate">{attachment.name}</span>
          {attachment.size != null && <span>{formatFileSize(attachment.size)}</span>}
        </div>
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

function UserMessageContent({ content }: { content: string }) {
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
        elements.push(<AttachmentCard key={`att-${idx}`} attachment={attachments[idx]} />)
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
}

export default function ChatPage({ onLogout }: ChatPageProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState<WsProgressPayload | null>(null)
  const [liveIterations, _setLiveIterations] = useState<IterationSnapshot[]>([])
  const liveIterationsRef = useRef<IterationSnapshot[]>([])
  // Keep ref in sync so we can read the latest value synchronously
  // (React setState updater callbacks are async and cannot be relied upon).
  const setLiveIterationsSync = (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => {
    _setLiveIterations(prev => {
      const next = typeof updater === 'function' ? updater(prev) : updater
      liveIterationsRef.current = next
      return next
    })
  }
  const prevIterationRef = useRef<number>(-1)
  const progressRef = useRef<WsProgressPayload | null>(null) // sync ref to avoid stale closures
  const reasoningRef = useRef<string>('') // accumulated reasoning from stream_content
  const streamingContentRef = useRef<string>('') // accumulated content from stream_content
  const resetProgress = createResetProgress({
    setProgress: (v) => setProgress(v),
    setLiveIterations: setLiveIterationsSync,
    prevIterationRef,
    progressRef,
    reasoningRef,
    streamingContentRef,
  })
  const [autoScroll, setAutoScroll] = useState(true)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [dragActive, setDragActive] = useState(false)
  const [nickname, setNickname] = useState<string>(() => localStorage.getItem('xbot-nickname') || '')
  const editorRef = useRef<TiptapEditorHandle>(null)
  const [presets, setPresets] = useState<PresetCommand[]>([])
  const [askUser, setAskUser] = useState<{ questions: { question: string; options?: string[] }[]; answers: Record<string, string>; currentQ: number } | null>(null)
  const [toasts, setToasts] = useState<{ id: number; message: string; type: 'info' | 'error' | 'success' }[]>([])
  const [currentModel, setCurrentModel] = useState('')
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [modelDropdownOpen, setModelDropdownOpen] = useState(false)
  const [currentChatID, setCurrentChatID] = useState<string>('')
  const [contextInfo, setContextInfo] = useState<{ prompt_tokens: number; max_tokens: number; usage_pct: number; source: string } | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  const showToast = useCallback((message: string, type: 'info' | 'error' | 'success' = 'info') => {
    const id = Date.now()
    setToasts(prev => [...prev, { id, message, type }])
    setTimeout(() => setToasts(prev => prev.filter(t => t.id !== id)), 3000)
  }, [])

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
        showToast(`已切换到 ${model}`, 'success')
      } else {
        showToast(data.error || '切换失败', 'error')
      }
    } catch {
      showToast('切换失败', 'error')
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

  const handleContainerScroll = useCallback(() => {
    setAutoScroll(isNearBottom())
  }, [isNearBottom])

  // Auto-scroll during streaming/progress updates — throttled to avoid layout thrashing.
  // Only follows when user is already at the bottom (autoScroll=true).
  const scrollRafRef = useRef<number>(0)
  useEffect(() => {
    if (!autoScroll) return
    if (scrollRafRef.current) cancelAnimationFrame(scrollRafRef.current)
    scrollRafRef.current = requestAnimationFrame(() => scrollToBottom('instant'))
    return () => { if (scrollRafRef.current) cancelAnimationFrame(scrollRafRef.current) }
  }, [messages, progress, autoScroll, scrollToBottom])

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
  })
  const {
    connected,
    reconnecting,
    serverStopped,
    send: wsSend,
    disconnect: wsDisconnect,
  } = useWebSocket({ onMessage, lastSeqRef })

  // --- Load history (extracted for reuse on chat switch) ---
  const loadHistory = useCallback(() => {
    fetch('/api/history')
      .then((r) => r.json())
      .then((data) => {
        if (data.ok && data.messages) {
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
          const lastIsUser = hist.length > 0 && hist[hist.length - 1].type === 'user'
          if (isProcessing && lastIsUser) {
            setLoading(true)
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
          setTimeout(() => {
            scrollToBottom('instant')
            requestAnimationFrame(() => scrollToBottom('instant'))
          }, 100)
        }
      })
      .catch((err) => { console.warn('[ChatPage] failed to load history:', err) })
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
        setLoading(false)
        fetch('/api/history', { method: 'DELETE' }).catch((err) => { console.warn('[ChatPage] failed to clear history:', err) })
        showToast('对话已清空', 'info')
        return
      }
      if (cmd === '/new') {
        setMessages([])
        resetProgress()
        setLoading(false)
        showToast('新对话', 'info')
        return
      }
      if (cmd === '/help') {
        const helpMsg: Message = {
          id: `help-${Date.now()}`,
          type: 'system',
          content: `## 可用命令\n\n| 命令 | 说明 |\n|------|------|\n| /clear | 清空对话 |\n| /new | 新对话 |\n| /compact | 压缩上下文 |\n| /help | 显示帮助 |\n| /cancel | 取消当前生成 |`,
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
    }
    setMessages((prev) => [...prev, userMsg])
    resetProgress()
    setLoading(true)
    setAutoScroll(true)

    const payload: { type: string; content: string; file_ids?: string[]; file_names?: string[]; file_sizes?: number[]; upload_keys?: string[]; file_mimes?: string[] } = {
      type: 'message',
      content,
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
  }, [scrollToBottom, isNearBottom, pendingFiles])

  // --- Cancel generation ---
  const handleCancel = useCallback(() => {
    wsSend({ type: 'cancel' })
    setLoading(false)
    resetProgress()
    // Remove streaming placeholder if present
    setMessages(prev => prev.filter(m => m.id !== '__streaming__'))
  }, [wsSend, resetProgress])

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
    await fetch('/api/auth/logout', { method: 'POST' })
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
    setDragActive(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragActive(false)
  }, [])

  const handleDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragActive(false)

    const files = e.dataTransfer.files
    if (!files || files.length === 0) return

    for (const file of Array.from(files)) {
      const result = await uploadFile(file)
      if (result.ok) {
        handleFileUploaded({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
      } else {
        showToast(result.error || '上传失败', 'error')
      }
    }
  }, [handleFileUploaded, showToast])

  // --- AskUser callbacks ---
  const handleAskUserSubmit = useCallback((answers: Record<string, string>) => {
    wsSend({ type: 'ask_user_response', answers, cancelled: false })
    setAskUser(null)
  }, [wsSend])

  const handleAskUserCancel = useCallback((answers: Record<string, string>) => {
    wsSend({
      type: 'ask_user_response',
      answers,
      cancelled: true,
    })
    setAskUser(null)
  }, [wsSend])

  // --- Paste handler (for images) ---
  const handlePaste = usePasteUpload(handleFileUploaded, loading)

  // --- Search toggle callback (stable for SearchPanel) ---
  const handleSearchToggle = useCallback(() => {
    setSearchOpen(prev => !prev)
  }, [])

  const turns = useMemo(() => groupMessagesIntoTurns(messages), [messages])

  // --- Virtual scrolling via @tanstack/react-virtual ---
  const virtualizer = useVirtualizer({
    count: turns.length,
    getScrollElement: () => messagesContainerRef.current,
    estimateSize: (index) => {
      const turn = turns[index]
      return turn?.type === 'user' ? 80 : 200
    },
    overscan: 5,
  })

  return (
    <div className={`flex flex-col h-screen bg-slate-900${dragActive ? ' drag-active' : ''}`}
         onDragOver={handleDragOver}
         onDragLeave={handleDragLeave}
         onDrop={handleDrop}
         onPaste={handlePaste}
    >
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-3 bg-slate-800 border-b border-slate-700">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-bold text-white">🤖 xbot{nickname ? ` · ${nickname}` : ''}</h1>
          <span className={`text-xs px-2 py-0.5 rounded-full ${
            connected
              ? 'bg-green-900/50 text-green-400'
              : reconnecting
                ? 'bg-yellow-900/50 text-yellow-400'
                : 'bg-red-900/50 text-red-400'
          }`}>
            {connected ? '● Connected' : reconnecting ? '◐ Connecting...' : '○ Disconnected'}
          </span>
          {/* Context indicator */}
          {contextInfo && contextInfo.max_tokens > 0 && (
            <span
              className={`text-xs px-2 py-0.5 rounded-full ${
                contextInfo.usage_pct > 80 ? 'bg-red-900/50 text-red-400' :
                contextInfo.usage_pct > 50 ? 'bg-yellow-900/50 text-yellow-400' :
                'bg-green-900/50 text-green-400'
              }`}
              title={`上下文使用: ${contextInfo.prompt_tokens.toLocaleString()} / ${contextInfo.max_tokens.toLocaleString()} tokens`}
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
                title="切换模型"
              >
                🧠 {currentModel || 'default'}
                <span className="text-[10px]">▾</span>
              </button>
              {modelDropdownOpen && (
                <>
                  <div className="fixed inset-0 z-40" onClick={() => setModelDropdownOpen(false)} />
                  <div className="absolute top-full left-0 mt-1 bg-slate-800 border border-slate-600 rounded-lg shadow-xl z-50 py-1 min-w-[200px] max-h-64 overflow-y-auto">
                    {availableModels.map(model => (
                      <button
                        key={model}
                        onClick={() => handleModelSwitch(model)}
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
            onClick={handleSearchToggle}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title="搜索 (Ctrl+K)"
          >
            🔍
          </button>
          <button
            onClick={() => setSettingsOpen(true)}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title="设置"
          >
            ⚙️
          </button>
          <button
            onClick={handleLogout}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
          >
            Logout
          </button>
        </div>
      </header>

      {/* Search panel */}
      <Suspense fallback={null}>
        <SearchPanel
          open={searchOpen}
          onClose={() => setSearchOpen(false)}
          onToggle={handleSearchToggle}
          messagesContainerRef={messagesContainerRef}
        />
      </Suspense>

      {/* Disconnected / Reconnecting banner */}
      {!connected && serverStopped && (
        <div className="bg-red-900/40 border-b border-red-800/50 px-4 py-2 text-center text-sm text-red-400">
          ⛔ 服务已断开，请刷新页面重新连接
        </div>
      )}
      {reconnecting && !connected && (
        <div className="bg-yellow-900/40 border-b border-yellow-800/50 px-4 py-2 text-center text-sm text-yellow-400">
          ⚠️ 连接断开，正在尝试重连...
        </div>
      )}

      {/* Main content: ChatSidebar + messages */}
      <div className="flex flex-1 min-h-0">
        <ChatSidebar
          onSwitchChat={(chatID: string) => {
            setCurrentChatID(chatID)
            setMessages([])
            resetProgress()
            setLoading(false)
            // Reload history for the new chat after switch
            setTimeout(() => loadHistory(), 100)
          }}
          onNewChat={() => {
            setMessages([])
            resetProgress()
            setLoading(false)
            setContextInfo(null)
          }}
          currentChatID={currentChatID}
        />
        <div className="flex flex-col flex-1 min-w-0">

      {/* Messages */}
      <div
        ref={messagesContainerRef}
        onScroll={handleContainerScroll}
        className="flex-1 overflow-y-auto px-4 py-4 chat-messages"
        role="main"
        aria-label="消息"
      >
        {messages.length === 0 && !loading && (
          <div className="text-center py-20 animate-fade-in">
            <div className="text-5xl mb-4 opacity-30">🤖</div>
            <p className="text-slate-400 text-base font-medium mb-2">开始一段对话</p>
            <p className="text-slate-500 text-sm mb-8">发送消息开始与 AI 助手交流</p>
            <div className="flex flex-col items-center gap-2 text-xs text-slate-600">
              <span className="px-3 py-1 rounded-full bg-slate-800/50 border border-slate-700/50">
                按 <kbd className="px-1 py-0.5 rounded bg-slate-700/60 text-slate-400 font-mono text-[10px]">Ctrl+K</kbd> 搜索历史消息
              </span>
              <span className="px-3 py-1 rounded-full bg-slate-800/50 border border-slate-700/50">
                输入 <kbd className="px-1 py-0.5 rounded bg-slate-700/60 text-slate-400 font-mono text-[10px]">/</kbd> 查看快捷指令
              </span>
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
                    <div className="flex justify-end mb-4" data-msg-id={turn.message.id}>
                      <div className="max-w-[80%] rounded-xl px-4 py-3 bg-blue-600 text-white markdown-body text-sm">
                        <UserMessageContent content={turn.message.content} />
                        {turn.message.ts && (
                         <div className="text-xs mt-1 text-right text-blue-200/50">
                           {formatTime(turn.message.ts)}
                         </div>
                        )}
                      </div>
                    </div>
                  ) : (
                    <div className="mb-4" data-msg-id={turn.messages[0].id}>
                      <AssistantTurn
                        messages={turn.messages}
                        progress={isLatestTurn && isActive ? progress : null}
                        liveIterations={isLatestTurn && isActive ? liveIterations : undefined}
                        loading={isLatestTurn && isActive && loading}
                        savedProgress={turn.messages[turn.messages.length - 1]?.savedProgress ?? null}
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
          onClick={() => { setAutoScroll(true); requestAnimationFrame(() => scrollToBottom('smooth')) }}
          className="scroll-to-bottom-btn"
          aria-label="滚动到底部"
        >
          ↓ 新消息
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
                title={p.content.length > 50 ? p.content.slice(0, 50) + '...' : p.content}
              >
                {p.icon || '⚡'} {p.label}
              </button>
            ))}
        </div>
      )}

      {/* Input area */}
      <div className="px-4 py-3 bg-slate-800 border-t border-slate-700">
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
                      title="移除"
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
          />
          {loading && (
            <button
              onClick={handleCancel}
              className="cancel-btn"
              title="停止生成"
            >
              ⏹
            </button>
          )}
        </div>
	      </div>
	      </div>{/* end flex-1 inner column */}
	      </div>{/* end ChatSidebar + content row */}

	      {/* Toast notifications */}
      <div className="fixed top-4 right-4 z-50 space-y-2">
        {toasts.map(toast => (
          <div
            key={toast.id}
            className={`px-4 py-2 rounded-lg shadow-lg text-sm toast-enter ${
              toast.type === 'error' ? 'bg-red-500/90 text-white' :
              toast.type === 'success' ? 'bg-green-500/90 text-white' :
              'bg-slate-700/90 text-slate-200 border border-slate-600'
            }`}
          >
            {toast.message}
          </div>
        ))}
      </div>

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
    </div>
  )
}
