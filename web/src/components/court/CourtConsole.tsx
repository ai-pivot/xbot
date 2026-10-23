/**
 * CourtConsole —— Agent 通信视图完整面板（2026-10-22 第二次重新设计）。
 *
 * 布局：顶栏 · 左侧组织树（紧凑行）· 主视图（活动流 / 流向图 / 线程）· 右侧详情 · 回放条。
 *
 * 2026-10-23 两处整改：
 *   P1 组织树数据源 —— 只渲染「当前会话的直属子代理」（parent_chat_id == 当前会话
 *      且 status != unloaded），历史会话 / 孤儿 / 一次性 explore 折叠，默认 ≤ 20 条；
 *      消息只扫「当前会话 + 直属」这几个会话（旧实现乱扫前 40 个会话）。
 *   P2 界面降噪 —— 空态收成单栏（不再渲染三栏骨架）、顶栏两行说明收进一个「?」弹层、
 *      无事件不渲染回放条、右栏空态插画缩小。
 *
 * 这一版的核心变化（用户 10 条具体反馈）：
 *   1. **默认视图 = 活动流**：类飞书/Slack 的垂直时间线，一条事件一行，普通人一眼能读；
 *      流向图（原 Sankey）降级为可切换的分析视图，空数据时不再显示一块空白画布。
 *   2. **术语全中文化**：Sankey→消息流向，correlation_id→同一次对话/对话编号，
 *      泳道→会话轨道（正文里尽量不提），长解释收进「这是什么？」。
 *   3. **组织树改紧凑行**：44px/行 + 状态点 + 未读 + 12px sparkline，hover 给最近事件。
 *   4. **图例可区分**：空闲青灰 / 忙碌琥珀 / 等待处理红 / 已完成绿；「已离开」不进图例。
 *
 * 数据（见 ./sources.ts）：组织树 = POST /api/session-tree；通信流 = 各会话
 * GET /api/messages 里 kind ∈ {report,ask_user,answer,decision,command,event} 的行。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { fetchGroups, fetchMembers, fetchMessages, subscribeGroupMessages } from './api'
import { DEMO_GROUP_ID, buildDemoMessages, demoGroup, demoMembers, deriveLanes } from './seed'
import { emptyReasonFor, statusLabel, statusTitle, type CourtConn } from './connection'
import {
  buildOrgTree, fetchCourtEvents, fetchSessionTree, lanesFromSessions,
  type CourtEvent, type CourtSession, type OrgTreeGroup,
} from './sources'
import { ActivityFeed, KindLegend, messageSummary } from './ActivityFeed'
import { CourtEmptyState } from './CourtEmptyState'
import { OrgTree } from './OrgTree'
import { PlaybackBar } from './PlaybackBar'
import { SankeyView } from './SankeyView'
import { ThreadPanel, ThreadsView } from './ThreadPanel'
import { TimelineCanvas } from './TimelineCanvas'
import type { Group, GroupMember, GroupMessage, Lane } from './types'
import { KIND_LABELS, senderAddr } from './types'
import { appendStructuredMessage } from '@/structured/api'
import { postAPI } from '@/lib/api'

const WINDOW_OPTIONS = [
  { ms: 15 * 60e3, label: '15 分钟' },
  { ms: 60 * 60e3, label: '1 小时' },
  { ms: 6 * 60 * 60e3, label: '6 小时' },
  { ms: 24 * 60 * 60e3, label: '全天' },
  { ms: Number.MAX_SAFE_INTEGER, label: '全部' },
]

type CourtView = 'feed' | 'flow' | 'threads' | 'timeline'

const VIEW_LABELS: Record<CourtView, string> = {
  feed: '活动流',
  flow: '流向图',
  threads: '线程',
  timeline: '时间轴',
}

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** 消息归属的会话轨道 id：结构化事件自带 laneID，群组消息退回发送者地址。 */
function laneIdOf(m: GroupMessage): string {
  const lane = (m as CourtEvent).laneID
  return lane || senderAddr(m)
}

export function CourtConsole({ demo = false }: { demo?: boolean }) {
  // 只有 /court-demo（离线预览）才预置演示数据；真实面板从空状态开始，
  // API 失败时展示错误 + 空视图，绝不伪造内容。
  const [sessions, setSessions] = useState<CourtSession[]>([])
  const [events, setEvents] = useState<CourtEvent[]>([])
  const [groups, setGroups] = useState<Group[]>(demo ? [demoGroup] : [])
  const [activeGroup, setActiveGroup] = useState<string>(demo ? DEMO_GROUP_ID : '')
  const [members, setMembers] = useState<GroupMember[]>(demo ? demoMembers : [])
  const [groupMessages, setGroupMessages] = useState<GroupMessage[]>(demo ? buildDemoMessages() : [])
  const [source] = useState<'api' | 'seed'>(demo ? 'seed' : 'api')
  const [conn, setConn] = useState<CourtConn>(demo ? 'online' : 'loading')
  const [error, setError] = useState<string | null>(null)
  const [emptyReason, setEmptyReason] = useState<string | null>(null)
  const [streamChat, setStreamChat] = useState('')
  // 默认 = 活动流（普通人第一眼要能读懂；流向图是可切换的分析视图）。
  const [view, setView] = useState<CourtView>('feed')
  const [windowMs, setWindowMs] = useState(24 * 60 * 60e3)
  const [selected, setSelected] = useState<GroupMessage | null>(null)
  const [laneFilter, setLaneFilter] = useState<string | null>(null)
  const [, setLaneHeight] = useState(92)
  const [refreshing, setRefreshing] = useState(false)
  const [sendingTest, setSendingTest] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)

  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(10)
  const [fallbackNow] = useState(() => Date.now())
  const [cursor, setCursor] = useState<number>(() => Date.now())
  const cursorRef = useRef(cursor)
  const playRef = useRef({ playing: false, speed: 10, min: 0, max: 0 })

  const load = useCallback(async (signal?: AbortSignal) => {
    setConn('loading')
    setError(null)
    setEmptyReason(null)
    const errors: string[] = []

    // ① 组织树（真实子代理层级）
    let tree: CourtSession[] = []
    let treeOk = false
    try {
      tree = await fetchSessionTree(signal)
      treeOk = true
    } catch (e) {
      if (signal?.aborted) return
      errors.push(errorText(e))
    }

    // ② 通信流：只扫「当前会话 + 直属子代理」这几个会话。
    //    旧实现按 session-tree 的前 40 个会话扫（顺序里混着大量历史会话）⇒
    //    请求数与命中率都离谱；现在扫描集合由组织树选择器给出（见 buildOrgTree）。
    let nextEvents: CourtEvent[] = []
    if (treeOk && tree.length > 0) {
      const scan = buildOrgTree(tree).scan
      const res = await fetchCourtEvents(scan.length > 0 ? scan : tree, signal)
      if (signal?.aborted) return
      nextEvents = res.events
    }

    // ③ 群组（可选叠加：没有群组不再导致整页空）
    let nextGroups: Group[] = []
    let nextMembers: GroupMember[] = []
    let nextGroupMessages: GroupMessage[] = []
    let nextChat = ''
    let groupOk = false
    try {
      nextGroups = await fetchGroups(signal)
      groupOk = true
      if (nextGroups.length > 0) {
        const g = nextGroups[0]
        const [ms, page] = await Promise.all([
          fetchMembers(g.id, signal),
          fetchMessages(g.id, { limit: 200 }, signal),
        ])
        nextMembers = ms
        nextGroupMessages = page.items
        nextChat = page.chat_id ?? ''
      }
    } catch (e) {
      if (signal?.aborted) return
      errors.push(errorText(e))
    }
    if (signal?.aborted) return

    if (!treeOk && !groupOk) {
      setError(errors[0] ?? '请求失败')
      setSessions([])
      setEvents([])
      setGroups([])
      setMembers([])
      setGroupMessages([])
      setStreamChat('')
      setConn('offline')
      return
    }

    setSessions(tree)
    setEvents(nextEvents)
    setGroups(nextGroups)
    setActiveGroup(nextGroups[0]?.id ?? '')
    setMembers(nextMembers)
    setGroupMessages(nextGroupMessages)
    setStreamChat(nextChat)
    const hasData = tree.length > 0 || nextEvents.length > 0 || nextGroups.length > 0
    setEmptyReason(hasData ? null : emptyReasonFor(nextGroups.length, nextEvents.length))
    setConn('online')
  }, [])

  useEffect(() => {
    if (demo) {
      setConn('online')
      return
    }
    const ac = new AbortController()
    void load(ac.signal)
    return () => ac.abort()
  }, [load, demo])

  const refresh = useCallback(() => {
    setRefreshing(true)
    void load().finally(() => setRefreshing(false))
  }, [load])

  useEffect(() => {
    if (source !== 'api' || streamChat === '') return
    return subscribeGroupMessages(streamChat, (m) => {
      setGroupMessages((prev) => (prev.some((x) => x.id === m.id) ? prev : [...prev, m]))
    })
  }, [source, streamChat])

  const messages: GroupMessage[] = useMemo(() => {
    if (demo) return groupMessages
    if (events.length === 0) return groupMessages
    const offset = 1_000_000
    const merged = [
      ...events,
      ...groupMessages.map((m) => ({ ...m, id: offset + m.id })),
    ]
    return merged.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  }, [demo, events, groupMessages])

  // 组织树模型：当前会话 + 直属子代理 + 折叠历史（纯函数，见 ./sources.ts）。
  const org = useMemo(() => buildOrgTree(sessions), [sessions])

  // 展示用会话集合 = 扫描集合（当前会话 + 直属）+ 历史（历史只在展开时才渲染）。
  // 历史必须也在里面，否则「显示历史」打开后一行都点不出来（泳道缺失）。
  const displaySessions = useMemo(() => {
    if (org.scan.length === 0) return sessions
    const seen = new Set(org.scan.map((s) => s.laneID))
    return [...org.scan, ...org.history.filter((s) => !seen.has(s.laneID))]
  }, [org.scan, org.history, sessions])

  const lanes: Lane[] = useMemo(() => {
    if (demo) return deriveLanes(demoMembers, messages)
    // 只给「会被扫描的会话」建泳道：组织树里点得到、活动流里读得懂，两边同一份。
    const real = lanesFromSessions(displaySessions, events)
    const fromGroups = members.length > 0 ? deriveLanes(members, groupMessages) : []
    const seen = new Set(real.map((l) => l.id))
    return [...real, ...fromGroups.filter((l) => !seen.has(l.id))]
  }, [demo, displaySessions, events, members, groupMessages, messages])

  const laneByID = useMemo(() => {
    const m = new Map<string, Lane>()
    for (const l of lanes) m.set(l.id, l)
    return m
  }, [lanes])

  // 分组模式下只显示直属子代理；定不出当前会话（或演示数据）时退回平铺。
  const grouped = !demo && org.currentChatID !== null && lanes.length > 0
  const orgGroups: OrgTreeGroup[] | undefined = useMemo(() => {
    if (!grouped) return undefined
    const fromTree = org.groups
      .map((g) => ({ ...g, laneIDs: g.laneIDs.filter((id) => laneByID.has(id)) }))
      .filter((g) => g.laneIDs.length > 0)
    if (fromTree.length > 0) return fromTree
    // 树里一个都没有但还有群组成员泳道 ⇒ 平铺（避免整栏空掉）
    return undefined
  }, [grouped, org.groups, laneByID])

  const directLanes = useMemo(() => {
    if (!orgGroups) return lanes
    const ids = new Set(orgGroups.flatMap((g) => g.laneIDs))
    return lanes.filter((l) => ids.has(l.id))
  }, [orgGroups, lanes])

  const historyLanes = useMemo(() => {
    if (!grouped) return []
    const ids = new Set(org.history.map((s) => s.laneID))
    return lanes.filter((l) => ids.has(l.id) && !directLanes.some((d) => d.id === l.id))
  }, [grouped, org.history, lanes, directLanes])

  const currentLane = useMemo(
    () => (org.currentChat ? laneByID.get(org.currentChat.laneID) ?? null : null),
    [org.currentChat, laneByID],
  )

  const hasData = lanes.length > 0

  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const m of messages) {
      const from = laneIdOf(m)
      c[from] = (c[from] ?? 0) + 1
      for (const to of m.to ?? []) c[to] = (c[to] ?? 0) + 1
    }
    return c
  }, [messages])

  const pending = useMemo(() => {
    const answered = new Set(
      messages
        .filter((m) => (m.kind === 'answer' || m.kind === 'decision') && m.correlation_id)
        .map((m) => m.correlation_id),
    )
    return messages.filter((m) => m.kind === 'ask_user' && m.correlation_id && !answered.has(m.correlation_id))
  }, [messages])

  // 组织树的未读数：本轨道上还没人回答的提问。
  const unread = useMemo(() => {
    const out: Record<string, number> = {}
    for (const m of pending) {
      const id = laneIdOf(m)
      out[id] = (out[id] ?? 0) + 1
    }
    return out
  }, [pending])

  // 组织树的最近事件（tooltip）与 sparkline（最近 6 个时间桶）。
  const latest = useMemo(() => {
    const out: Record<string, string> = {}
    const sorted = [...messages].sort(
      (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
    )
    for (const m of sorted) out[laneIdOf(m)] = `${KIND_LABELS[m.kind] ?? m.kind}：${messageSummary(m)}`
    return out
  }, [messages])

  const timeRange = useMemo(() => {
    if (messages.length === 0) return { min: fallbackNow - 3600e3, max: fallbackNow }
    const ts = messages.map((m) => new Date(m.created_at).getTime()).filter(Number.isFinite)
    if (ts.length === 0) return { min: fallbackNow - 3600e3, max: fallbackNow }
    return { min: Math.min(...ts), max: Math.max(...ts) }
  }, [messages, fallbackNow])

  const series = useMemo(() => {
    const out: Record<string, number[]> = {}
    const span = Math.max(1, timeRange.max - timeRange.min)
    const bucket = span / 6
    for (const m of messages) {
      const t = new Date(m.created_at).getTime()
      if (!Number.isFinite(t)) continue
      const id = laneIdOf(m)
      const arr = out[id] ?? [0, 0, 0, 0, 0, 0]
      const idx = Math.min(5, Math.max(0, Math.floor((t - timeRange.min) / bucket)))
      arr[idx] += 1
      out[id] = arr
    }
    return out
  }, [messages, timeRange])

  useEffect(() => {
    setCursor((c) => (c < timeRange.min || c > timeRange.max ? timeRange.max : c))
  }, [timeRange])
  cursorRef.current = cursor
  playRef.current = { playing, speed, min: timeRange.min, max: timeRange.max }

  // 动画节拍（脉冲/流动点）+ 回放推进。
  useEffect(() => {
    let raf = 0
    let last = performance.now()
    const loop = (t: number) => {
      const dt = Math.min(120, t - last)
      last = t
      const p = playRef.current
      if (p.playing && p.max > p.min) {
        const span = p.max - p.min
        const rate = (span / 60) * p.speed
        const next = Math.min(p.max, cursorRef.current + (rate * dt) / 1000)
        cursorRef.current = next
        setCursor(next)
        if (next >= p.max) setPlaying(false)
      }
      raf = requestAnimationFrame(loop)
    }
    raf = requestAnimationFrame(loop)
    return () => cancelAnimationFrame(raf)
  }, [])

  const onSelect = useCallback((m: GroupMessage | null) => setSelected(m), [])
  const onPickSankey = useCallback((from: string, to: string) => {
    setLaneFilter(from)
    setView('feed')
    const hit = messages.find((m) => laneIdOf(m) === from && (m.to ?? []).includes(to))
    if (hit) setSelected(hit)
  }, [messages])

  const laneNameOf = useCallback((id: string) => {
    const lane = lanes.find((l) => l.id === id)
    if (lane) return lane.name
    return id.replace('agent:', '').replace('session:', '').split(':')[0]
  }, [lanes])

  const visibleMessages = laneFilter
    ? messages.filter((m) => laneIdOf(m) === laneFilter || (m.to ?? []).includes(laneFilter))
    : messages

  /** 整页空态：不渲染三栏骨架（左组织树 / 右详情栏 / 回放条都不出现）。 */
  const singleColumnEmpty = !demo && conn !== 'loading' && lanes.length === 0
  /** 回放条只在真有事件时出现（无事件的时间轴滑块没有意义）。 */
  const hasPlayback = !singleColumnEmpty && messages.length > 0 && lanes.length > 0

  // 「发送一条测试消息」：往第一个会话写一条 event（走公开 API，不碰生产库）。
  // 一条会话都没有时**先建一个**（否则空态里的按钮点了没反应 = 假按钮）。
  const sendTestMessage = useCallback(async () => {
    setSendingTest(true)
    try {
      let channel = sessions[0]?.channel
      let chatID = sessions[0]?.chatID
      if (!channel || !chatID) {
        const created = await postAPI<{ chat_id: string; channel?: string }>('/api/chats/create', {
          label: '通信视图测试',
          channel: 'web',
        })
        channel = created.channel || 'web'
        chatID = created.chat_id
      }
      await appendStructuredMessage({
        kind: 'event',
        text: '这是一条测试事件：由通信视图的「发送一条测试消息」按钮写入。',
        payload: { summary: '测试事件：来自通信视图空态按钮', source: 'court-ui' },
        channel,
        chat_id: chatID,
        idempotency_key: `court-test-${Date.now()}`,
      })
      await load()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setSendingTest(false)
    }
  }, [sessions, load])

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-bg-primary text-text-primary">
      {/* 顶栏 */}
      <header className="flex shrink-0 flex-wrap items-center gap-3 border-b border-border bg-bg-secondary px-4 py-2.5">
        <div className="flex items-center gap-2">
          <span className="flex size-7 items-center justify-center rounded-lg bg-bg-tertiary text-[13px] text-app-accent">◈</span>
          <div className="text-[13px] font-semibold tracking-wide text-text-primary">通信视图</div>
        </div>

        <div className="flex items-center gap-2">
          {groups.length > 1 ? (
            <select
              value={activeGroup}
              onChange={(e) => {
                setActiveGroup(e.target.value)
                setSelected(null)
              }}
              className="rounded-md border border-border bg-bg-primary px-2 py-1 text-[11px] text-text-primary outline-none"
              aria-label="选择群组"
            >
              {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </select>
          ) : groups.length === 1 ? (
            <span className="text-[11px] text-text-secondary">{groups[0].name}</span>
          ) : null}
          <span
            data-testid="court-conn-badge"
            data-conn={conn}
            title={statusTitle({ conn, error, emptyReason }, demo)}
            className={
              conn === 'offline'
                ? 'rounded-full bg-destructive/10 px-2 py-0.5 text-[9px] text-destructive'
                : 'rounded-full bg-lane-done/10 px-2 py-0.5 text-[9px] text-lane-done'
            }
          >
            {statusLabel({ conn }, demo)}
          </span>
          {/* 唯一的「?」：说明 + 状态含义都收进这个弹层（P2：顶栏不再并排两行说明）。 */}
          <button
            type="button"
            data-testid="court-help-toggle"
            aria-expanded={helpOpen}
            aria-label="这个页面是什么"
            title="这个页面是什么"
            onClick={() => setHelpOpen((v) => !v)}
            className="rounded-full border border-border px-1.5 py-0.5 text-[9px] text-text-muted hover:bg-bg-tertiary hover:text-text-primary"
          >
            ?
          </button>
        </div>

        <div className="ml-auto flex items-center gap-2">
          {/* 视图切换器**始终**可用：空态下也要能切到「流向图/线程」去看它们的
              空态说明（隐藏它会让空态变成一个死角，用户只能看到活动流）。 */}
          {conn !== 'loading' ? (
            <>
              {hasData && pending.length > 0 ? (
                <span className="rounded-md bg-lane-waiting/10 px-2 py-1 text-[10px] text-lane-waiting">
                  待处理 {pending.length}
                </span>
              ) : null}
              {/* 分段控件：三个视图，默认停在活动流。 */}
              <div data-testid="court-view-switch" className="flex overflow-hidden rounded-md border border-border bg-bg-primary p-0.5">
                {(['feed', 'flow', 'threads'] as const).map((v) => (
                  <button
                    key={v}
                    type="button"
                    aria-pressed={view === v}
                    onClick={() => setView(v)}
                    className={
                      view === v
                        ? 'rounded bg-app-accent/15 px-2.5 py-1 text-[10px] font-medium text-app-accent'
                        : 'rounded px-2.5 py-1 text-[10px] text-text-secondary hover:bg-bg-tertiary'
                    }
                  >
                    {VIEW_LABELS[v]}
                  </button>
                ))}
                <button
                  type="button"
                  aria-pressed={view === 'timeline'}
                  onClick={() => setView('timeline')}
                  title="时间轴：按部门把事件画在时间线上（可缩放/回放）"
                  className={
                    view === 'timeline'
                      ? 'rounded bg-app-accent/15 px-2.5 py-1 text-[10px] font-medium text-app-accent'
                      : 'rounded px-2.5 py-1 text-[10px] text-text-secondary hover:bg-bg-tertiary'
                  }
                >
                  {VIEW_LABELS.timeline}
                </button>
              </div>
              {view === 'flow' && hasData ? (
                <select
                  value={windowMs}
                  onChange={(e) => setWindowMs(Number(e.target.value))}
                  className="rounded-md border border-border bg-bg-primary px-2 py-1 text-[10px] text-text-primary outline-none"
                  aria-label="时间窗口"
                >
                  {WINDOW_OPTIONS.map((o) => <option key={o.label} value={o.ms}>{o.label}</option>)}
                </select>
              ) : null}
            </>
          ) : null}
          <button
            type="button"
            onClick={refresh}
            disabled={refreshing}
            className="rounded-md bg-bg-tertiary px-2 py-1 text-[10px] text-text-secondary hover:text-text-primary disabled:opacity-50"
          >
            {refreshing ? '刷新中…' : '刷新'}
          </button>
        </div>

        {/* 全部解释收在一个「?」弹层里（正文不占行）。 */}
        {helpOpen ? (
          <div data-testid="court-help-popover" className="w-full rounded-md border border-border bg-bg-primary px-3 py-2 text-[10.5px] leading-5 text-text-secondary">
            <p className="font-medium text-text-primary">这是什么</p>
            <p>所有部门之间的汇报、请旨（提问）、批红，按时间排成一条流。</p>
            <p className="mt-1">
              左边组织树只列<strong className="font-medium text-text-primary">当前会话的直属子代理</strong>
              ，可按角色折叠；历史会话、已销毁的孤儿会话、一次性探索代理收在「显示历史」里。
              圆点颜色是它的状态：空闲青灰 / 忙碌琥珀 / 等待处理红 / 已完成绿。
            </p>
            <p className="mt-1">
              中间四个视图：<strong className="font-medium text-text-primary">活动流</strong>（一条事件一行，默认）·
              <strong className="font-medium text-text-primary"> 流向图</strong>（只看跨部门消息）·
              <strong className="font-medium text-text-primary"> 线程</strong>（同一「对话编号」聚成一条）·
              <strong className="font-medium text-text-primary"> 时间轴</strong>（按时间缩放看密度与先后）。
              点一条事件 → 右边看详情；底部是回放条，拖动时上方实时过滤。
            </p>
            <p className="mt-1 text-text-muted">
              「已连接 / 离线」= 后端会话树与消息接口是否可达；离线时页面上是历史快照或空态。
            </p>
          </div>
        ) : null}
      </header>

      {/* 示例数据角标（只有 /court-demo）：「示例」必须写在页面上，不能只靠人事后知道。 */}
      {demo ? (
        <div
          data-testid="court-sample-badge"
          className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border bg-lane-busy/10 px-4 py-1.5 text-[10px] text-text-secondary"
        >
          <span className="rounded bg-lane-busy/15 px-1.5 py-0.5 font-semibold text-lane-busy">示例数据</span>
          本页的部门、消息、时间都是演示用的编造内容，不代表真实状态；真实数据请打开 /court。
        </div>
      ) : null}

      {/* 在线但无数据时的说明：这是空态，不是故障。
          文案刻意不提「群组」—— 那是内部概念，用户看到的是「没有数据」。 */}
      {conn === 'online' && emptyReason && !demo ? (
        <div
          data-testid="court-empty-reason"
          className="flex shrink-0 items-center gap-2 border-b border-border bg-bg-secondary px-4 py-1.5 text-[10px] text-text-secondary"
        >
          <span className="size-1.5 rounded-full bg-app-accent" />
          已连接。当前没有可展示的通信数据 —— 下面任一视图都会在你产生第一条消息后自动更新。
        </div>
      ) : null}

      {error ? (
        <div data-testid="court-error" className="flex shrink-0 items-center gap-2 border-b border-border bg-destructive/10 px-4 py-1.5 text-[10px] text-destructive">
          读取失败：{error}
          <button type="button" onClick={refresh} className="ml-auto rounded bg-destructive/15 px-2 py-0.5">重试</button>
        </div>
      ) : null}

      {/* P2：空态收成**单栏** —— 不再渲染三栏骨架（左空树 + 中小卡 + 右空插画），
          空白占比从 87% 降到一屏之内说完「是什么 / 为什么空 / 点哪里」。 */}
      {singleColumnEmpty ? (
        <CourtEmptyState
          conn={conn}
          emptyReason={emptyReason}
          onRefresh={refresh}
          onSendTest={sendTestMessage}
          sendingTest={sendingTest}
          hasSessions={sessions.length > 0}
        />
      ) : (
      <div className="flex min-h-0 flex-1">
        <OrgTree
          lanes={directLanes}
          groups={orgGroups}
          history={historyLanes}
          overflow={org.overflow}
          currentLane={grouped ? currentLane : null}
          laneFilter={laneFilter}
          onLaneFilter={setLaneFilter}
          counts={counts}
          unread={unread}
          series={series}
          latest={latest}
          onRefresh={refresh}
          hasSessions={sessions.length > 0 || demo}
        />
        {conn === 'loading' && lanes.length === 0 ? (
          <div className="flex min-h-0 min-w-0 flex-1 items-center justify-center bg-bg-primary text-xs text-text-muted">
            正在读取会话树与结构化消息…
          </div>
        ) : view === 'feed' ? (
          <ActivityFeed
            messages={visibleMessages}
            lanes={lanes}
            selectedId={selected?.id ?? null}
            onSelect={onSelect}
            laneNameOf={laneNameOf}
            cutoff={cursor}
            onRefresh={refresh}
            onSendTest={sendTestMessage}
            sendingTest={sendingTest}
          />
        ) : view === 'flow' ? (
          <SankeyView
            messages={visibleMessages}
            lanes={lanes}
            windowMs={windowMs}
            now={timeRange.max}
            onPick={onPickSankey}
            onSwitchToFeed={() => setView('feed')}
          />
        ) : view === 'threads' ? (
          <ThreadsView
            messages={visibleMessages}
            selected={selected}
            onSelect={setSelected}
            onSwitchToFeed={() => setView('feed')}
            laneNameOf={laneNameOf}
          />
        ) : (
          <TimelineCanvas
            lanes={lanes}
            messages={visibleMessages}
            selectedId={selected?.id ?? null}
            onSelect={onSelect}
            laneFilter={laneFilter}
            onLaneFilter={setLaneFilter}
            cutoff={cursor}
            onLaneHeightChange={setLaneHeight}
            onRefresh={refresh}
          />
        )}
        <ThreadPanel
          messages={visibleMessages}
          selected={selected}
          onSelect={setSelected}
          onClose={() => setSelected(null)}
          showList={view !== 'threads'}
          laneNameOf={laneNameOf}
        />
      </div>
      )}

      {/* P2：一条事件都没有时不渲染回放条（滑一条空时间轴 = 纯噪音）；空态单栏时也不渲染。 */}
      {hasPlayback ? (
        <PlaybackBar
          min={timeRange.min}
          max={timeRange.max}
          cursor={cursor}
          playing={playing}
          speed={speed}
          onSeek={(t) => { setPlaying(false); setCursor(t) }}
          onToggle={() => {
            if (cursor >= timeRange.max - 1) setCursor(timeRange.min)
            setPlaying((p) => !p)
          }}
          onSpeed={setSpeed}
          onLive={() => { setPlaying(false); setCursor(timeRange.max) }}
        />
      ) : null}

      {/* 图例 / 快捷键（降噪：只有真有内容时才提示交互） */}
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-t border-border bg-bg-secondary px-4 py-1 text-[9px] text-text-muted">
        <KindLegend />
        {hasPlayback ? (
          <span className="ml-auto">拖动时间轴实时过滤 · 点组织树某一行只看该部门</span>
        ) : null}
      </div>
    </div>
  )
}
