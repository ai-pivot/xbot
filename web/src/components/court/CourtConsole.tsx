/**
 * CourtConsole —— Agent 通信视图完整面板。
 *
 * 布局：顶栏 · 左侧组织树 · 主视图（时间轴/Sankey）· 右侧线程详情 · 底部回放条。
 * 数据：真实 `/api/groups*` + `group.message` SSE；无数据/失败时切到内置 seed。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { fetchGroups, fetchMembers, fetchMessages, subscribeGroupMessages } from './api'
import {
  DEMO_GROUP_ID, buildDemoMessages, demoGroup, demoMembers, deriveLanes,
} from './seed'
import { emptyReasonFor, statusLabel, statusTitle, type CourtConn } from './connection'
import { OrgTree } from './OrgTree'
import { PlaybackBar } from './PlaybackBar'
import { SankeyView } from './SankeyView'
import { ThreadPanel } from './ThreadPanel'
import { TimelineCanvas } from './TimelineCanvas'
import type { Group, GroupMember, GroupMessage, Lane } from './types'
import { KIND_COLORS, KIND_LABELS, senderAddr } from './types'

const WINDOW_OPTIONS = [
  { ms: 15 * 60e3, label: '15 分钟' },
  { ms: 60 * 60e3, label: '1 小时' },
  { ms: 6 * 60 * 60e3, label: '6 小时' },
  { ms: 24 * 60 * 60e3, label: '全天' },
  { ms: Number.MAX_SAFE_INTEGER, label: '全部' },
]

export function CourtConsole({ demo = false }: { demo?: boolean }) {
  // 只有 /court-demo（离线预览）才预置演示数据；真实的 /court 页面从空状态开始，
  // API 失败时展示错误 + 空视图，绝不伪造内容（用户 2026-10-20：不要演示数据）。
  const [groups, setGroups] = useState<Group[]>(demo ? [demoGroup] : [])
  const [activeGroup, setActiveGroup] = useState<string>(demo ? DEMO_GROUP_ID : '')
  const [members, setMembers] = useState<GroupMember[]>(demo ? demoMembers : [])
  const [messages, setMessages] = useState<GroupMessage[]>(demo ? buildDemoMessages() : [])
  const [source, setSource] = useState<'api' | 'seed'>(demo ? 'seed' : 'api')
  // 连接状态与数据空态分离：只有请求失败才是 offline（见 ./connection.ts）。
  const [conn, setConn] = useState<CourtConn>(demo ? 'online' : 'loading')
  const [error, setError] = useState<string | null>(null)
  const [emptyReason, setEmptyReason] = useState<string | null>(null)
  // 空串 = 还没有解析出真实会话视角；此时**不要**订阅 SSE（旧实现用演示会话
  // 的 court-demo 去订阅，每次都 404）。
  const [streamChat, setStreamChat] = useState('')
  const [view, setView] = useState<'timeline' | 'sankey'>('timeline')
  const [windowMs, setWindowMs] = useState(24 * 60 * 60e3)
  const [selected, setSelected] = useState<GroupMessage | null>(null)
  const [laneFilter, setLaneFilter] = useState<string | null>(null)
  // 实际泳道高度（由画布自适应算出来，回传给组织树保持像素对齐）
  const [laneHeight, setLaneHeight] = useState(92)

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
    try {
      const list = await fetchGroups(signal)
      if (list.length === 0) {
        // 空 ≠ 断：服务端 200 且确实没有可见群组 —— 这在线、只是没数据。
        setGroups([])
        setActiveGroup('')
        setMembers([])
        setMessages([])
        setStreamChat('')
        setSource('api')
        setEmptyReason(emptyReasonFor(0, 0))
        setConn('online')
        return
      }
      const g = list[0]
      const ms = await fetchMembers(g.id, signal)
      const page = await fetchMessages(g.id, { limit: 200 }, signal)
      setGroups(list)
      setActiveGroup(g.id)
      setMembers(ms)
      setMessages(page.items)
      // 只有拿到真实会话视角才订阅 SSE；不再回退到演示会话（/court-demo 才是演示）。
      setStreamChat(page.chat_id ?? '')
      setSource('api')
      setEmptyReason(emptyReasonFor(list.length, page.items.length))
      setConn('online')
    } catch (e) {
      if (signal?.aborted) return
      // 只有请求/解析真的失败才判「离线」。
      setError(e instanceof Error ? e.message : String(e))
      setGroups([])
      setActiveGroup('')
      setMembers([])
      setMessages([])
      setStreamChat('')
      setSource('api')
      setConn('offline')
    }
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

  // group.message 实时追加（SSE；断线由浏览器自动重连，REST 数据仍可浏览）。
  useEffect(() => {
    if (source !== 'api' || streamChat === '') return
    return subscribeGroupMessages(streamChat, (m) => {
      setMessages((prev) => (prev.some((x) => x.id === m.id) ? prev : [...prev, m]))
    })
  }, [source, streamChat])

  const lanes: Lane[] = useMemo(() => deriveLanes(members, messages), [members, messages])
  // 没有泳道 = 没有可展示的通信数据：此时隐藏「待处理 / 视图切换」等工具条，
  // 只留「刷新」，避免空界面上堆一排无意义按钮（用户 2026-10-20「看起来全是 bug」）。
  const hasData = lanes.length > 0

  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const m of messages) {
      const from = senderAddr(m)
      c[from] = (c[from] ?? 0) + 1
      for (const to of m.to ?? []) c[to] = (c[to] ?? 0) + 1
    }
    return c
  }, [messages])

  const pending = useMemo(() => {
    const answered = new Set(messages.filter((m) => m.kind === 'answer' && m.correlation_id).map((m) => m.correlation_id))
    return messages.filter((m) => m.kind === 'ask_user' && m.correlation_id && !answered.has(m.correlation_id))
  }, [messages])

  const timeRange = useMemo(() => {
    if (messages.length === 0) return { min: fallbackNow - 3600e3, max: fallbackNow }
    const ts = messages.map((m) => new Date(m.created_at).getTime())
    return { min: Math.min(...ts), max: Math.max(...ts) }
  }, [messages, fallbackNow])

  // cursor 初始化到最新；数据变化时夹紧。
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
        // 1x ≈ 60 秒走完全程；10x/100x 线性加速。
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
    setView('timeline')
    const hit = messages.find((m) => senderAddr(m) === from && (m.to ?? []).includes(to))
    if (hit) setSelected(hit)
  }, [messages])

  const visibleMessages = laneFilter
    ? messages.filter((m) => senderAddr(m) === laneFilter || (m.to ?? []).includes(laneFilter))
    : messages

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden text-slate-200"
      style={{ background: 'radial-gradient(1200px 600px at 25% -10%, rgba(56,189,248,0.08), transparent), #070b14' }}>
      {/* 顶栏 */}
      <header className="flex shrink-0 items-center gap-3 border-b px-4 py-2.5"
        style={{ borderColor: 'rgba(148,163,184,0.14)', background: 'rgba(9,14,24,0.86)' }}>
        <div className="flex items-center gap-2">
          <span className="flex size-7 items-center justify-center rounded-lg text-[13px]"
            style={{ background: 'rgba(56,189,248,0.14)', color: '#38bdf8' }}>◈</span>
          <div>
            <div className="text-[13px] font-semibold tracking-wide">Agent 通信视图</div>
            <div className="text-[9.5px]" style={{ color: 'rgba(148,163,184,0.65)' }}>
              泳道时间轴 · 组织树 · 线程 · Sankey · 回放
            </div>
          </div>
        </div>

        <div className="ml-3 flex items-center gap-2">
          {/* 无群组时不渲染选择器：空 <select> 只剩一个箭头，是明显的 UI bug
              （2026-10-20 用户截图）。群组多于 1 个才有选择的意义。 */}
          {groups.length > 1 ? (
            <select
              value={activeGroup}
              onChange={(e) => {
                setActiveGroup(e.target.value)
                setSelected(null)
              }}
              className="rounded-md border px-2 py-1 text-[11px] outline-none"
              style={{ background: 'rgba(15,23,42,0.8)', borderColor: 'rgba(148,163,184,0.2)', color: '#e2e8f0' }}
              aria-label="选择群组"
            >
              {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </select>
          ) : groups.length === 1 ? (
            <span className="text-[11px]" style={{ color: 'rgba(226,232,240,0.85)' }}>{groups[0].name}</span>
          ) : null}
          {/* 连接状态只用一个点 + 文案；失败原因放 title（hover 可见），
              不再用整行横幅（用户 2026-10-20：别用横幅、别提演示数据）。 */}
          <span
            data-testid="court-conn-badge"
            data-conn={conn}
            title={statusTitle({ conn, error, emptyReason }, demo)}
            className="rounded-full px-2 py-0.5 text-[9px]"
            style={{
              background: conn === 'offline' ? 'rgba(239,68,68,0.12)' : 'rgba(34,197,94,0.12)',
              color: conn === 'offline' ? '#f87171' : '#22c55e',
            }}>
            {statusLabel({ conn }, demo)}
          </span>
        </div>

        <div className="ml-auto flex items-center gap-2">
          {hasData ? (
            <>
              <span className="rounded-md px-2 py-1 text-[10px]"
                style={{ background: 'rgba(239,68,68,0.10)', color: '#f87171' }}>
                待处理 {pending.length}
              </span>
              <div className="flex overflow-hidden rounded-md border" style={{ borderColor: 'rgba(148,163,184,0.18)' }}>
                {(['timeline', 'sankey'] as const).map((v) => (
                  <button key={v} type="button" onClick={() => setView(v)}
                    className="px-2.5 py-1 text-[10px]"
                    style={{
                      background: view === v ? 'rgba(56,189,248,0.16)' : 'transparent',
                      color: view === v ? '#38bdf8' : 'rgba(148,163,184,0.75)',
                    }}>
                    {v === 'timeline' ? '时间轴' : 'Sankey'}
                  </button>
                ))}
              </div>
              {view === 'sankey' ? (
                <select value={windowMs} onChange={(e) => setWindowMs(Number(e.target.value))}
                  className="rounded-md border px-2 py-1 text-[10px] outline-none"
                  style={{ background: 'rgba(15,23,42,0.8)', borderColor: 'rgba(148,163,184,0.2)', color: '#cbd5e1' }}
                  aria-label="流量时间窗口">
                  {WINDOW_OPTIONS.map((o) => <option key={o.label} value={o.ms}>{o.label}</option>)}
                </select>
              ) : null}
            </>
          ) : null}
          <button type="button" onClick={() => void load()}
            className="rounded-md px-2 py-1 text-[10px]"
            style={{ background: 'rgba(148,163,184,0.1)', color: 'rgba(203,213,225,0.85)' }}>
            刷新
          </button>
        </div>
      </header>

      {/* 在线但无数据时的说明：这是空态，不是故障（避免「空白一片」被误读成 bug）。 */}
      {conn === 'online' && emptyReason && !demo ? (
        <div data-testid="court-empty-reason"
          className="flex shrink-0 items-center gap-2 border-b px-4 py-1.5 text-[10px]"
          style={{ borderColor: 'rgba(148,163,184,0.1)', color: 'rgba(148,163,184,0.75)',
            background: 'rgba(56,189,248,0.04)' }}>
          <span className="size-1.5 rounded-full" style={{ background: '#38bdf8' }} />
          已连接服务端，但没有可展示的通信数据：{emptyReason}
        </div>
      ) : null}

      <div className="flex min-h-0 flex-1">
        <OrgTree lanes={lanes} laneFilter={laneFilter} onLaneFilter={setLaneFilter} counts={counts} laneHeight={laneHeight} />
        {view === 'timeline' ? (
          <TimelineCanvas
            lanes={lanes}
            messages={visibleMessages}
            selectedId={selected?.id ?? null}
            onSelect={onSelect}
            laneFilter={laneFilter}
            onLaneFilter={setLaneFilter}
            cutoff={cursor}
            onLaneHeightChange={setLaneHeight}
          />
        ) : (
          <SankeyView messages={visibleMessages} lanes={lanes} windowMs={windowMs} now={timeRange.max} onPick={onPickSankey} />
        )}
        <ThreadPanel messages={visibleMessages} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} />
      </div>

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

      {/* 图例 / 快捷键 */}
      <div className="flex shrink-0 items-center gap-3 border-t px-4 py-1 text-[9px]"
        style={{ borderColor: 'rgba(148,163,184,0.1)', color: 'rgba(148,163,184,0.55)' }}>
        {Object.entries(KIND_LABELS).map(([k, label]) => (
          <span key={k} className="flex items-center gap-1">
            <span className="inline-block h-[2px] w-3" style={{ background: KIND_COLORS[k] }} />
            {label}
          </span>
        ))}
        <span className="ml-auto font-mono">滚轮缩放 · 拖拽平移 · 双击全览 · 点击泳道过滤</span>
      </div>
    </div>
  )
}
