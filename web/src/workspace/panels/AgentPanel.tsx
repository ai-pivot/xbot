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
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { useAskUser } from '@/hooks/useAskUser'
import { useChatMessages, type Attachments } from '@/hooks/useChatMessages'
import { sameSession } from "@/lib/session-grouping"
import { useAgentChatState } from '@/chat/useAgentChatState'
import { useTodos } from '@/hooks/useTodos'
import { usePendingEdit, goalEqual, todosListEqual } from '@/hooks/usePendingEdit'
import { useActiveSSESubscription } from '@/hooks/useActiveSSESubscription'
import { useSessionContext } from '@/hooks/useSessionContext'
import { subscribeLLMConfigChanged, useLLMSettings } from '@/hooks/useLLMSettings'
import { rewindHistory, fetchHistory, setGoal, clearGoal, getGoal, updateTodos } from '@/components/agent/api'
import { resolveUserMessageDBIDFromHistMsgs } from '@/components/agent/rewind'
import { postAPI } from '@/lib/api'
import type { QueueItemPayload } from '@/types/shared'

import { AskUserPanel } from '@/components/agent/AskUserPanel'
import { ContextRing } from '@/components/agent/ContextRing'
import { ToolSessionContext } from '@/components/agent/ToolSessionContext'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import { ModelSelector } from '@/components/agent/ModelSelector'
import { sessionSwitch } from '@/lib/sessionSwitch'
import { StagingTray } from '@/components/agent/StagingTray'
import { useDockviewContext } from '@/workspace/types'
import { DebugToolbar } from '@/workspace/panels/DebugToolbar'
import { useDeveloperMode } from '@/hooks/useDeveloperMode'
import type { PanelProps } from '@/workspace/panels/types'
import type { PanelParams } from '@/types/tab'
import type { ChatMessage, GoalInfo, TodoItem } from '@/types/shared'
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

export function AgentPanel({ params, api, containerApi }: PanelProps) {
  const ctx = useDockviewContext()
  const ws = ctx.ws
  const store = ctx.sessionStore
  const rightSidebar = ctx.rightSidebar
  const { t } = useI18n()
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
  // 占位 agent tab（无 sessionId）是「引导槽」：只有当它**独占** main agent 面板时
  // 才跟随 activeSession（移动端 / 首个会话尚未选择时的既有形态）。若已存在绑定会话
  // 的 agent tab（session tab）而占位再跟随同一会话，两个面板会同时挂载同一会话 ⇒
  // 消息列表整棵渲染两份、`/api/history` 拉两次（2026-09-16「切会话后同一 user 行
  // 重复渲染」）。不变量：一个会话至多被一个 agent 面板渲染
  //（另一半修复在 useTabManager.openTab：会话 tab 认领占位 tab，不再新建面板）。
  const isPlaceholderMainAgent = !params.sessionId && !isSubAgent && !params.agentChatID
  const [panelSetVersion, setPanelSetVersion] = useState(0)
  useEffect(() => {
    if (!isPlaceholderMainAgent || !containerApi?.onDidAddPanel) return
    const onAdd = containerApi.onDidAddPanel(() => setPanelSetVersion((v) => v + 1))
    const onRemove = containerApi.onDidRemovePanel(() => setPanelSetVersion((v) => v + 1))
    return () => {
      onAdd.dispose()
      onRemove.dispose()
    }
  }, [containerApi, isPlaceholderMainAgent])
  const sessionOwnedByPeerPanel = useMemo(() => {
    if (!isPlaceholderMainAgent || !activeSession?.chatID) return false
    return (containerApi?.panels ?? []).some((p) => {
      if (p.id === api?.id) return false
      const pp = p.params as PanelParams | undefined
      return (
        !!pp && pp.type === 'agent' && pp.sessionId === activeSession.chatID && !pp.subAgentRole && !pp.agentChatID
      )
    })
    // panelSetVersion: 面板增删后重新判定（session tab 关闭 ⇒ 占位回到引导态）。
  }, [containerApi, api, isPlaceholderMainAgent, activeSession?.chatID, panelSetVersion])
  const chatID = params.agentChatID
    ? (params.agentChatID ?? null)
    : isSubAgent
      ? (params.parentChatID ?? null)
      : (params.sessionId ?? (sessionOwnedByPeerPanel ? null : (activeSession?.chatID ?? null)))
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
      if (info?.requestID) {
        ackUserRef.current(info.requestID, info.turnID, info.queued)
      }
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
  // ── 一致性暂态修复（用户 2026-09-15）──
  // ① 切到 busy 会话时「几秒只看得到 live iter，历史很久才出来」：live 由状态机即时归约，
  //    而历史要等 fetchHistory —— 渲染层在 history 未就绪时必须显示 loading，
  //    而不是先给一个"只有 live"的不一致画面。
  // ② 手机锁屏半天再打开「不触发重新加载、SSE 追赶期间画面剧烈抖动」：隐藏超过阈值 ⇒
  //    恢复可见时强制整屏重载（DB 权威历史），重载期间同样显示 loading 屏幕
  //    （用户明确偏好：「不如展示 loading 屏幕」）。
  const [resumeLoading, setResumeLoading] = useState(false)
  const hiddenAtRef = useRef<number | null>(null)
  useEffect(() => {
    const onVisibility = () => {
      if (document.hidden) {
        hiddenAtRef.current = Date.now()
        return
      }
      const hiddenAt = hiddenAtRef.current
      hiddenAtRef.current = null
      if (hiddenAt === null || Date.now() - hiddenAt < 60_000) return
      setResumeLoading(true)
      void reloadChat()
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [reloadChat])

  // ── 重新订阅后补齐断连期间丢失的**行**（P0，2026-09-16 用户报告）────────────
  // 现象（用户截图 + 描述）：「切回缓存的 tab，user 消息消失，刷新才恢复」，且消失的
  // 一定是**通知变成的 user 行**（🔔 Notification）。
  //
  // 机制（三处实证）：
  //   1) 通知行的**唯一载体**是 turn_started(trigger='notification', content)
  //      （chat/reduce.ts 的 notifContent 分支；迟到 inject_user 会被 dbID 过滤掉）。
  //   2) 面板不可见时 SSE 会**主动断开**（useActiveSSESubscription，active=isVisible）
  //      ⇒ 该事件既没实时到达，也未必在 last_event_id 重放窗口里。
  //   3) 兜底只在**可检测到 seq gap**时触发（providers/sseConnection 的
  //      resync_required → replay_gap → reloadChat）。仅"游标推进 + 断连"不产生 gap
  //      ⇒ 不触发任何 reconcile ⇒ 已持久化到 DB 的那行通知不会自己回来（只有手刷）。
  //
  // 修复：在"不可见 → 可见"（= 重新订阅）时做一次**非破坏性**历史对账。
  // `reloadChat` 走 history_replaced 的 **merge 语义**（DB 覆盖它【有】的 turn，
  // 状态机持有的 live / post-fetch commit 一律保留），所以不会再出现当年
  // "live 迭代被 history_replaced 清掉"的问题（见本文件 203-210 行的历史备注）。
  // ⛔ 「一次新的会话激活」= 面板重新变为可见（切 tab / 点侧栏进入该会话）。
  // 用户判据（2026-09-18）：「会话只要开始切换就应该渲染 loading 了，这才是修复」。
  // 面板里保存的是**上一次可见时**的快照 —— 直接渲染它再等后台对账回来改写，就是
  // 用户看到的那「一瞬间的渲染错误」。所以进入即回到 loading，等 DB 权威历史落地。
  // 必须用 **useLayoutEffect**：与"变为可见"落在同一帧（paint 前提交），否则会先画
  // 一帧旧内容再翻成 loading（那仍是一帧错误渲染）。
  const markHistoryStaleRef = useRef(chat.markHistoryStale)
  markHistoryStaleRef.current = chat.markHistoryStale
  const wasSubscribedRef = useRef(shouldSubscribe)
  useLayoutEffect(() => {
    const was = wasSubscribedRef.current
    wasSubscribedRef.current = shouldSubscribe
    if (was || !shouldSubscribe || !chatID) return
    markHistoryStaleRef.current()
    void reloadChat()
  }, [shouldSubscribe, chatID, reloadChat])
  // 历史落地（重载完成且已有消息）后收起 loading 屏幕。
  useEffect(() => {
    if (resumeLoading && !chat.loading && chat.messages.length > 0) setResumeLoading(false)
  }, [resumeLoading, chat.loading, chat.messages.length])
  // 注意：只看 `=== false`（历史确实未就绪）；undefined（测试/旧调用方）视为就绪，
  // 避免把 loading 屏幕变成常驻。
  // ⚠️ 只在"确实有会话、但它的历史还没到"时才用 loading 屏幕遮挡面板。
  // 无会话（chatID 为空 / 会话树为空，如全新安装的 E2E 环境）时**绝不能**挡：
  // 那会把输入区一起盖住，用户既看不到空状态也无法创建/发送（CI 的
  // chat.spec"should show user message after sending" 就是这样红的 —— 快照里侧栏是
  // "No sessions yet — create one from the top-right"、面板只有 Loading…）。
  // ⛔ SSE 断开（重连中）不再显示黄色 "Reconnecting…" 条 —— 2026-09-17 用户要求：
  // 「把黄色的 reconnecting… 去掉，以后这个期间直接显示 loading 的 splash screen」。
  // 重连期间与"历史还没到"是同一语义（面板暂时不可用），统一用 loading splash 表达，
  // 不再另设一条提示（那条黄条既丑又和 splash 表达同一件事）。
  // ⛔ 断线遮挡面板只适用于**曾经连上过**再掉线的「真·重连」（2026-09-17 CI E2E 实测）：
  // 从未连上（初次加载 / 没有 SSE 的 mock 场景）绝不能遮罩 —— 那会把已渲染的历史一起藏起来
  // （实测：`!ws.connected` 一刀切 ⇒ 非 SSE 的 spec 被判成 loading，8 个 E2E 找不到内容）。
  const sawConnectedRef = useRef(false)
  if (ws.connected) sawConnectedRef.current = true
  const reconnecting = !ws.connected && sawConnectedRef.current
  const showLoadingScreen =
    (chat.historyReady === false && !!chatID) || resumeLoading || (reconnecting && !!chatID && !isSubAgent)
  // ⛔ 「换会话/进入新会话」的加载态 = 面板**只渲染 loading 屏**（不渲染消息区/托盘/输入框）。
  // 理由（2026-09-18 用户报告「切换会话一闪而过、DOM 抓不到的错误布局」，附截图：
  // 消息区**上方浮着一排输入框控件**=回形针/ContextRing/发送按钮）：
  // 新面板被 dockview 以**未兑现的尺寸**布局一帧时，flex 会把 `flex-1 min-h-0` 的消息区
  // 压到 0、把输入框（自然高度）顶到面板顶部 ⇒ 那排控件就出现在消息区上方一闪而过。
  // 加载态本就不该出现任何输入控件（也无法使用），从结构上不渲染它们 ⇒ 该类瞬态不可能出现。
  // ⚠️ 只对「会话加载」生效（`historyReady===false`）；`resumeLoading`/`reconnecting`
  // 仍保留输入框 —— 那两种情况面板可能有用户草稿，卸载会丢草稿（且它们不在顶部布局）。
  const sessionLoading = chat.historyReady === false && !!chatID
  // 切换过渡态（sessionSwitch）：只要存在**指向其他面板**的切换，本面板只渲染 loading
  // —— 这是「会话只要开始切换就应该渲染 loading」的实现点。目标面板历史就绪后 end()。
  const pendingSwitch = useSyncExternalStore(sessionSwitch.subscribe, sessionSwitch.get)
  const panelSwitchKey = `agent:${messageChannel}:${chatID ?? ''}`
  const switchSplash = pendingSwitch !== null && pendingSwitch.key !== panelSwitchKey
  useEffect(() => {
    if (pendingSwitch && pendingSwitch.key === panelSwitchKey && chat.historyReady) {
      sessionSwitch.end(pendingSwitch.key)
    }
  }, [pendingSwitch, panelSwitchKey, chat.historyReady])
  const sessionContext = useSessionContext(messageChannel, isSubAgent ? null : chatID)

  // NOTE: 这里曾经把 `wasSubscribed`（shouldSubscribe false→true 时 reloadChat）
  // 整个移除，理由是把 reloadChat 无条件挂在 visibilitychange 上会经
  // history_replaced 清掉 live 迭代（"切到缓存 tab 后 live iter 消失"）。
  //
  // 但 2026-09-16 用户报告暴露了移除后的**空隙**：通知变成的 user 行（🔔）在
  // 面板不可见期间**丢失后无法自愈**——它的唯一载体 turn_started(trigger=
  // notification) 既没实时到达（SSE 因不可见已断开），也未必能被 last_event_id
  // 重放覆盖；而 reconcile 只在**可检测到 seq gap** 时才触发，断连+游标推进不产生
  // gap ⇒ 该行永久缺失，只有手刷（全量加载）恢复。
  //
  // 现在恢复了这条对账（见上方 `wasSubscribedRef` 的 effect），但用**非破坏性**的
  // reloadChat：history_replaced 已是 merge 语义（DB 覆盖它【有】的 turn，状态机持有
  // 的 live / post-fetch commit 保留，见 chat/reduce.ts 的 history_replaced 注释），
  // 所以不会再清 live 迭代。若将来 history_replaced 退回"盲替换"，这个 effect 会重新
  // 咬人——届时必须同时修 reduce。

  // 暴露当前会话给独立插件视图（window.__xbot_session__）。
  // 独立 ESM 插件（如 xbot.git-fancy）无法 import 宿主内部模块，通过此全局
  // 读取当前 channel/chatID，用于 ctx.rpc 拉取会话相关数据（git 状态等）。
  // ⚡ 空值不覆盖（协议修复 2026-09-06）：布局恢复的 agent tab 无 sessionId 参数，
  // activeSession 未加载时 progressChatID 为空——旧代码写入 chatID:'' 会把
  // usePluginRuntimeHost 设置的正确身份毒化（resolveChat 对空值回落 'default'
  // → 后端创建幽灵会话 → git 面板显示"不是 git 仓库"且不自愈）。跳过空值写入，
  // 保留 bootstrap 写入的正确身份。
  useEffect(() => {
    if (!progressChatID) return
    const w = window as unknown as { __xbot_session__?: { channel: string; chatID: string } }
    w.__xbot_session__ = { channel: messageChannel, chatID: progressChatID }
  }, [messageChannel, progressChatID])

  // get_goal 兜底水合的稳定入口（在 effect 里用 ref，避免 render 期读 agentChat；
  // 赋值见下方 resetAgentChatRef 附近）。
  const hydrateSessionFieldsRef = useRef<(f: { goal?: GoalInfo | null }) => void>(() => {})

  // Fetch goal on session load/switch — handles the case where progress events
  // don't carry the goal (emitGoalProgress Phase:"" may be skipped by frontend).
  // Also handles page refresh: GetActiveProgress may not return goal if the
  // snapshot doesn't have it, so we fetch it directly via get_goal RPC.
  // 水合走状态机（hydrateSessionFields —— 会话级字段单一数据源）。
  useEffect(() => {
    if (!chatID || !messageChannel) return
    let cancelled = false
    getGoal({ channel: messageChannel, chatID })
      .then((g) => {
        if (cancelled) return
        // 只写**非空**值：`goal: null` 是"显式清除"语义，而 get_goal 返回空可能
        // 只是读取窗口/落库延迟（CR：会把用户刚提交的 goal 抹掉）。清除由后端 push
        // 的 cleared 标记驱动。
        if (g && g.objective) {
          hydrateSessionFieldsRef.current({ goal: { objective: g.objective, status: g.status || 'active', summary: g.summary } })
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
  // Per-panel session lookup: derive from this panel's own chatID/channel
  // (from params), NOT from the global activeSession. Using activeSession would
  // make split-view panels share the same busy/running state — tab A's
  // session(busy) event would set tab B's input to busy too.
  //
  // ⚠️ 必须早于 useAgentChatState：状态机的 turn live-ness **服从**这个 running
  // （不变量：输入框 = cancel ⇒ 上面必须显示进行中信号 —— 见 chat/types.ts 的
  // `session_running`）。
  const currentSession = chatID
    ? store.sessions.find((s) => sameSession(s, { channel: messageChannel, chatID }))
    : undefined

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
    // 会话 running（服务端 reconcile 权威）—— 状态机的 turn live-ness 服从它
    //（不变量：输入框 = cancel ⇒ 上面必须显示进行中信号）。
    sessionRunning: currentSession?.running ?? false,
  })
  // SubAgent idle/done 时重置（SubAgent 面板收不到 text/session(idle)）。
  const resetAgentChatRef = useRef(agentChat.reset)
  resetAgentChatRef.current = agentChat.reset
  hydrateSessionFieldsRef.current = agentChat.hydrateSessionFields
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

  // ── 拖动调序（Staging Tray）──
  // 后端把新顺序投影到真实投递通道（msgCh）后回传权威快照：拖动期间被
  // dequeue 的消息已不在里面，客户端没列出的项保持服务端顺序 —— 因此用响应
  // 覆盖本地顺序（本地提交顺序可能落后于并发 dequeue）。
  const handleReorderQueue = useCallback((msgIDs: string[]) => {
    if (!chatID || !messageChannel) return
    void postAPI<{ items?: QueueItemPayload[] }>('/api/queue/reorder', {
      channel: messageChannel,
      chat_id: chatID,
      msg_ids: msgIDs,
    })
      .then((resp) => {
        hydrateQueueRef.current(Array.isArray(resp?.items) ? resp.items : [])
      })
      .catch(() => {
        // 队列以后端为权威 —— 下次 queue_state / 对账快照会纠正显示顺序
        toast.error(t('agent.staging.reorderFailed'))
      })
  }, [chatID, messageChannel, t])

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

  // ⚠️ 熄屏/后台恢复必须“主动” resume 渲染通知（2026-09-18 用户复现：「熄屏解锁后
  // loading 结束但消息区冻死 —— 头部速率在动、消息不再更新」）。上面那个
  // IntersectionObserver 只在**交叉状态变化**时回调：页面被 OS 冻结/隐藏期间浏览器
  // 可能投递一次 isIntersecting=false（→ pause），恢复可见时若交叉状态未再变化就
  // **没有回调** ⇒ store 永久 paused（dispatch 照常、React 永不重渲染，表现为
  // “状态机在动但 UI 冻死”）。这里在 visibilitychange→可见 / pageshow(bfcache) /
  // focus 时按需 resume：只有面板真有渲染盒（非 display:none）才恢复，避免把
  // 移动端隐藏视图（工具页/终端页）也解暂停。
  useEffect(() => {
    const resumeIfVisible = () => {
      const el = agentPanelRootRef.current
      if (!el) return
      if (el.offsetParent !== null || el.getBoundingClientRect().width > 0) {
        resumeRenderRef.current()
      }
    }
    const onVisibilityChange = () => {
      if (!document.hidden) resumeIfVisible()
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('pageshow', resumeIfVisible)
    window.addEventListener('focus', resumeIfVisible)
    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange)
      window.removeEventListener('pageshow', resumeIfVisible)
      window.removeEventListener('focus', resumeIfVisible)
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

  // 会话级字段（goal / todos）的**用户编辑乐观覆盖**：RPC 成功后立刻显示用户提交的
  // 值；服务端快照相对"提交前的值"发生变化（push 收敛 / agent 第三方改动）时让位 —
  // 服务端始终是权威。修复"编辑后恢复旧内容、刷新才生效"（2026-09-12 用户报告，
  // goal 与 todos 同类：显示优先级写反 + 过度清除覆盖 + todos 没有乐观副本）。
  //
  // 单一数据源：goal 的兜底水合（get_goal RPC，见下方两个 effect）经
  // `agentChat.hydrateSessionFields` 写进状态机 —— 组件不再保留 shadow state
  //（旧 `fallbackGoal` 的 `snapshot.goal ?? fallback` 会在快照显式清除（null）时
  // 静默回退到过期值，banner 显示已删除的目标）。
  //
  // 会话身份：覆盖不得跨会话存活（CR：A 的覆盖会在 B 的快照恰好等于 A 的 before 时
  // 继续渲染 —— 最典型"两会话都没 goal"，`null == null` → 永不自愈）。
  const editKey = `${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}`
  const [goal, goalEdit] = usePendingEdit(progressSnapshot.goal, goalEqual, editKey)
  const [todos, todosEdit] = usePendingEdit(progressSnapshot.todos, todosListEqual, editKey)
  const todoState = useTodos(todos)
  // 编辑句柄 ref 化：handle* 的 deps 保持低频（chatID/messageChannel），
  // 不让 goal/todos 快照每帧都换回调引用（MessageInput 依赖 onSend 等回调）。
  const goalEditRef = useRef(goalEdit)
  goalEditRef.current = goalEdit
  const todosEditRef = useRef(todosEdit)
  todosEditRef.current = todosEdit
  // Busy state: sessionStore.running is the primary source (same source the
  // sidebar uses — SSE session(busy)/session(idle) events). BUT after a page
  // refresh, SSE does NOT replay session(busy) for an in-flight turn, so
  // running stays false even though active_progress says the agent is
  // mid-turn (first-iteration thinking with no content yet = no liveMessage).
  // Fall back to the hydrated progressSnapshot.streaming (set true by
  // historyProgressToLive and by any stream/structured event while phase !=
  // done) so the "思考中…" placeholder still renders on refresh.
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
    !askUser.prompt &&
    // waiting_input (AskUser pending) is mutually exclusive with busy/running:
    // the turn is PAUSED, so the input must not show the generating/stop state.
    // Covers the window where a stale backend running flag still says busy.
    currentSession?.status !== 'waiting_input'

  // Turn 结束（busy→idle 边沿）时重取 get_goal —— goal 状态变化的事件兜底：
  // set_goal_complete 后端 emitGoalProgress 会推 goal 事件（TDSM 实时更新），
  // 但 SSE 丢事件 / 事件被合并时 banner 会滞留旧状态，RPC 兜底保证收敛。
  const prevBusyRef = useRef(busy)
  useEffect(() => {
    const was = prevBusyRef.current
    prevBusyRef.current = busy
    if (was && !busy && chatID && messageChannel) {
      // Stale-guard（xbotgh CR）：慢响应跨会话切换会把旧会话的 goal 写进新会话的
      // banner —— 与 session-load getGoal effect（cancelled flag 模式）保持一致。
      let cancelled = false
      getGoal({ channel: messageChannel, chatID })
        .then((g) => {
          if (cancelled) return
          // 只写非空（同上：空返回不等于"用户删除目标"）。
          if (g && g.objective) {
            hydrateSessionFieldsRef.current({ goal: { objective: g.objective, status: g.status || 'active', summary: g.summary } })
          }
        })
        .catch(() => {})
      return () => { cancelled = true }
    }
  }, [busy, chatID, messageChannel])

  const llmSettings = useLLMSettings()

  // Session-level LLM info (current model / subscription / context limits) also
  // depends on server-side LLM config: after the settings dialog adds, updates or
  // removes a subscription — or changes the default — the session's resolution can
  // change (e.g. the session was bound to the edited sub). Re-resolve it here so
  // the selector bar shows the new model/limits immediately, no page refresh.
  const sessionRefreshRef = useRef(sessionContext.refresh)
  useEffect(() => {
    sessionRefreshRef.current = sessionContext.refresh
  }, [sessionContext.refresh])
  useEffect(() => subscribeLLMConfigChanged(() => void sessionRefreshRef.current()), [])
  // Vision state of the CURRENT model (purely manual per-model switch — NO
  // built-in whitelist). Read from the owning subscription's per_model_configs
  // (sessionContext.subscriptionID + model); undefined when the model is
  // unknown (no hint shown). MessageInput uses this for the "vision off"
  // advisory bar and the image-sent confirmation toast.
  // 计算极轻（两次数组 find），不用 useMemo——React Compiler 对 source/inferred
  // 依赖不一致会报 preserve-manual-memoization（推断 sessionContext.subscriptionID
  // vs 手写 sessionContext?.subscriptionID），直接计算更简单且无行为差异。
  const currentModelVision = (() => {
    const model = sessionContext?.model
    if (!model) return undefined
    const subs = llmSettings.data.subscriptions
    if (!subs || subs.length === 0) return undefined
    const sub = subs.find((s) => s.id === sessionContext?.subscriptionID)
      ?? subs.find((s) => s.per_model_configs?.[model] != null)
    return Boolean(sub?.per_model_configs?.[model]?.vision)
  })()
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
    // ⚡ Interject mode: skip optimistic rendering (no user row — the message
    // appears inside the active turn as a user_interrupt tool via SSE).
    const rid = `req-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    // ⚠️ 这里**不得**乐观设置 goal（2026-09-16 用户报告）：goal 按钮的语义只是把
    // 消息加上 `/goal ` 前缀 —— 消息排队时 goal 并没有生效。goal 只能在后端 pop
    // 该消息并真正执行 `/goal` 之后设置（后端 push / getGoal 回读收敛）。此前在
    // 发送瞬间就写入乐观覆盖 ⇒ 排队期间 banner 已显示新目标（用户："排队的 goal
    // 应该 pop 之后才设置"）。
    if (!interrupt) {
      sendUserRef.current(content, rid)
    }
    sendMessageRef.current(content, attachments, rid, interrupt)
  }, [])

  // Goal handlers — direct RPC (does not trigger a Run, just updates the goal text)
  // 并发编辑用 begin() 的单调序号：RPC 乱序返回时落后的提交不得回写（CR 缺陷 2）。
  const handleSetGoal = useCallback(async (objective: string) => {
    if (!chatID || !messageChannel) return
    const seq = goalEditRef.current.begin()
    try {
      await setGoal({ channel: messageChannel, chatID }, objective)
      // 乐观覆盖：RPC 成功后立刻显示新目标，不等后端 push（push 到达即收敛让位）。
      goalEditRef.current.commit({ objective, status: 'active' }, seq)
    } catch (e) {
      goalEditRef.current.discard()
      toast.error(e instanceof Error ? e.message : 'Failed to set goal')
    }
  }, [chatID, messageChannel])

  // User edits the checklist (rename / toggle done / delete). 服务端持久化后会把新
  // 列表 push 回进度流；在 push 到达前用乐观覆盖显示用户提交的列表（push 收敛即
  // 让位，丢失也不回退 —— 2026-09-12 用户报告"编辑后回退、刷新才生效"）。
  const handleUpdateTodos = useCallback(
    async (todos: TodoItem[]) => {
      if (!chatID || !messageChannel) return
      const seq = todosEditRef.current.begin()
      try {
        await updateTodos(
          { channel: messageChannel, chatID },
          todos.map((it) => ({ text: it.text, status: it.status })),
        )
        todosEditRef.current.commit(todos, seq)
      } catch (e) {
        todosEditRef.current.discard()
        toast.error(e instanceof Error ? e.message : 'Failed to update todos')
      }
    },
    [chatID, messageChannel],
  )

  const handleClearGoal = useCallback(async () => {
    if (!chatID || !messageChannel) return
    const seq = goalEditRef.current.begin()
    try {
      await clearGoal({ channel: messageChannel, chatID })
      // 乐观覆盖：立刻移除 banner（服务端 push 为显式 cleared 标记，到达即收敛）。
      goalEditRef.current.commit(null, seq)
    } catch (e) {
      goalEditRef.current.discard()
      toast.error(e instanceof Error ? e.message : 'Failed to clear goal')
    }
  }, [chatID, messageChannel])

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
    // 分页窗口把**这一个 turn 切开**时不要补拉（2026-09-13 同 cursor 重复请求根因）：
    // 最老的那一行就是本 turn 的 assistant/tool 行 ⇒ 本 turn 的 user 行在更老的、
    // 尚未加载的页里（DB 行 id 严格递增，user 行 id 必然小于窗口里本 turn 的任何
    // 行）⇒ 再 reload 一次只会把**同一页**原样拉回来：既补不出 user 行（长 turn
    // 首屏稳定出现 #2 before_id == #1 before_id 的重复 fetch），也白占一次请求。
    // 反面：若最老一行属于**更早的 turn**，则本 turn 的 user 行落在已加载窗口内，
    // 缺失只可能是 SSE 丢事件 ⇒ reload 确实能把它带回来，补拉必须保留。
    const oldest = msgs[0]
    if (oldest && oldest.role !== 'user' && oldest.turnID === turnID) return
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

  // ⛔ 幽灵面板（2026-09-18 用户截图「切换会话闪烁一瞬间错误布局」根因之一）：
  // 占位 tab（无 sessionId）在**已有别的 agent 面板承载 activeSession** 时 chatID=null
  // （不镜像，避免同一会话两个面板渲染两份）。但它此前**仍然渲染整块面板 UI**（欢迎
  // 空态 + MessageInput）—— 而 agent tab 是 `renderer='always'`（常驻 DOM），dockview
  // 在切换瞬间会重排分组/尺寸 ⇒ 这些**没有任何会话可承载**的输入框控件会漏进可见区
  // （实测：帧级 E2E 里出现 `(seed)|vis=1|rect=…` 与真实面板**完全重叠**，切换瞬间甚至
  // 先分屏）。
  // 契约：占位面板**不承载会话时不渲染任何面板 UI**（tab 本身仍在，尺寸归 dockview）；
  // 只有它真的在镜像一个会话（引导态 / 独占）时才渲染，否则一律 null。
  if (isPlaceholderMainAgent && sessionOwnedByPeerPanel) return null

  return (
    <ToolSessionContext.Provider
      value={{ channel: progressChannel, chatID: progressChatID }}
    >
    <div
      ref={agentPanelRootRef}
      data-agent-chat-id={chatID ?? ''}
      data-agent-visible={isVisible ? '1' : '0'}
      className="relative flex h-full min-h-0 flex-col"
    >
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
      {!(showLoadingScreen || switchSplash) && isVisible ? (
      <MessageList
        chatKey={`${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`}
        followResetToken={followResetToken}
        messages={agentChat.messages}
        liveProgress={progressSnapshot}
        busy={busy}
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
      ) : null}
      {!isSubAgent && isVisible && !(sessionLoading || switchSplash) && (
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
          onReorder={handleReorderQueue}
        />
      )}
      {!isSubAgent && isVisible && !(sessionLoading || switchSplash) && (
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
          onUpdateTodos={handleUpdateTodos}
          onSetGoalTodo={handleSetGoal}
          interruptMode={interruptMode}
          onInterruptModeChange={setInterruptMode}
          modelVision={currentModelVision}
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
      {/* ⛔ loading = **覆盖整块面板的不透明覆盖层**（不是替换消息区）。
          用户的「切换会话一闪而过」实测为：面板在切换那一帧被 dockview 以**未兑现的尺寸**
          布局，flex 把消息区（flex-1 min-h-0）压到 0，而 MessageInput（自然高度）仍占位
          ⇒ 它被顶到面板**顶部**（= 截图里消息区上方那排回形针/Clock/Stop 控件），下一帧
          尺寸兑现又回到底部。所以：① 会话加载态（history 未就绪）**根本不渲染输入框/托盘**；
          ② 其余 loading 态（reconnecting / 长时间恢复）输入框**保持挂载**（草稿不丢、不闪），
          但由这层**不透明覆盖层**盖住整块面板 ⇒ 任何一帧的错位布局都不可能被看见。 */}
      {(showLoadingScreen || switchSplash) && (
        <div
          data-testid="session-loading-screen"
          className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2 bg-bg-primary text-text-muted"
        >
          <Loader2 className="size-5 animate-spin" />
          <span className="text-xs">Loading…</span>
        </div>
      )}
    </div>
    </ToolSessionContext.Provider>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  const boundary = latestCompactBoundaryIndex(messages)
  return messages.filter((m, i) => i > boundary && m.role === 'user' && !!m.timestamp && m.persisted === true)
}
