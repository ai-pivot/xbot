/**
 * ActivityFeed —— 通信视图的**默认视图**（2026-10-22 重新设计）。
 *
 * 设计依据（用户四条反馈）：
 *   ① 原来默认是 Sankey，没有跨泳道消息时主区是一整块空白 → 第一印象「页面坏了」；
 *   ② 术语裸奔（Sankey / correlation_id / 泳道）→ 普通人读不懂；
 *   ③ 信息密度要么过高（画布上的弧线）要么过低（三张占满整列的大卡片）；
 *   ④ 消息只有裸文本，没有类型、时间、发送者。
 *
 * 现在的形态是类飞书/Slack 的垂直时间线：每条 = 左侧类型色条 + 「部门 · 类型 ·
 * 相对时间」一行 + 一句摘要，hover 展开正文。普通人不需要任何背景知识就能读。
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import type { GroupMessage, Lane } from './types'
import { KIND_COLORS, KIND_LABELS, KIND_ORDER, messageSource, senderAddr, UNKNOWN_SOURCE } from './types'
import { EmptyState } from './EmptyState'

export interface ActivityFeedProps {
  messages: GroupMessage[]
  lanes: Lane[]
  selectedId: number | null
  onSelect: (m: GroupMessage | null) => void
  /** 泳道名解析（含未出现在组织树里的会话）。 */
  laneNameOf: (id: string) => string
  /** 回放游标：只显示 <= cutoff 的消息（拖动时间轴时**实时**过滤）。 */
  cutoff: number
  onRefresh: () => void
  /** 「发送一条测试消息」：写一条 event 进结构化消息存储。 */
  onSendTest?: () => void | Promise<void>
  /** 测试消息写入中（按钮置灰 + 文案变化）。 */
  sendingTest?: boolean
}

/** 相对时间：普通人读得懂的粒度，超过两天给日期。 */
export function relativeTime(iso: string, now = Date.now()): string {
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return ''
  const d = now - t
  if (d < 0) return '刚刚'
  if (d < 60e3) return '刚刚'
  if (d < 60 * 60e3) return `${Math.floor(d / 60e3)} 分钟前`
  if (d < 24 * 3600e3) return `${Math.floor(d / (60 * 60e3))} 小时前`
  if (d < 48 * 3600e3) return '昨天'
  const date = new Date(t)
  return `${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

/** 绝对时间（hover tooltip / 详情用）。 */
export function absoluteTime(iso: string): string {
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return ''
  return new Date(t).toLocaleString('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}

/**
 * 一句话摘要：活动流一行放不下 300 字，取最能说明「是什么事」的那一句。
 * 顺序：payload.summary → payload.question/topic → content 首行。
 */
export function messageSummary(m: GroupMessage): string {
  const p = (m.payload ?? {}) as Record<string, unknown>
  for (const key of ['summary', 'question', 'topic', 'title']) {
    const v = p[key]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  const first = (m.content ?? '').split('\n').map((l) => l.trim()).find(Boolean)
  // 回答的正文常常是机器拼的 `q0:yes`（用户原话：「只有 q1:yes 这种裸文本」）。
  // 答的是哪一项本身就是全部信息量，所以翻成「已回答：yes」。
  if (first && /^q\d+\s*:/.test(first)) {
    const choice = first.replace(/^q\d+\s*:\s*/, '')
    return `已回答：${choice || '（空）'}`
  }
  return first ?? '（无正文）'
}

/** 消息的展示归属：优先匹配组织树里的泳道名，其次来源角色，最后发送者名。 */
export function messageAuthor(m: GroupMessage, laneName: (id: string) => string): string {
  const addr = senderAddr(m)
  const lane = laneName(addr)
  if (lane && lane !== addr) return lane
  const src = messageSource(m)
  if (src?.role) return src.role
  // 一条事件都不能没有 header：拿不到任何归属时显式写「未知来源」。
  return (m.sender_name || lane || m.sender_id || '').replace('agent:', '').trim() || UNKNOWN_SOURCE
}

/** 空态里的 3 条示例（标「示例」，不写进真实数据源，刷新即消失）。 */
const SAMPLE_ROWS: { kind: string; author: string; text: string; when: string }[] = [
  { kind: 'report', author: '户部', when: '10 分钟前', text: '清理 12 个过期工作区，释放 41GB 磁盘。' },
  { kind: 'ask_user', author: '工部', when: '8 分钟前', text: '是否批准把 3 台空转的 spot 实例释放？' },
  { kind: 'decision', author: '首辅', when: '6 分钟前', text: '批准释放，先迁走上面的推理服务。' },
]

export function ActivityFeed({
  messages, lanes, selectedId, onSelect, laneNameOf, cutoff, onRefresh, onSendTest, sendingTest,
}: ActivityFeedProps) {
  const [hoverKey, setHoverKey] = useState<string | null>(null)
  const [showSample, setShowSample] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const seenRef = useRef<Set<number>>(new Set())
  const pinnedRef = useRef(true)

  // 相对时间每 30s 刷新一次（不重建列表，只是重算文案）。
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(id)
  }, [])

  // 泳道名：先查组织树，命中不了再用外部兜底（消息的发送者地址可能是
  // `agent:court.hubu`，而泳道 id 是 `agent:court.hubu/main`，需要兜底剥离）。
  const laneName = useMemo(() => {
    const map = new Map(lanes.map((l) => [l.id, l.name]))
    return (id: string) => map.get(id) ?? laneNameOf(id) ?? ''
  }, [lanes, laneNameOf])

  const visible = useMemo(() => {
    const list = messages.filter((m) => {
      const t = new Date(m.created_at).getTime()
      return Number.isFinite(t) ? t <= cutoff : true
    })
    return list.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  }, [messages, cutoff])

  // 新事件滑入：只给「这次渲染才出现」的行加动画类，避免每次重渲染都抖一下。
  const animated = useMemo(() => {
    const next = new Set<number>()
    for (const m of visible) if (!seenRef.current.has(m.id)) next.add(m.id)
    return next
  }, [visible])
  useEffect(() => {
    seenRef.current = new Set(visible.map((m) => m.id))
  }, [visible])

  // 时间线默认贴底（最新消息在视口内），用户往上翻时不抢滚动。
  //
  // 但**不能**直接贴到底：贴底后最上面那条会被切成半行，只剩摘要，
  // 「部门 · 类型 · 时间」那行被裁到视口外 —— 用户 2026-10-22 报的
  // 「活动流首条缺 header，下面 12 条都有」就是这么来的（不是 header 条件渲染）。
  // 所以贴底后再把滚动位置吸附到某条的顶边：顶部的行永远完整可见（header 一定看得见），
  // 溢出量不足一行时宁可裁掉最后一条的摘要尾巴，也不裁任何一条的 header。
  useEffect(() => {
    const el = scrollRef.current
    if (!el || !pinnedRef.current) return
    el.scrollTop = el.scrollHeight
    const box = el.getBoundingClientRect()
    const rows = el.querySelectorAll<HTMLElement>('[data-feed-item]')
    for (const row of rows) {
      const r = row.getBoundingClientRect()
      if (r.bottom <= box.top) continue // 整条已在视口上方（贴底恰好对齐时就是这种）
      if (r.top < box.top - 0.5) {
        // 只露出半截：把这一条的顶边吸到容器顶（2px = 列表容器的上内边距）
        el.scrollTop -= box.top - r.top - 2
      }
      break
    }
  }, [visible.length])

  const hoverCorrelation = useMemo(() => {
    if (!hoverKey) return null
    const hit = visible.find((m) => m.correlation_id === hoverKey)
    return hit?.correlation_id ?? null
  }, [hoverKey, visible])

  if (visible.length === 0) {
    return (
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-bg-primary">
        <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2 text-[11px] text-text-secondary">
          <span className="font-medium text-text-primary">活动流</span>
          <span className="text-text-muted">按时间顺序列出部门之间的事件</span>
        </div>
        {messages.length === 0 ? (
          <EmptyState
            testId="feed-empty"
            glyph="◇"
            title="还没有结构化消息"
            description={(
              <>
                部门汇报、请旨（提问）、批红会出现在这里，一条事件一行。
                <br />
                现在既没有子代理会话，也没有历史事件。
              </>
            )}
          >
            <button
              type="button"
              data-testid="feed-send-test"
              disabled={sendingTest}
              onClick={() => void onSendTest?.()}
              className="rounded-md bg-app-accent px-3 py-1.5 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover disabled:opacity-50"
            >
              {sendingTest ? '发送中…' : '发送一条测试消息'}
            </button>
            <button
              type="button"
              data-testid="feed-show-sample"
              onClick={() => setShowSample(true)}
              className="rounded-md border border-border bg-bg-secondary px-3 py-1.5 text-[11px] text-text-secondary hover:bg-bg-tertiary hover:text-text-primary"
            >
              看示例
            </button>
            <button
              type="button"
              onClick={onRefresh}
              className="rounded-md px-2 py-1.5 text-[11px] text-text-muted hover:text-text-primary"
            >
              刷新
            </button>
          </EmptyState>
        ) : (
          <EmptyState
            testId="feed-empty-window"
            glyph="◷"
            title="这个时间窗口里没有事件"
            description="把底部时间轴往右拖（或点「回到最新」）就能看到更新的消息。"
          >
            <button
              type="button"
              onClick={onRefresh}
              className="rounded-md border border-border bg-bg-secondary px-3 py-1.5 text-[11px] text-text-secondary hover:bg-bg-tertiary hover:text-text-primary"
            >
              刷新
            </button>
          </EmptyState>
        )}
        {showSample ? <SampleRows /> : null}
      </div>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-bg-primary">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2 text-[11px]">
        <span className="font-medium text-text-primary">活动流</span>
        <span className="text-text-muted">按时间顺序 · 点击一条看详情 · 悬停展开全文</span>
        <span className="ml-auto rounded-full bg-bg-tertiary px-2 py-0.5 font-mono text-[10px] text-text-secondary">
          {visible.length} 条
        </span>
      </div>
      <div
        ref={scrollRef}
        data-testid="feed-list"
        onScroll={(e) => {
          const el = e.currentTarget
          pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48
        }}
        className="min-h-0 flex-1 overflow-y-auto px-3 py-2"
      >
        {visible.map((m) => {
          const color = KIND_COLORS[m.kind] ?? KIND_COLORS.text
          const active = selectedId === m.id
          const inThread = hoverCorrelation != null && m.correlation_id === hoverCorrelation
          const author = messageAuthor(m, laneName)
          const summary = messageSummary(m)
          const pendingAsk = m.kind === 'ask_user' && m.answered !== true
          return (
            <button
              key={m.id}
              type="button"
              data-feed-item={m.id}
              data-kind={m.kind}
              data-testid="feed-item"
              onClick={() => onSelect(active ? null : m)}
              onMouseEnter={() => setHoverKey(m.correlation_id ?? null)}
              onMouseLeave={() => setHoverKey(null)}
              title={`${author} · ${absoluteTime(m.created_at)}${m.correlation_id ? ` · 同一次对话 ${m.correlation_id}` : ''}`}
              className={[
                'group relative mb-1 flex w-full items-start gap-2 overflow-hidden rounded-lg border px-2.5 py-1.5 pl-3 text-left transition-colors',
                active ? 'border-app-accent bg-app-accent/10' : 'border-border-subtle bg-bg-primary hover:bg-bg-secondary',
                inThread && !active ? 'ring-1 ring-app-accent/40' : '',
                animated.has(m.id) ? 'feed-item-in' : '',
              ].join(' ')}
            >
              <span aria-hidden className="absolute inset-y-0 left-0 w-[3px]" style={{ background: color }} />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-1.5 text-[10px] leading-4 text-text-muted">
                  <span className="font-medium text-text-secondary">{author}</span>
                  <span>·</span>
                  <span className="font-semibold" style={{ color }}>{KIND_LABELS[m.kind] ?? m.kind}</span>
                  <span>·</span>
                  <span title={absoluteTime(m.created_at)}>{relativeTime(m.created_at, now)}</span>
                  {pendingAsk ? (
                    <span className="rounded bg-lane-waiting/12 px-1 py-[1px] text-lane-waiting">待回答</span>
                  ) : null}
                </span>
                <span className="mt-0.5 block truncate text-[12px] leading-5 text-text-primary group-hover:whitespace-pre-wrap group-hover:break-words">
                  {summary}
                </span>
              </span>
              <span className="mt-0.5 shrink-0 text-[10px] text-text-muted group-hover:hidden">›</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}

/** 空态里的「看示例」：明确标注示例，绝不冒充真实数据。 */
function SampleRows() {
  return (
    <div data-testid="feed-sample" className="mx-6 mb-6 rounded-lg border border-dashed border-border p-3">
      <div className="mb-2 flex items-center gap-2 text-[10px] text-text-muted">
        <span className="rounded bg-bg-tertiary px-1.5 py-0.5">示例</span>
        下面三条是假的示例数据，用来展示活动流长什么样；刷新后不会保留。
      </div>
      <ul className="space-y-1.5">
        {SAMPLE_ROWS.map((row) => (
          <li
            key={row.text}
            className="relative overflow-hidden rounded-lg border border-border bg-bg-secondary/60 px-2.5 py-1.5 pl-3 opacity-70"
          >
            <span
              aria-hidden
              className="absolute inset-y-0 left-0 w-[3px]"
              style={{ background: KIND_COLORS[row.kind] ?? KIND_COLORS.text }}
            />
            <div className="text-[10px] text-text-muted">
              {row.author}{' · '}
              <span className="font-semibold" style={{ color: KIND_COLORS[row.kind] }}>
                {KIND_LABELS[row.kind] ?? row.kind}
              </span>
              {' · '}{row.when}
            </div>
            <div className="text-[12px] leading-5 text-text-secondary">{row.text}</div>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** 图例（活动流底部）：类型色条，顺序固定。 */
export function KindLegend() {
  return (
    <>
      {KIND_ORDER.map((k) => (
        <span key={k} className="flex items-center gap-1">
          <span className="inline-block size-2 rounded-sm" style={{ background: KIND_COLORS[k] }} />
          {KIND_LABELS[k]}
        </span>
      ))}
    </>
  )
}
