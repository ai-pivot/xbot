/**
 * 线程视图 + 消息详情。
 *
 * 2026-10-22 重新设计：
 *   - 术语中文化：不再把 `correlation_id` 直接印给用户，改称「同一次对话」（技术值
 *     收进详情里的技术标识符行，hover 可看）；
 *   - 消息详情不再直接怼一段裸 JSON：先给可读的键值对，原始数据折叠在
 *     「原始数据（技术详情）」里，默认收起；
 *   - 线程列表抽成 `ThreadList`，可以单独作为主视图（顶部 Segmented Control 的
 *     「线程」页签），右侧面板复用同一份实现。
 */
import { useMemo, useState } from 'react'
import type { GroupMessage } from './types'
import { KIND_COLORS, KIND_LABELS, kindTint, messageSource, senderAddr, UNKNOWN_SOURCE } from './types'
import { EmptyState } from './EmptyState'
import { absoluteTime, messageSummary } from './ActivityFeed'

export interface Thread {
  key: string
  list: GroupMessage[]
  open: boolean
  title: string
}

export function threadKey(m: GroupMessage): string {
  return m.correlation_id || `single:${m.id}`
}

/**
 * 按「同一次对话」把消息折叠成线程；未带对话编号的消息聚合成一条「未定向消息」。
 * 未闭环（最后一问还没答）的线程置顶。
 */
export function threadGroups(messages: GroupMessage[]): Thread[] {
  const map = new Map<string, GroupMessage[]>()
  for (const m of messages) {
    const k = threadKey(m)
    const arr = map.get(k) ?? []
    arr.push(m)
    map.set(k, arr)
  }
  const out = [...map.entries()]
    .filter(([key]) => !key.startsWith('single:'))
    .map(([key, list]) => {
      list.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
      const last = list[list.length - 1]
      const open = last.kind === 'ask_user' &&
        !list.some((m) => m.kind === 'answer' && m.correlation_id === last.correlation_id)
      return {
        key,
        list,
        open,
        title: String(list[0]?.payload?.topic ?? messageSummary(list[0])),
      }
    })
  const singles = [...map.entries()]
    .filter(([key]) => key.startsWith('single:'))
    .flatMap(([, list]) => list)
    .sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
  if (singles.length > 0) out.push({ key: 'singles', list: singles, open: false, title: '未定向消息' })
  out.sort((a, b) => Number(b.open) - Number(a.open))
  return out
}

/** 线程跨多个会话时的展示名（不能挑一个多数派冒充）。 */
export const MULTI_LANE = '多部门'

/**
 * 线程归属的会话（泳道）id：真实后端里同一次对话的消息都落在**同一个**会话，
 * 所以「多数归属」就是这条线程属于谁；示例数据也按同一规则填了 laneID。
 */
export function threadLane(t: Thread): string {
  const counts = new Map<string, number>()
  for (const m of t.list) {
    const id = m.laneID || senderAddr(m)
    if (id) counts.set(id, (counts.get(id) ?? 0) + 1)
  }
  let best = ''
  let bestN = 0
  for (const [id, n] of counts) if (n > bestN) { best = id; bestN = n }
  return best
}

/**
 * 线程的部门名：与活动流**同一条规则**（组织树里的部门名 → 来源角色 → 发送者名）。
 *
 * 2026-10-22 用户报障：右栏 14 条线程全显示「未知来源」，而左栏同一批消息都能显示
 * 部门名 —— 因为右栏只读了 `sourceChannel/sourceRole`（技术来源），示例数据没有这些
 * 字段就直接落到「未知来源」。线程归属是**会话**，所以先按 laneID 查组织树名字。
 */
export function threadAuthor(t: Thread, laneNameOf?: (id: string) => string): string {
  // 一次对话正常只在**一个**会话里（真实后端就是这么存的）；跨了多个会话就
  // 老实写「多部门」，不能挑一个多数派冒充，否则读者会以为整条线程都是一个部门的事。
  const lanes = new Set(t.list.map((m) => m.laneID || senderAddr(m)).filter(Boolean))
  if (lanes.size > 1) return MULTI_LANE
  const lane = threadLane(t)
  const name = lane ? (laneNameOf?.(lane) ?? '') : ''
  if (name && name !== lane) return name
  const first = t.list[0]
  const src = first ? messageSource(first) : null
  if (src?.role) return src.role
  const fallback = (first?.sender_name || first?.sender_id || '').replace('agent:', '').trim()
  return fallback || UNKNOWN_SOURCE
}

/**
 * 来源 chip：渠道 · 会话短 id · 角色名（子代理才有）。
 * 没有来源信息时显示「未知来源」——不能静默，否则读者无法判断这条事件属于谁。
 */
function SourceChip({ message, testId }: { message: GroupMessage; testId: string }) {
  const src = messageSource(message)
  const label = src ? [src.channel, src.short, src.role].filter(Boolean).join('·') : UNKNOWN_SOURCE
  return (
    <span
      data-testid={testId}
      data-source={src?.key ?? 'unknown'}
      title={src ? `${src.key}${src.role ? ` · ${src.role}` : ''}` : '这条事件没有携带会话来源信息'}
      className={
        src
          ? 'max-w-[104px] shrink-0 truncate rounded bg-bg-tertiary px-1 py-0.5 font-mono text-[8.5px] text-text-secondary'
          : 'max-w-[104px] shrink-0 truncate rounded bg-lane-waiting/15 px-1 py-0.5 font-mono text-[8.5px] text-lane-waiting'
      }
    >
      {label}
    </span>
  )
}

/** payload 的友好展示：常见键映射成中文标签，其余按 key: value 逐行。 */
const PAYLOAD_LABELS: Record<string, string> = {
  summary: '摘要',
  question: '问题',
  questions: '问题列表',
  topic: '主题',
  title: '标题',
  status: '状态',
  target: '接收方',
  to: '接收方',
  department: '部门',
  progress: '进度',
  reason: '原因',
  verdict: '结论',
  options: '可选项',
  answers: '回答',
}

function scalar(v: unknown): string {
  if (v == null) return '—'
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  if (Array.isArray(v)) {
    if (v.every((x) => typeof x === 'string' || typeof x === 'number')) return v.join('、')
    return `${v.length} 项`
  }
  return '…'
}

/** 消息详情：可读信息在前，原始 JSON 折叠在后。 */
export function MessageDetail({ selected, onClose }: { selected: GroupMessage; onClose: () => void }) {
  const payloadEntries = Object.entries(selected.payload ?? {})
  return (
    <div data-testid="message-detail" className="shrink-0 border-b border-border px-3 py-2.5">
      <div className="flex items-center gap-2">
        <span
          className="rounded px-1.5 py-0.5 text-[10px] font-semibold"
          style={{ background: kindTint(selected.kind), color: KIND_COLORS[selected.kind] ?? KIND_COLORS.text }}
        >
          {KIND_LABELS[selected.kind] ?? selected.kind}
        </span>
        <span className="min-w-0 truncate text-[12px] font-semibold text-text-primary">{selected.sender_name}</span>
        <SourceChip message={selected} testId="selected-source" />
        <button
          type="button"
          onClick={onClose}
          className="ml-auto shrink-0 rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] text-text-secondary hover:text-text-primary"
        >
          关闭
        </button>
      </div>
      <div className="mt-1.5 text-[10px] text-text-muted" title={absoluteTime(selected.created_at)}>
        {absoluteTime(selected.created_at)}
        {selected.to && selected.to.length > 0 ? ` · 发给 ${selected.to.map(senderAddrOf).join('、')}` : ''}
      </div>
      <div className="mt-2 whitespace-pre-wrap text-[11px] leading-5 text-text-primary">{selected.content}</div>

      {payloadEntries.length > 0 ? (
        <dl className="mt-2 space-y-0.5 rounded-md bg-bg-tertiary/60 px-2 py-1.5">
          {payloadEntries.map(([k, v]) => (
            <div key={k} className="flex gap-2 text-[10px] leading-4">
              <dt className="shrink-0 text-text-muted">{PAYLOAD_LABELS[k] ?? k}</dt>
              <dd className="min-w-0 flex-1 truncate text-text-secondary" title={scalar(v)}>{scalar(v)}</dd>
            </div>
          ))}
        </dl>
      ) : null}

      {selected.payload ? (
        <details data-testid="payload-raw" className="mt-2">
          <summary className="cursor-pointer select-none text-[10px] text-text-muted hover:text-text-secondary">
            原始数据（技术详情）
          </summary>
          <pre className="mt-1 max-h-40 overflow-auto rounded-md bg-bg-tertiary p-2 font-mono text-[9.5px] leading-4 text-text-secondary">
            {JSON.stringify(selected.payload, null, 2)}
          </pre>
        </details>
      ) : null}
      <div className="mt-1 text-[9px] text-text-muted">
        对话编号 <span className="font-mono">{selected.correlation_id || '—'}</span>
        {selected.correlation_id ? '（同一个编号的消息算一次对话）' : '（这条消息不属于任何一次对话）'}
      </div>
    </div>
  )
}

function senderAddrOf(id: string): string {
  return id.replace('agent:', '')
}

interface ThreadListProps {
  messages: GroupMessage[]
  selected: GroupMessage | null
  onSelect: (m: GroupMessage) => void
  /** 主视图（宽版）时展开全部线程，右侧栏只展开选中的。 */
  expandAll?: boolean
  onSwitchToFeed?: () => void
  /** 泳道 id → 部门名（组织树的同一份 map）。缺省时会退化成来源角色/发送者名。 */
  laneNameOf?: (id: string) => string
}

export function ThreadList({
  messages, selected, onSelect, expandAll = false, onSwitchToFeed, laneNameOf,
}: ThreadListProps) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const threads = useMemo(() => threadGroups(messages), [messages])
  const selectedKey = selected ? threadKey(selected) : null

  if (threads.length === 0) {
    return (
      <EmptyState
        testId="thread-empty"
        compact
        glyph="⌘"
        title="还没有形成对话线程"
        description={(
          <>
            同一个对话编号下的提问、回答、决策会聚成一条线程。
            <br />
            部门发起请旨（并收到批红）之后，线程就会出现在这里。
          </>
        )}
      >
        {onSwitchToFeed ? (
          <button
            type="button"
            onClick={onSwitchToFeed}
            className="rounded-md bg-app-accent px-3 py-1.5 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover"
          >
            切到活动流
          </button>
        ) : null}
      </EmptyState>
    )
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
      {threads.map((t) => {
        const autoCollapsed = expandAll ? false : selectedKey !== t.key
        const isCollapsed = collapsed[t.key] ?? autoCollapsed
        return (
          <div
            key={t.key}
            data-thread={t.key}
            className={t.open
              ? 'mb-1.5 overflow-hidden rounded-lg border border-lane-waiting/40'
              : 'mb-1.5 overflow-hidden rounded-lg border border-border'}
          >
            <button
              type="button"
              onClick={() => setCollapsed((c) => ({ ...c, [t.key]: !isCollapsed }))}
              className={t.open
                ? 'flex w-full items-center gap-2 bg-lane-waiting/8 px-2.5 py-1.5 text-left'
                : 'flex w-full items-center gap-2 bg-bg-tertiary/60 px-2.5 py-1.5 text-left hover:bg-bg-tertiary'}
            >
              <span className={t.open ? 'size-1.5 shrink-0 rounded-full bg-lane-waiting' : 'size-1.5 shrink-0 rounded-full bg-lane-done'} />
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5">
                  <span
                    data-testid="thread-author"
                    data-lane={threadLane(t)}
                    className="max-w-[92px] shrink-0 truncate rounded bg-bg-tertiary px-1 py-0.5 text-[9px] font-medium text-text-secondary"
                  >
                    {threadAuthor(t, laneNameOf)}
                  </span>
                  <span className="min-w-0 truncate text-[11px] font-medium text-text-primary">{t.title}</span>
                </span>
              </span>
              <span className="shrink-0 text-[9px] text-text-muted">{t.open ? '待回答' : '已闭环'}</span>
              <span className="shrink-0 font-mono text-[9px] text-text-muted">{t.list.length} 条</span>
            </button>
            {!isCollapsed ? (
              <div className="space-y-0.5 px-2 py-1.5">
                {t.list.map((m) => {
                  const color = KIND_COLORS[m.kind] ?? KIND_COLORS.text
                  const active = selected?.id === m.id
                  return (
                    <button
                      key={m.id}
                      type="button"
                      onClick={() => onSelect(m)}
                      data-msg-id={m.id}
                      className={active
                        ? 'flex w-full items-start gap-2 rounded bg-app-accent/10 px-1.5 py-1 text-left'
                        : 'flex w-full items-start gap-2 rounded px-1.5 py-1 text-left hover:bg-bg-tertiary'}
                    >
                      <span className="mt-1 size-1.5 shrink-0 rounded-full" style={{ background: color }} />
                      <span className="min-w-0 flex-1">
                        <span className="flex min-w-0 items-center gap-1.5">
                          <span className="min-w-0 truncate text-[10px] font-semibold" style={{ color }}>
                            {KIND_LABELS[m.kind] ?? m.kind} · {(m.laneID ? laneNameOf?.(m.laneID) : '') || m.sender_name}
                          </span>
                          <SourceChip message={m} testId="msg-source" />
                          <span className="shrink-0 font-mono text-[8.5px] text-text-muted">
                            {new Date(m.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
                          </span>
                        </span>
                        <span className="mt-0.5 block truncate text-[10px] text-text-secondary">{messageSummary(m)}</span>
                      </span>
                    </button>
                  )
                })}
              </div>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

/** 主视图的「线程」页：宽版线程列表（左侧一条竖线把对话串起来）。 */
export function ThreadsView({ messages, selected, onSelect, onSwitchToFeed, laneNameOf }: Omit<ThreadListProps, 'expandAll'>) {
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-bg-primary">
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-4 py-2 text-[11px]">
        <span className="font-medium text-text-primary">对话线程</span>
        <span className="text-text-muted">同一个对话编号下的消息按时间排在一起</span>
        <span className="ml-auto font-mono text-[10px] text-text-muted">
          {threadGroups(messages).length} 条线程
        </span>
      </div>
      <ThreadList
        messages={messages}
        selected={selected}
        onSelect={onSelect}
        expandAll
        onSwitchToFeed={onSwitchToFeed}
        laneNameOf={laneNameOf}
      />
    </div>
  )
}

/** 右侧栏：选中消息详情 + 线程折叠列表。 */
export function ThreadPanel({
  messages, selected, onSelect, onClose, showList = true, laneNameOf,
}: {
  messages: GroupMessage[]
  selected: GroupMessage | null
  onSelect: (m: GroupMessage) => void
  onClose: () => void
  /** 主区已经在显示完整线程列表时传 false —— 两侧同时列一遍线程是冗余。 */
  showList?: boolean
  /** 泳道 id → 部门名（与活动流共用一份）。 */
  laneNameOf?: (id: string) => string
}) {
  return (
    <aside className="hidden w-[330px] shrink-0 flex-col border-l border-border bg-bg-secondary lg:flex">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-text-secondary">
          {selected ? '消息详情' : '对话线程'}
        </span>
        {selected ? (
          <button
            type="button"
            onClick={onClose}
            className="ml-auto rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] text-text-secondary hover:text-text-primary"
          >
            关闭
          </button>
        ) : null}
      </div>
      {selected ? <MessageDetail selected={selected} onClose={onClose} /> : null}
      {showList
        ? <ThreadList messages={messages} selected={selected} onSelect={onSelect} laneNameOf={laneNameOf} />
        : null}
      {!showList && !selected ? (
        <p className="px-3 py-4 text-[10px] leading-5 text-text-muted">
          左侧是完整的对话线程列表；点其中一条消息，详情会显示在这里。
        </p>
      ) : null}
    </aside>
  )
}
