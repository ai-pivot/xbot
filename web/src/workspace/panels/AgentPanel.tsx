/**
 * AgentPanel — the Agent workspace panel.
 *
 * Wires the message + progress + ask-user hooks for one chat and composes the
 * message list, input, and ask-user surface.
 *
 * Spec C: Rewind is now inline-edit mode (no RewindDialog). The MessageList
 * carries editingMessageId state; user messages show a Pencil icon that
 * switches to an inline textarea on click.
 *
 * Chat identity:
 *   - The main Agent tab follows SessionStore.activeSession directly.
 *   - SubAgent tabs are fixed to their parent chat + role/instance params.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { useAskUser } from '@/hooks/useAskUser'
import { useChatMessages, type Attachments } from '@/hooks/useChatMessages'
import { sameSession } from "@/lib/session-grouping"
import { useCollapseLevel, useMergeTools } from '@/hooks/useCollapseLevel'
import { useAgentChatState } from '@/chat/useAgentChatState'
import { useTodos } from '@/hooks/useTodos'
import { useActiveSSESubscription } from '@/hooks/useActiveSSESubscription'
import { useSessionContext } from '@/hooks/useSessionContext'
import { useLLMSettings } from '@/hooks/useLLMSettings'
import { rewindHistory, fetchHistory, setGoal, clearGoal, getGoal } from '@/components/agent/api'
import { resolveUserMessageDBIDFromHistMsgs } from '@/components/agent/rewind'
import { postAPI } from '@/lib/api'
import type { QueueItemPayload } from '@/types/shared'

import { AskUserPanel } from '@/components/agent/AskUserPanel'
import { ContextRing } from '@/components/agent/ContextRing'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import { ModelSelector } from '@/components/agent/ModelSelector'
import { StagingTray } from '@/components/agent/StagingTray'
import { useDockviewContext } from '@/workspace/types'
import { DebugToolbar } from '@/workspace/panels/DebugToolbar'
import { useDeveloperMode } from '@/hooks/useDeveloperMode'
import type { PanelProps } from '@/workspace/panels/types'
import type { ChatMessage, GoalInfo } from '@/types/shared'
import { useI18n } from '@/providers/i18n'
// import { useOptionalPluginRuntime } from '@/plugin-runtime'


interface RewindHistoryResponse {
  draft?: string
  rewind_result?: {
    restored?: string[]
    created_del?: string[]
    skipped?: string[]
    errors?: string[]
  }
}

export function AgentPanel({ params, api }: PanelProps) {
  const ctx = useDockviewContext()
  const ws = ctx.ws
  const store = ctx.sessionStore
  const rightSidebar = ctx.rightSidebar
  const { t } = useI18n()
  const { level } = useCollapseLevel()
  const { mergeTools } = useMergeTools()
  const { enabled: devMode } = useDeveloperMode()
  const [draft, setDraft] = useState<string | undefined>(undefined)
  const [followResetToken, setFollowResetToken] = useState(0)
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null)
  const [interruptMode, setInterruptMode] = useState(false)

  // Track dockview panel visibility — only visible panels subscribe to SSE
  // (split view: both panels are visible → both subscribe; tab switch: only
  // the active tab is visible → only it subscribes). This prevents N concurrent
  // SSE connections for N open tabs (traffic explosion).
  const [isVisible, setIsVisible] = useState(true)
  useEffect(() => {
    if (!api?.onDidVisibilityChange) return
    const disp = api.onDidVisibilityChange((e: { isVisible: boolean }) => setIsVisible(e.isVisible))
    setIsVisible(api.isVisible ?? true)
    return () => disp.dispose()
  }, [api])

  // Detect SubAgent mode: when the panel carries SubAgent params, we load
  // messages via get_session_messages RPC instead of get_history.
  const isSubAgent = !!((params.subAgentRole && params.parentChatID) || params.agentChatID)

  // Session identity: each agent tab carries its own session in params
  // (session-per-tab architecture, VSCode-like). Mobile (no dockview) or the
  // seed tab (no sessionId) falls back to store.activeSession.
  const activeSession = store.activeSession
  const chatID = params.agentChatID
    ? (params.agentChatID ?? null)
    : isSubAgent
      ? (params.parentChatID ?? null)
      : (params.sessionId ?? activeSession?.chatID ?? null)
  const liveSubAgentChatID = !params.agentChatID && isSubAgent && params.subAgentRole && params.parentChatID
    ? `${params.parentChannel ?? 'web'}:${params.parentChatID}/${params.subAgentRole}${params.subAgentInstance ? `:${params.subAgentInstance}` : ''}`
    : null
  const progressChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const subscribeChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const messageChannel = params.agentChatID ? 'agent' : isSubAgent ? (params.parentChannel ?? 'web') : (params.channel ?? activeSession?.channel ?? 'web')
  const progressChannel = params.agentChatID || liveSubAgentChatID ? 'agent' : messageChannel
  // SSE subscription follows panel visibility — invisible tabs (behind another
  // tab in the same group) disconnect SSE to save bandwidth. Visible panels
  // (active tab + split-view siblings) keep their SSE alive.
  const shouldSubscribe = isVisible
  const historyEnabled = params.agentChatID
    ? !!params.agentChatID
    : isSubAgent
      ? !!chatID
      : !!chatID

  useActiveSSESubscription({
    ws,
    chatID: subscribeChatID,
    channel: progressChannel,
    active: shouldSubscribe,
  })

  const chat = useChatMessages({
    chatID,
    channel: messageChannel,
    enabled: historyEnabled,
    ws,
    subAgentRole: params.subAgentRole,
    subAgentInstance: params.subAgentInstance,
    parentChatID: params.parentChatID,
    agentChatID: params.agentChatID,
    liveEventsEnabled: shouldSubscribe,
    onSendSuccess: (info) => {
      // Optimistically mark the session as running so the UI enters busy
      // immediately — don't wait for the SSE session(busy) event which may
      // arrive late or get lost.
      if (chatID) {
        const selector = { channel: messageChannel, chatID }
        store.setStatus(selector, 'running')
      }
      // REST 成功 ack 状态机乐观行：清 sending（成功即非发送中），
      // 回填服务端 turn_id/queued。
      if (info?.requestID) ackUserRef.current(info.requestID, info.turnID, info.queued)
    },
    onSendFail: (requestID) => {
      failUserRef.current(requestID)
    },
    onCancelSuccess: () => {
      // Optimistically mark the session as idle so the UI exits busy
      // immediately — don't wait for the SSE session(idle) event.
      if (chatID) {
        const selector = { channel: messageChannel, chatID }
        store.setStatus(selector, 'idle')
      }
    },
  })
  const reloadChat = chat.reload
  const sessionContext = useSessionContext(messageChannel, isSubAgent ? null : chatID)

  // NOTE: The old wasSubscribed effect (reloadChat when shouldSubscribe
  // changes false→true) is REMOVED. When a tab becomes visible again (SSE
  // reconnects), the SSE reconnection mechanism already handles everything:
  //   1. last_event_id replay (server replays missed events)
  //   2. restoreActiveProgress (fetches get_active_progress for live state)
  //   3. resync_required → replay_gap → reloadChat() (only when gap is large)
  // Calling reloadChat() unconditionally on visibility change was clearing
  // live iterations via history_replaced, causing "live iter disappears when
  // switching to a cached tab".

  // 暴露当前会话给独立插件视图（window.__xbot_session__）。
  // 独立 ESM 插件（如 xbot.git-fancy）无法 import 宿主内部模块，通过此全局
  // 读取当前 channel/chatID，用于 ctx.rpc 拉取会话相关数据（git 状态等）。
  useEffect(() => {
    const w = window as unknown as { __xbot_session__?: { channel: string; chatID: string } }
    w.__xbot_session__ = { channel: messageChannel, chatID: progressChatID ?? '' }
  }, [messageChannel, progressChatID])

  // Fetch goal on session load/switch — handles the case where progress events
  // don't carry the goal (emitGoalProgress Phase:"" may be skipped by frontend).
  // Also handles page refresh: GetActiveProgress may not return goal if the
  // snapshot doesn't have it, so we fetch it directly via get_goal RPC.
  useEffect(() => {
    if (!chatID || !messageChannel) return
    let cancelled = false
    getGoal({ channel: messageChannel, chatID })
      .then((g) => {
        if (cancelled) return
        if (g && g.objective) {
          setGoalOverride({ objective: g.objective, status: g.status || 'active', summary: g.summary })
        } else {
          setGoalOverride(null)
        }
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [chatID, messageChannel])

  useEffect(() => {
    if (!isSubAgent) return
    return ws.onSession((ev) => {
      if (!ev.role) return
      if (params.subAgentRole && ev.role !== params.subAgentRole) return
      if ((params.subAgentInstance ?? '') && ev.instance !== params.subAgentInstance) return
      const parentID = ev.parent_id || ev.chat_id
      if (!params.agentChatID && params.parentChatID && parentID && parentID !== params.parentChatID) return
      // When the SubAgent session transitions to idle/done, reset the
      // progress store. SubAgent panels never receive `text` or `session(idle)`
      // events directly (those carry the parent's chatID), so the store
      // would stay in finalizing state forever without this reset.
      if (ev.action !== 'busy') {
        resetAgentChatRef.current()
        // Reload to fetch the SubAgent's final persisted messages. SubAgent
        // panels may never receive a `text` event (those carry the parent's
        // chatID), so onAssistantComplete won't fire — reload is the only way
        // to surface the final reply.
        void reloadChat()
      }
    })
  }, [isSubAgent, params.agentChatID, params.parentChatID, params.subAgentInstance, params.subAgentRole, reloadChat, ws])

  // ── M4：新状态机（web/src/chat/）作为唯一渲染数据源 ──
  // 全部 SSE 事件 → normalizeEvent → reduce；DB 历史 → history_replaced。
  // 旧 useProgressStream（1742 行）+ MessageStore（622 行）双轨协调已移除。
  const agentChat = useAgentChatState({
    progressChatID,
    ws,
    historyMessages: chat.messages,
    historyReady: chat.historyReady,
    // 属主门控：resolvedChatID（fetch 成功才更新）≠ 当前 chatID 时跳过
    // history dispatch —— 切会话窗口期旧会话 messages 不得灌入新 store。
    historyOwner: chat.resolvedChatID,
    historyChatID: chatID,
    initialProgress: chat.resolvedChatID === chatID ? chat.initialProgress : null,
    resetKey: `${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`,
  })
  // SubAgent idle/done 时重置（SubAgent 面板收不到 text/session(idle)）。
  const resetAgentChatRef = useRef(agentChat.reset)
  resetAgentChatRef.current = agentChat.reset
  const progressSnapshot = agentChat.liveProgress

  // ── Queue state hydration（refresh / session switch / tab 可见性恢复）──
  // SSE queue_state events only fire on enqueue/dequeue — refresh has no events to
  // restore the StagingTray. ⚰️ 2026-09-04 queue 残留修复（用户："切 tab 时
  // user msg 已 dequeue 但 web 仍显示"）：切 tab → SSE 断开 → 后端 dequeue 的 queue_state 事件丢失 → 切回后无对账 → StagingTray 残留已 dequeue 的消息，刷新才正常（remount 重新 hydrate）。修复：shouldSubscribe（面板可见性）恢复时重新拉 REST 快照对账 —— queue 是后端权威，hydrateQueue 全量替换。
  // SubAgent sessions don't have a queue (parent's session queue).
  const hydrateQueueRef = useRef(agentChat.hydrateQueue)
  hydrateQueueRef.current = agentChat.hydrateQueue
  useEffect(() => {
    if (!chatID || !messageChannel || isSubAgent) return
    let cancelled = false
    void postAPI<{ items?: QueueItemPayload[] }>('/api/queue/list', {
      channel: messageChannel,
      chat_id: chatID,
    })
      .then((resp) => {
        if (cancelled) return
        const items = Array.isArray(resp?.items) ? resp.items : []
        hydrateQueueRef.current(items)
      })
      .catch(() => {
        // non-fatal — queue hydration is best-effort (session may not have a queue)
      })
    return () => { cancelled = true }
  }, [chatID, messageChannel, isSubAgent, shouldSubscribe])

  // ── 渲染暂停（面板不可见时挂起 React 通知）──
  // MobileAppShell 用 display:none 切换视图（AgentPanel 保持挂载——store
  // 不可销毁，见 MobileAppShell 文件头不变量）。IntersectionObserver 检测
  // display:none（元素无渲染盒 → 0 交叉）→ store.pause() 挂起 rAF 通知。
  // dispatch 照常（状态机数据流完整，SSE 事件不丢）；resume 时一次 flush
  // （useSyncExternalStore 读最新 state，与"持续渲染"的最终态一致——正是
  // rAF 合并的结构保证）。桌面 DockviewContainer 的 tab 切走同样受益
  // （不可见面不渲染）。手机上切到工具页/终端页后 JS 渲染全停（此前
  // display:none 只省 paint，React reconciliation 照常满负荷跑）。
  const agentPanelRootRef = useRef<HTMLDivElement>(null)
  const pauseRenderRef = useRef(agentChat.pauseRender)
  const resumeRenderRef = useRef(agentChat.resumeRender)
  pauseRenderRef.current = agentChat.pauseRender
  resumeRenderRef.current = agentChat.resumeRender
  useEffect(() => {
    const el = agentPanelRootRef.current
    if (!el) return
    // SSR / jsdom（测试环境）安全：无 IntersectionObserver 时跳过（渲染照常）。
    if (typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => {
      const visible = entries[entries.length - 1]?.isIntersecting ?? true
      if (visible) resumeRenderRef.current()
      else pauseRenderRef.current()
    })
    io.observe(el)
    return () => {
      io.disconnect()
      // 卸载/会话切换时恢复（防 paused 泄漏——store 重建时新 store 从未
      // pause（初始 paused=false），resume 是 no-op；同 store 复用时确保
      // 残留的 paused 状态被清除，否则该 store 永久无通知）。
      resumeRenderRef.current()
    }
  }, [])
  // liveMessage comes from useProgressStream's live store — its visibility is
  // governed by the store's own hydration/reset lifecycle (initialProgress →
  // historyProgressToLive → store.replace, SSE-driven updates, reset on
  // turn_end/session-idle). It must NOT be gated on useChatMessages' loading:
  //
  // CRITICAL: a turn mid-stream can trigger resync_required (SSE ring-buffer
  // overflow with high-frequency reasoning events) / replay_gap(force) →
  // useChatMessages setLoading(true) + reload(). The reload BLANKS messages
  // (`setMessages([])` in the no-cache path) and sets loading=true. If we hid
  // liveMessage on `chat.loading` (or even `loading && messages.length === 0`),
  // the ENTIRE live turn (with all its already-rendered iterations) vanished
  // from the DOM for the ~1s reload duration — rows collapsed from 65 to 0
  // (user report + [RENDER_LOSS_ROWS] rowsLen:0). The live store stays
  // authoritative during reload; buildMessageRows merges the live row into the
  // refreshed committed rows when history lands. NEVER gate live on loading.
  // 方案 A：live 行由 store.toRows() 输出（liveMessage=null），渲染永不 gate。
  const askUser = useAskUser({ chatID, channel: messageChannel })

  const todoState = useTodos(progressSnapshot.todos)
  // goalOverride: optimistic local goal state. Set immediately after set_goal RPC
  // succeeds (before any progress event arrives). Cleared when progressSnapshot.goal
  // catches up (from SSE progress event or GetActiveProgress on refresh).
  // This avoids waiting for emitGoalProgress (which may not reach the frontend
  // reliably with Phase: "").
  const [goalOverride, setGoalOverride] = useState<GoalInfo | null | undefined>(undefined)
  const goal = progressSnapshot.goal ?? goalOverride
  // Clear override when progress snapshot catches up with a REAL goal value
  // (not null — null means "no goal in progress event", which should NOT clear
  // the override set by set_goal RPC / /goal command / getGoal RPC).
  useEffect(() => {
    if (progressSnapshot.goal) {
      setGoalOverride(undefined)
    }
  }, [progressSnapshot.goal])
  // Busy state: sessionStore.running is the primary source (same source the
  // sidebar uses — SSE session(busy)/session(idle) events). BUT after a page
  // refresh, SSE does NOT replay session(busy) for an in-flight turn, so
  // running stays false even though active_progress says the agent is
  // mid-turn (first-iteration thinking with no content yet = no liveMessage).
  // Fall back to the hydrated progressSnapshot.streaming (set true by
  // historyProgressToLive and by any stream/structured event while phase !=
  // done) so the "思考中…" placeholder still renders on refresh.
  // Per-panel session lookup: derive from this panel's own chatID/channel
  // (from params), NOT from the global activeSession. Using activeSession would
  // make split-view panels share the same busy/running state — tab A's
  // session(busy) event would set tab B's input to busy too.
  const currentSession = chatID
    ? store.sessions.find((s) => sameSession(s, { channel: messageChannel, chatID }))
    : undefined
  // busy 来源（三路 OR，覆盖所有窗口）：
  // 1. currentSession.running（SSE session(busy) 事件设置 —— 主路径）
  // 2. progressSnapshot.streaming（live turn 在跑 —— TDSM 状态机经
  //    liveProgressFromState 输出快照，phase 值域仅 'thinking'|'tool_exec'，
  //    committed/frozen turn 不进 liveProgress，无需再比较 phase）
  // 3. agentChat.busyFallback（状态机 activeTurn !== null —— 覆盖 REST ack
  //    到 turn_started 之间的窗口：sending 已清但 live turn 可能已由 lazy
  //    采纳/stream 事件建立，session(busy) 尚未到达）
  const busy = ((currentSession?.running ?? false) ||
    progressSnapshot.streaming ||
    agentChat.busyFallback) &&
    !askUser.prompt

  const llmSettings = useLLMSettings()
  const progressPromptTokens = progressSnapshot.tokenUsage?.promptTokens
  const progressTokenRef = useRef<{ key: string; promptTokens: number | null }>({
    key: '',
    promptTokens: null,
  })

  useEffect(() => {
    if (isSubAgent) return
    const key = chatID ? `${messageChannel}:${chatID}` : ''
    const exactPromptTokens = typeof progressPromptTokens === 'number' && progressPromptTokens > 0
      ? progressPromptTokens
      : null
    if (progressTokenRef.current.key !== key) {
      progressTokenRef.current = { key, promptTokens: null }
      return
    }
    if (exactPromptTokens === null || exactPromptTokens === progressTokenRef.current.promptTokens) return
    progressTokenRef.current.promptTokens = exactPromptTokens
    void sessionContext.refresh()
  }, [chatID, isSubAgent, messageChannel, progressPromptTokens, sessionContext.refresh])

  // Keep sendMessageRef before rewindTo so rewindTo can call sendMessage
  // (which increments followResetToken for scroll-follow behavior)
  const sendMessageRef = useRef(chat.sendMessage)
  sendMessageRef.current = chat.sendMessage
  // agentChat.sendUser 的 ref（sendMessage 回调用，避免闭包过期）。
  const sendUserRef = useRef(agentChat.sendUser)
  sendUserRef.current = agentChat.sendUser
  const ackUserRef = useRef(agentChat.ackUser)
  ackUserRef.current = agentChat.ackUser
  const failUserRef = useRef(agentChat.failUser)
  failUserRef.current = agentChat.failUser

  const sendMessage = useCallback((content: string, attachments?: Attachments, interrupt?: boolean) => {
    setFollowResetToken((v) => v + 1)
    // Detect /goal command and optimistically set goalOverride (frontend-only,
    // no backend needed — progress event may not carry goal reliably).
    if (content.startsWith('/goal ') && !content.startsWith('/goal status') && !content.startsWith('/goal clear')) {
      const objective = content.slice(6).trim()
      if (objective) {
        setGoalOverride({ objective, status: 'active' })
      }
    }
    // ⚡ Interject mode: skip optimistic rendering (no user row — the message
    // appears inside the active turn as a user_interrupt tool via SSE).
    const rid = `req-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    if (!interrupt) {
      sendUserRef.current(content, rid)
    }
    sendMessageRef.current(content, attachments, rid, interrupt)
  }, [setGoalOverride])

  // Goal handlers — direct RPC (does not trigger a Run, just updates the goal text)
  const handleSetGoal = useCallback(async (objective: string) => {
    if (!chatID || !messageChannel) return
    try {
      await setGoal({ channel: messageChannel, chatID }, objective)
      // Optimistic update: set goal locally so UI updates immediately without
      // waiting for a progress event (emitGoalProgress may not reach frontend reliably).
      setGoalOverride({ objective, status: 'active' })
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to set goal')
    }
  }, [chatID, messageChannel, setGoalOverride])

  const handleClearGoal = useCallback(async () => {
    if (!chatID || !messageChannel) return
    try {
      await clearGoal({ channel: messageChannel, chatID })
      // Optimistic update: clear goal locally.
      setGoalOverride(null)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to clear goal')
    }
  }, [chatID, messageChannel, setGoalOverride])

  // chatRef：rewindTo/footer 的稳定闭包读取（useChatMessages 每帧返回新对象，
  // 若 rewindTo deps 含 chat 则每帧重建 → 传给 MessageList 的 onRewind 引用
  // 每帧变化 → 击穿 MessageList/MessageItem 的 memo。ref 化后 deps 全部低频
  // （chatID/isSubAgent/messageChannel/ws/t），回调行为不变（调用时读最新 chat）。
  const chatRef = useRef(chat)
  chatRef.current = chat

  // ── user 缺失自动补拉（"强刷恢复"的程序化等价，user msg 消失根治）──
  // SSE 丢 turn_started/user_echo（断连窗口/ring evict/coalescing）后，
  // lazy 采纳 commit 的 turn user=null（DOM: turn-c 相邻无 user 行；DB
  // eager-save 有 user 行 —— 强刷恢复证明）。运行中无 reload 触发 → user
  // 永缺。检测最新 committed turn 无 user 行 → 程序化 reload：fetchHistory →
  // history_replaced 的 mergeTurnData（user: cur.user ?? h.user）嫁接 DB user
  // —— 一次 reload 补齐所有缺 user 的 committed turn（不只最新）。防抖：每
  // turnID 只触发一次（reload 失败由下次 resync/会话切换兜底；resume turn
  // DB 本无 user 行 → reload 后仍缺 → Set 防抖挡住，不循环）。
  const userMissingReloadedRef = useRef<Set<string>>(new Set())
  useEffect(() => {
    if (isSubAgent || !chatID) return
    const msgs = agentChat.messages
    // 最新 committed assistant 行（跳过 live/frozen —— 在跑的 turn user 可能未绑定）
    let lastCommitted: ChatMessage | undefined
    for (let i = msgs.length - 1; i >= 0; i--) {
      const m = msgs[i]
      if (m.role === 'assistant' && !m.isPartial) { lastCommitted = m; break }
    }
    if (!lastCommitted || !lastCommitted.turnID) return
    const turnID = lastCommitted.turnID
    // 复合 key（chatID:turnID）：turnID 是 per-session 计数器（各会话都从 1
    // 开始），而 MobileAppShell 的 <AgentPanel> 无 key —— 会话切换不重挂载，
    // 同一组件实例跨会话复用这个 Set。裸 turnID 会让会话 A 的 turn 3 挡掉
    // 会话 B 的 turn 3（reload 被永久跳过，user 行缺失直到手刷）。
    const dedupKey = `${chatID}:${turnID}`
    if (userMissingReloadedRef.current.has(dedupKey)) return
    // 同 turnID 的 user 行缺失（deriveRows：user 行 turnID 绑定 turn ——
    // pendingUsers 沉底行 turnID=MAX_SAFE_INTEGER 不误判）
    const hasUserRow = msgs.some((m) => m.role === 'user' && m.turnID === turnID)
    if (hasUserRow) return
    userMissingReloadedRef.current.add(dedupKey)
    void chatRef.current.reload()
  }, [agentChat.messages, isSubAgent, chatID])

  // Rewind via inline edit: rewind to the message's DB id, then send
  // the edited content as a new message.
  const rewindTo = useCallback(async (editedContent: string, originalMessage: ChatMessage) => {
    if (!chatID || isSubAgent) return
    // User messages rendered from user_echo SSE carry persisted=true but no
    // dbID — the DB id is assigned when the agent loop persists the message,
    // AFTER the echo is sent at queue-admission time. Resolve the id from a
    // direct history API call — bypass chat.reload() which can return null
    // due to requestIsSuperseded() race conditions when SSE events fire
    // during the await.
    try {
      // User messages rendered from user_echo SSE carry persisted=true but no
      // dbID — the DB id is assigned when the agent loop persists the message,
      // AFTER the echo is sent at queue-admission time. Resolve the id from a
      // direct history API call — bypass chat.reload() which can return null
      // due to requestIsSuperseded() race conditions when SSE events fire
      // during the await.
      let dbID = originalMessage.dbID
      if (!dbID) {
        const data = await fetchHistory(ws, { channel: messageChannel, chatID }, { limit: 100 })
        dbID = resolveUserMessageDBIDFromHistMsgs(data.messages ?? [], originalMessage)
      }
      if (!dbID) {
        toast.error(t('agent.rewindUnavailable'))
        return
      }
      await rewindHistory<RewindHistoryResponse>({ channel: messageChannel, chatID }, dbID)
      // Exit edit mode
      setEditingMessageId(null)
      // Rewind is destructive: clear the visible/cache rows before reload so
      // an empty truncated history is not mistaken for a background refresh.
      chatRef.current.clearMessages()
      // Rewind MUST also reset the state machine — otherwise the pre-rewind
      // turn's live residue is committed on the next turn_started,
      // re-rendering the rewind-deleted assistant below the new turn.
      resetAgentChatRef.current()
      // Reload FIRST to fetch the truncated history from the server.
      // This must happen BEFORE sendMessage — otherwise sendMessage increments
      // messageMutationGenRef, the subsequent reload captures the incremented
      // value, requestHasMessageMutation() returns false, and the optimistic
      // message is silently wiped by the fresh history.
      await chatRef.current.reload()
      // Send the edited content as a new message (sendMessage increments
      // followResetToken so the viewport scrolls to bottom for the response)
      sendMessage(editedContent)
      toast.success(t('agent.rewindComplete'))
    } catch (e) {
      // Keep edit mode active when the rewind request fails.
      toast.error(e instanceof Error ? e.message : t('agent.rewindFailed'))
    }
  }, [chatID, isSubAgent, messageChannel, ws, t, sendMessage])

  const rewindLatest = useCallback(() => {
    if (busy) return
    const candidates = rewindCandidates(agentChat.messages)
    if (candidates.length === 0) {
      toast.error(t('agent.noUserMessageToRewind'))
      return
    }
    // Enter edit mode for the latest rewindable user message
    const latest = candidates[candidates.length - 1]
    setEditingMessageId(latest.id)
  }, [busy, agentChat.messages, t])

  const handleStartEdit = useCallback((messageId: string) => {
    setEditingMessageId(messageId)
  }, [])

  const handleEndEdit = useCallback(() => {
    setEditingMessageId(null)
  }, [])

  // footer（AskUserPanel）：useMemo 保持引用稳定 —— AgentPanel 每帧 re-render
  // （AgentPanel 是状态机订阅点，busy/draft/context 等任何变化都触发整树
  // re-render）时，footer JSX 若每帧新对象会击穿 MessageList 的 React.memo
  // （props 浅比较失败）。deps 全部为稳定引用：prompt（store 稳定对象，
  // 变化时新引用）、respond/cancel（useAskUser 的 useCallback）、isSubAgent
  // （boolean）。chat.reload 走 chatRef（闭包读最新，不进 deps）。
  const askUserFooter = useMemo(() => {
    if (!askUser.prompt || isSubAgent) return null
    return (
      <AskUserPanel
        prompt={askUser.prompt}
        onRespond={(answers) => {
          askUser.respond(answers)
          // Deterministic: the backend persists the answer as a user
          // message; reload history so it renders with its authoritative
          // turn_id (NO optimistic rendering).
          void chatRef.current.reload()
        }}
        onCancel={askUser.cancel}
      />
    )
  }, [askUser.prompt, askUser.respond, askUser.cancel, isSubAgent])

  return (
    <div ref={agentPanelRootRef} className="flex h-full min-h-0 flex-col">
      {!ws.connected && !isSubAgent && chatID && (
        <div className="flex items-center gap-2 border-b border-border/50 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-600 dark:text-amber-400">
          <Loader2 className="size-3 animate-spin" />
          <span>{t('agent.reconnecting') || 'Reconnecting…'}</span>
        </div>
      )}
      {!isSubAgent && devMode && (
        <DebugToolbar
          ws={ws}
          getStateSnapshot={() => ({
            meta: {
              channel: messageChannel,
              chatID: progressChatID ?? null,
              isSubAgent: Boolean(isSubAgent),
              capturedAt: Date.now(),
            },
            // New state-machine snapshot (liveProgress + derived rows).
            chatState: { liveProgress: progressSnapshot, messages: agentChat.messages, busy },
            // Committed message rows as currently rendered — the other half of
            // "100% frontend reconstruction": the turn-vanish symptom is a
            // rendering-state divergence, so the baseline must include the
            // committed list, not just the live store.
            chat: {
              messages: chat.messages,
              resolvedChatID: chat.resolvedChatID,
            },
            session: { busy },
          })}
        />
      )}
      <MessageList
        chatKey={`${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`}
        followResetToken={followResetToken}
        messages={agentChat.messages}
        liveProgress={progressSnapshot}
        busy={busy}
        collapseLevel={level}
        mergeTools={mergeTools}
        loading={chat.loading}
        loadingMore={chat.loadingMore}
        hasMore={chat.hasMore}
        onLoadMore={chat.loadMore}
        error={chat.error}
        onRewind={isSubAgent ? undefined : rewindTo}
        editingMessageId={editingMessageId}
        onStartEdit={handleStartEdit}
        onEndEdit={handleEndEdit}
        footer={askUserFooter}
      />
      {!isSubAgent && (
        <StagingTray
          items={agentChat.queue}
          busy={busy}
          onCancel={(msgID) => chat.cancelQueued(msgID)}
          onInterject={(msgID) => {
            const item = agentChat.queue.find((q) => q.msg_id === msgID)
            // CR#2: use the FULL content — preview is server-truncated to ~80
            // runes (queue tray rendering only); interjecting with it would
            // deliver a truncated message to the agent AND the queued original
            // is already cancelled (unrecoverable content loss for >80-rune
            // messages). Content falls back to preview for entries admitted
            // before this field existed (server upgrade window).
            if (item) chat.interjectQueued(msgID, item.content || item.preview)
          }}
          onClear={() => {
            agentChat.queue.forEach((q) => chat.cancelQueued(q.msg_id))
          }}
        />
      )}
      {!isSubAgent && (
        <MessageInput
          key={`${messageChannel}:${chatID ?? ''}`}
          busy={busy}
          cancelling={chat.cancelling}
          onSend={sendMessage}
          onCancel={askUser.prompt ? askUser.cancel : chat.cancel}
          onRewindLatest={rewindLatest}
          onOpenTasks={() => rightSidebar.openPanel('tasks')}
          onUpload={chat.upload}
          todoState={todoState.total > 0 ? todoState : null}
          goal={goal}
          onSetGoal={handleSetGoal}
          onClearGoal={handleClearGoal}
          interruptMode={interruptMode}
          onInterruptModeChange={setInterruptMode}
          trailingControls={
            chatID ? (
              <>
                <ContextRing
                  available={sessionContext.available}
                  promptTokens={sessionContext.promptTokens}
                  maxContext={sessionContext.maxContext}
                  usagePercent={sessionContext.usagePercent}
                />
                <ModelSelector
                  channel={messageChannel}
                  chatID={chatID}
                  currentSubID={sessionContext.subscriptionID}
                  currentModel={sessionContext.model}
                  subscriptions={llmSettings.data.subscriptions}
                  modelEntries={llmSettings.data.modelEntries}
                  thinkingMode={llmSettings.data.thinkingMode}
                  busy={busy}
                  saving={llmSettings.saving}
                  onModelSelected={sessionContext.refresh}
                  onThinkingModeChange={llmSettings.setThinkingMode}
                />
              </>
            ) : null
          }
          draft={draft}
          onDraftConsumed={() => setDraft(undefined)}
          sessionKey={`${messageChannel}:${chatID ?? ''}`}
        />
      )}
    </div>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  const boundary = latestCompactBoundaryIndex(messages)
  return messages.filter((m, i) => i > boundary && m.role === 'user' && !!m.timestamp && m.persisted === true)
}
