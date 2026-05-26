import { useCallback } from 'react'
import type { WebSocketMessage } from './useWebSocket'
import type { Message } from '../types'
import type { WsProgressPayload, IterationSnapshot, WsSubAgent } from '../components/ProgressPanel'
import { normalizeIterationHistory, sanitizeStreamContent } from '../utils'

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
  setTodos: React.Dispatch<React.SetStateAction<{ id: number; text: string; done: boolean }[]>>
  setSubAgents: React.Dispatch<React.SetStateAction<WsSubAgent[]>>
  currentChatIDRef: React.MutableRefObject<string>
  turnDoneRef: React.MutableRefObject<boolean>
  onSessionEvent?: (ev: { chat_id: string; action: string; label?: string; role?: string; instance?: string; parent_id?: string }) => void
}

// --- Individual handlers ---

function handleProgress(
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
) {
  // Only set loading if not already loading — prevents stale replayed events
  // from incorrectly entering typing state after page refresh.
  setLoading(prev => {
    if (prev) return true // already loading, keep it
    // Don't activate loading from a replayed event — loadHistory already set the correct state
    return false
  })
}

function handleProgressStructured(
  data: WebSocketMessage,
  prevIterationRef: React.MutableRefObject<number>,
  progressRef: React.MutableRefObject<WsProgressPayload | null>,
  reasoningRef: React.MutableRefObject<string>,
  setLiveIterationsSync: (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => void,
  setProgress: React.Dispatch<React.SetStateAction<WsProgressPayload | null>>,
  _setLoading: React.Dispatch<React.SetStateAction<boolean>>,
  setTodos: React.Dispatch<React.SetStateAction<{ id: number; text: string; done: boolean }[]>>,
  setSubAgents: React.Dispatch<React.SetStateAction<WsSubAgent[]>>,
  turnDoneRef: React.MutableRefObject<boolean>,
) {
  let p: WsProgressPayload = data.progress as WsProgressPayload
  const prevIter = prevIterationRef.current
  const prevProgress = progressRef.current

  // Preserve thinking from previous progress when new one is empty
  // (race between structured progress and stream_content)
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

  // Server-side iteration_history is authoritative (from GetActiveProgress snapshot
  // on reconnect). When present, use it to restore liveIterations instead of
  // trying to infer from previous state. This matches TUI's restoreIterationHistory.
  const serverIterHistory = (p as unknown as Record<string, unknown>).iteration_history as
    | { iteration: number; thinking?: string; reasoning?: string; completed_tools?: { name: string; label?: string; status: string; summary?: string }[] }[]
    | undefined

  if (serverIterHistory && serverIterHistory.length > 0) {
    const restoredIterations: IterationSnapshot[] = serverIterHistory.map(iter => ({
      iteration: iter.iteration,
      thinking: iter.thinking || '',
      reasoning: iter.reasoning || '',
      tools: (iter.completed_tools || []).map(t => ({
        name: t.name,
        label: t.label,
        status: t.status,
        summary: t.summary,
      })),
    }))
    // Use server history as authoritative source — replace any locally inferred data.
    // This is critical for WS reconnect where the server sends a full snapshot via
    // GetActiveProgress that includes all completed iterations.
    setLiveIterationsSync(normalizeIterationHistory(restoredIterations))
  } else if (prevIter >= 0 && p.iteration > prevIter && prevProgress) {
    // No server-side history — infer from local state (normal live progress flow)
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

  // Infer previous iteration from completed_tools if we haven't seen a transition yet.
  // Only do this when there's no server-side history (avoid overwriting authoritative data).
  if (!serverIterHistory && p.iteration > 0 && (p.completed_tools?.length ?? 0) > 0) {
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

  // Extract todos — only update when data is present (non-empty)
  // Empty todos field doesn't clear existing state (matches TUI behavior)
  if (p.todos && p.todos.length > 0) {
    setTodos(p.todos)
  }

  // Extract sub_agents — only update when data is present
  if (p.sub_agents && p.sub_agents.length > 0) {
    setSubAgents(p.sub_agents)
  }

  // Loading state: normally managed by handleSend/loadHistory/handleTextCard only.
  // Exception: WS reconnect sends progress snapshot with Phase != "done" while loading
  // is false (WS disconnect may have cleared it). Activate loading to restore the
  // in-progress turn's typing indicator. Matches TUI's acceptProgress path which
  // calls startAgentTurn() on reconnect.
  // Guard: only activate when Phase is explicitly a non-done phase to avoid false
  // activation from stale/done events.
  // Guard: do NOT activate if the turn has already ended (text/card received) —
  // a late progress_structured arriving after the final reply must not re-activate loading.
  const phase = (p as unknown as Record<string, unknown>).phase as string | undefined
  if (phase && phase !== 'done' && phase !== '' && !turnDoneRef.current) {
    _setLoading(prev => {
      if (prev) return true // already loading
      return true // activate loading for in-progress turn on reconnect
    })
  }
}

function handleStreamContent(
  data: WebSocketMessage,
  prevIterationRef: React.MutableRefObject<number>,
  progressRef: React.MutableRefObject<WsProgressPayload | null>,
  reasoningRef: React.MutableRefObject<string>,
  streamingContentRef: React.MutableRefObject<string>,
  setProgress: React.Dispatch<React.SetStateAction<WsProgressPayload | null>>,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  _setLoading: React.Dispatch<React.SetStateAction<boolean>>,
) {
  const reasoning = (data.progress as Record<string, string>)?.reasoning_stream_content || ''
  const rawContent = (data.progress as Record<string, string>)?.stream_content || ''
  // Sanitize: strip <system-reminder>, <think/>, <tool_call/> blocks that
  // some models echo back as text output. The TUI only shows stream_content
  // as a transient typing indicator so it's not visible; Web renders it as a
  // full message so it must be cleaned.
  const content = rawContent ? sanitizeStreamContent(rawContent) : ''
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

  // Loading state is managed by: handleSend (set true), loadHistory (set true if processing, false if idle), handleTextCard (set false).
  // WS progress/stream events should NOT alter loading state — prevents stale replayed events from incorrectly entering typing state.
  // setLoading intentionally not called here.
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
  setLoading: React.Dispatch<React.SetStateAction<boolean>>,
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>,
  fetchContextInfo: () => void,
  turnDoneRef: React.MutableRefObject<boolean>,
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
  // Do NOT reset lastSeqRef to 0 — that causes the next WS reconnect to replay
  // the entire ring buffer (up to 512 events) instead of only new events.
  // The seq counter is monotonic and the ring buffer handles wraparound naturally.
  setLoading(false)
  // Mark turn as done so late progress_structured events don't re-activate loading
  turnDoneRef.current = true

  // Use server-provided content as the authoritative final text.
  // Sanitize to strip <system-reminder>, <think/> blocks etc.
  // streamingContentRef is only a fallback when the server doesn't send content.
  const finalContent = sanitizeStreamContent((data.content as string) || streamingContentRef.current || '')
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
    setTodos, setSubAgents, currentChatIDRef, turnDoneRef,
  } = params

  const onMessage = useCallback((data: WebSocketMessage) => {
    // Filter messages by chatID in multi-chatroom mode.
    // If the WS message carries a chat_id that doesn't match the current active chat,
    // skip it to prevent messages from different chatrooms from leaking into the UI.
    // Messages WITHOUT chat_id (e.g. some progress events) from a different session
    // can leak through — skip those too if we have an active chatID to filter against.
    // EXCEPTION: "session" type messages carry status info about ALL sessions
    // and must not be filtered by chatID.
    const msgChatID = data.chat_id as string | undefined
    const activeChatID = currentChatIDRef.current
    if (data.type !== 'session' && msgChatID && activeChatID && msgChatID !== activeChatID) {
      return
    }

    switch (data.type) {
      case 'progress':
        handleProgress(setLoading)
        break

      case 'progress_structured':
        handleProgressStructured(
          data, prevIterationRef, progressRef, reasoningRef,
          setLiveIterationsSync, setProgress, setLoading,
          setTodos, setSubAgents, turnDoneRef,
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
          resetProgress, setLoading, setMessages, fetchContextInfo, turnDoneRef,
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

      case 'session': {
        const ev = (data.session || data.payload) as {
          chat_id: string
          action: string
          label?: string
          role?: string
          instance?: string
          parent_id?: string
        }
        if (params.onSessionEvent) {
          params.onSessionEvent(ev)
        }
        break
      }

      // [Reserved] subagent_started / subagent_stopped events
      // Backend currently only sends these to CLI channel via emitSessionState.
      // When web backend forwards these events, uncomment and implement:
      // case 'subagent_started': {
      //   const role = (data as Record<string, string>).role
      //   const instance = (data as Record<string, string>).instance
      //   const task = (data as Record<string, string>).task
      //   // Incremental: add running SubAgent
      //   break
      // }
      // case 'subagent_stopped': {
      //   const role = (data as Record<string, string>).role
      //   const instance = (data as Record<string, string>).instance
      //   // Update matching SubAgent status to done
      //   break
      // }

      default:
        break
    }
  }, [fetchContextInfo, resetProgress, setLiveIterationsSync, showToast, setMessages, setLoading, setProgress, setAskUser, prevIterationRef, progressRef, reasoningRef, streamingContentRef, liveIterationsRef, lastSeqRef, setTodos, setSubAgents, currentChatIDRef, turnDoneRef])

  return { onMessage }
}
