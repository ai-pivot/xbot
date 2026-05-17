import { useCallback } from 'react'
import type { WebSocketMessage } from './useWebSocket'
import type { Message } from '../types'
import type { WsProgressPayload, IterationSnapshot } from '../components/ProgressPanel'
import { normalizeIterationHistory } from '../utils'

export interface UseChatMessageHandlerParams {
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>
  setLoading: React.Dispatch<React.SetStateAction<boolean>>
  setProgress: React.Dispatch<React.SetStateAction<WsProgressPayload | null>>
  setAskUser: React.Dispatch<React.SetStateAction<{
    questions: { question: string; options?: string[] }[]
    answers: Record<string, string>
    currentQ: number
  } | null>>
  prevIterationRef: React.MutableRefObject<number>
  progressRef: React.MutableRefObject<WsProgressPayload | null>
  reasoningRef: React.MutableRefObject<string>
  streamingContentRef: React.MutableRefObject<string>
  liveIterationsRef: React.MutableRefObject<IterationSnapshot[]>
  fetchContextInfo: () => void
  resetProgress: () => void
  setLiveIterationsSync: (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => void
  showToast: (message: string, type: 'info' | 'error' | 'success') => void
  lastSeqRef: React.MutableRefObject<number>
}

// --- Individual handlers ---

function handleProgress(
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
) {
  setLoading(true)
}

function handleProgressStructured(
  data: WebSocketMessage,
  prevIterationRef: React.MutableRefObject<number>,
  progressRef: React.MutableRefObject<WsProgressPayload | null>,
  reasoningRef: React.MutableRefObject<string>,
  setLiveIterationsSync: (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => void,
  setProgress: React.Dispatch<React.SetStateAction<WsProgressPayload | null>>,
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
) {
  let p: WsProgressPayload = data.progress as WsProgressPayload
  const prevIter = prevIterationRef.current
  const prevProgress = progressRef.current

  if (prevProgress && p.iteration === prevProgress.iteration) {
    const nextThinking = (p.thinking || '').trim()
    const prevThinking = (prevProgress.thinking || '').trim()
    p = {
      ...p,
      thinking: nextThinking.length > 0 ? p.thinking : prevProgress.thinking,
      completed_tools: (p.completed_tools?.length ?? 0) > 0
        ? p.completed_tools
        : (prevProgress.completed_tools ?? []),
    }
    if (nextThinking.length === 0 && prevThinking.length > 0) {
      p.thinking = prevProgress.thinking
    }
  }

  if (prevIter >= 0 && p.iteration > prevIter && prevProgress) {
    const allTools = [
      ...(prevProgress.completed_tools ?? []),
      ...(prevProgress.active_tools ?? []),
    ].map(t => ({
      name: t.name,
      label: t.label,
      status: t.status,
      elapsed_ms: t.elapsed_ms,
    }))
    const snapThinking = reasoningRef.current.trim() || prevProgress.thinking || ''
    setLiveIterationsSync(prev => {
      const merged = normalizeIterationHistory([
        ...prev,
        {
          iteration: prevIter,
          thinking: snapThinking,
          tools: allTools,
        },
      ])
      return merged
    })
    reasoningRef.current = ''
  }

  if (p.iteration > 0 && (p.completed_tools?.length ?? 0) > 0) {
    const inferredPrev = p.iteration - 1
    setLiveIterationsSync(prev => {
      const hasPrev = prev.some((s) => s.iteration === inferredPrev)
      if (hasPrev) return prev
      return normalizeIterationHistory([
        ...prev,
        {
          iteration: inferredPrev,
          tools: (p.completed_tools ?? []).map((t) => ({
            name: t.name,
            label: t.label,
            status: t.status,
            elapsed_ms: t.elapsed_ms,
          })),
        },
      ])
    })
  }

  prevIterationRef.current = p.iteration
  progressRef.current = p
  setProgress(p)
  setLoading(true)
}

function handleStreamContent(
  data: WebSocketMessage,
  prevIterationRef: React.MutableRefObject<number>,
  progressRef: React.MutableRefObject<WsProgressPayload | null>,
  reasoningRef: React.MutableRefObject<string>,
  streamingContentRef: React.MutableRefObject<string>,
  setProgress: React.Dispatch<React.SetStateAction<WsProgressPayload | null>>,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
) {
  const reasoning = (data.progress as Record<string, string>)?.reasoning_stream_content || ''
  const content = (data.progress as Record<string, string>)?.stream_content || ''
  if (!reasoning && !content) return

  if (reasoning) {
    reasoningRef.current = reasoning
    if (progressRef.current) {
      progressRef.current = { ...progressRef.current, thinking: reasoningRef.current }
      setProgress({ ...progressRef.current })
    } else {
      const p: WsProgressPayload = {
        phase: 'thinking',
        iteration: prevIterationRef.current >= 0 ? prevIterationRef.current : 0,
        thinking: reasoningRef.current,
        active_tools: [],
        completed_tools: [],
      }
      progressRef.current = p
      prevIterationRef.current = p.iteration
      setProgress(p)
    }
  }

  if (content) {
    streamingContentRef.current = content
    setMessages(prev => {
      const last = prev[prev.length - 1]
      if (last && last.id === '__streaming__') {
        if (last.content === content) return prev
        return [...prev.slice(0, -1), { ...last, content: content }]
      }
      return [...prev, {
        id: '__streaming__',
        type: 'assistant' as const,
        content: content,
      }]
    })
  }

  setLoading(true)
}

function handleTextCard(
  data: WebSocketMessage,
  prevIterationRef: React.MutableRefObject<number>,
  progressRef: React.MutableRefObject<WsProgressPayload | null>,
  reasoningRef: React.MutableRefObject<string>,
  streamingContentRef: React.MutableRefObject<string>,
  liveIterationsRef: React.MutableRefObject<IterationSnapshot[]>,
  setLiveIterationsSync: (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => void,
  resetProgress: () => void,
  lastSeqRef: React.MutableRefObject<number>,
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  fetchContextInfo: () => void,
) {
  const accumulatedReasoning = reasoningRef.current.trim()
  const progressSnap = progressRef.current
    ? {
        ...progressRef.current,
        thinking: progressRef.current.thinking || accumulatedReasoning,
        active_tools: [],
      } as WsProgressPayload
    : accumulatedReasoning
      ? ({
          phase: 'done' as const,
          iteration: prevIterationRef.current >= 0 ? prevIterationRef.current : 0,
          thinking: accumulatedReasoning,
          active_tools: [],
          completed_tools: [],
        } as WsProgressPayload)
      : null

  const snapThinking = accumulatedReasoning || progressSnap?.thinking || ''
  const currentSnap = progressSnap ? (() => {
    const allTools = [
      ...(progressSnap.completed_tools ?? []),
    ].map(t => ({
      name: t.name,
      label: t.label,
      status: t.status,
      elapsed_ms: t.elapsed_ms,
      summary: t.summary,
    }))
    return {
      iteration: prevIterationRef.current,
      thinking: snapThinking,
      tools: allTools,
    }
  })() : null

  const currentLive = liveIterationsRef.current ?? []
  let localHistory: IterationSnapshot[] = [...currentLive]
  if (currentSnap) localHistory.push(currentSnap)
  localHistory = normalizeIterationHistory(localHistory)

  setLiveIterationsSync([])

  localHistory = normalizeIterationHistory(localHistory)

  let finalHistory = localHistory
  if (data.progress_history) {
    try {
      const serverHistory = normalizeIterationHistory(JSON.parse(data.progress_history as string))
      if (serverHistory.length > 0) {
        finalHistory = serverHistory
      }
    } catch {
      // keep local snapshots
    }
  }

  resetProgress()
  lastSeqRef.current = 0
  setLoading(false)

  const finalContent = streamingContentRef.current || (data.content as string) || ''
  streamingContentRef.current = ''

  setMessages((prev) => {
    const filtered = prev.filter(m => m.id !== '__streaming__')
    const msg: Message = {
      id: (data.id as string) || `ws-${Date.now()}`,
      type: data.type === 'card' ? 'system' : 'assistant',
      content: finalContent,
      ts: data.ts as number | undefined,
      savedProgress: progressSnap,
      iterationHistory: finalHistory.length > 0 ? finalHistory : undefined,
    }
    return [...filtered, msg]
  })
  fetchContextInfo()
}

function handleUserEcho(
  data: WebSocketMessage,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
) {
  if (data.original_content) {
    setMessages((prev) => prev.map((m) =>
      m.type === 'user' && m.content === (data.original_content as string)
        ? { ...m, content: data.content as string }
        : m
    ))
  }
}

function handleAskUser(
  data: WebSocketMessage,
  setAskUser: React.Dispatch<React.SetStateAction<{
    questions: { question: string; options?: string[] }[]
    answers: Record<string, string>
    currentQ: number
  } | null>>,
) {
  const questions = (data.progress as Record<string, unknown>)?.questions || []
  if (Array.isArray(questions) && questions.length > 0) {
    setAskUser({ questions: questions as { question: string; options?: string[] }[], answers: {}, currentQ: 0 })
  }
}

function handleRunnerStatus(data: WebSocketMessage) {
  try {
    const detail = data.content ? JSON.parse(data.content as string) : {}
    window.dispatchEvent(new CustomEvent('runner-status-change', {
      detail: { runnerName: detail.runner_name, online: detail.online },
    }))
  } catch { /* ignore */ }
}

function handleSyncProgress(
  data: WebSocketMessage,
  showToast: (message: string, type: 'info' | 'error' | 'success') => void,
) {
  try {
    const detail = data.content ? JSON.parse(data.content as string) : {}
    if (detail.message) showToast(detail.message, detail.phase === 'done' ? 'success' : 'info')
  } catch { /* ignore */ }
}

// --- Main hook ---

export function useChatMessageHandler(params: UseChatMessageHandlerParams) {
  const {
    setMessages, setLoading, setProgress, setAskUser,
    prevIterationRef, progressRef, reasoningRef, streamingContentRef, liveIterationsRef,
    fetchContextInfo, resetProgress, setLiveIterationsSync, showToast, lastSeqRef,
  } = params

  const onMessage = useCallback((data: WebSocketMessage) => {
    switch (data.type) {
      case 'progress':
        handleProgress(setLoading)
        break

      case 'progress_structured':
        handleProgressStructured(
          data, prevIterationRef, progressRef, reasoningRef,
          setLiveIterationsSync, setProgress, setLoading,
        )
        break

      case 'stream_content':
        handleStreamContent(
          data, prevIterationRef, progressRef, reasoningRef,
          streamingContentRef, setProgress, setMessages, setLoading,
        )
        break

      case 'text':
      case 'card':
        handleTextCard(
          data, prevIterationRef, progressRef, reasoningRef,
          streamingContentRef, liveIterationsRef, setLiveIterationsSync,
          resetProgress, lastSeqRef, setLoading, setMessages, fetchContextInfo,
        )
        break

      case 'user_echo':
        handleUserEcho(data, setMessages)
        break

      case 'ask_user':
        handleAskUser(data, setAskUser)
        break

      case 'runner_status':
        handleRunnerStatus(data)
        break

      case 'sync_progress':
        handleSyncProgress(data, showToast)
        break

      default:
        break
    }
  }, [fetchContextInfo, resetProgress, setLiveIterationsSync, showToast, setMessages, setLoading, setProgress, setAskUser, prevIterationRef, progressRef, reasoningRef, streamingContentRef, liveIterationsRef, lastSeqRef])

  return { onMessage }
}
