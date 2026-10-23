/**
 * 消息流向（原 Sankey）：谁把消息发给了谁，带宽 = 条数。
 *
 * 2026-10-22 重新设计：
 *   - 术语中文化：标题不再叫 «Sankey»，副标题不再出现 `correlation_id`；
 *   - 空态不再是「1000×600 空白画布 + 一行小字」，而是一句解释 + 一个可点按钮；
 *   - 图例只列可区分的前四类，不再把「已离开」和「空闲」画成同一个灰。
 *
 * 普通人不该被迫理解流向图 —— 所以它从默认视图降级为可切换的分析视图，
 * 默认停在「活动流」。
 */
import { useMemo, useState } from 'react'
import type { GroupMessage } from './types'
import { KIND_COLORS, KIND_LABELS, senderAddr } from './types'
import { EmptyState } from './EmptyState'

interface Props {
  messages: GroupMessage[]
  lanes: { id: string; name: string }[]
  windowMs: number
  now: number
  onPick: (from: string, to: string) => void
  /** 空态里的出口：切回活动流。 */
  onSwitchToFeed: () => void
}

interface Link {
  from: string
  to: string
  count: number
  kinds: Record<string, number>
}

const W = 920
const H = 360
const PAD = 46
const NODE_W = 8

/** `agent:court.hubu/main` → `court.hubu`（只影响显示）。 */
function shorten(id: string): string {
  return id.replace('agent:', '').replace('/main', '')
}

export function SankeyView({ messages, lanes, windowMs, now, onPick, onSwitchToFeed }: Props) {
  const [helpOpen, setHelpOpen] = useState(false)

  const { links, senders, receivers, maxCount } = useMemo(() => {
    const since = now - windowMs
    const counts = new Map<string, Link>()
    for (const m of messages) {
      const t = new Date(m.created_at).getTime()
      if (t < since || t > now) continue
      const from = senderAddr(m)
      const targets = (m.to && m.to.length > 0) ? m.to : []
      for (const to of targets) {
        if (from === to) continue
        const key = `${from}→${to}`
        const l = counts.get(key) ?? { from, to, count: 0, kinds: {} }
        l.count += 1
        l.kinds[m.kind] = (l.kinds[m.kind] ?? 0) + 1
        counts.set(key, l)
      }
    }
    const all = [...counts.values()]
    const sendersSet = new Map<string, number>()
    const receiversSet = new Map<string, number>()
    for (const l of all) {
      sendersSet.set(l.from, (sendersSet.get(l.from) ?? 0) + l.count)
      receiversSet.set(l.to, (receiversSet.get(l.to) ?? 0) + l.count)
    }
    const senders = [...sendersSet.entries()].sort((a, b) => b[1] - a[1])
    const receivers = [...receiversSet.entries()].sort((a, b) => b[1] - a[1])
    const maxCount = Math.max(1, ...all.map((l) => l.count))
    return { links: all.sort((a, b) => b.count - a.count), senders, receivers, maxCount }
  }, [messages, windowMs, now])

  const senderY = new Map<string, number>()
  const receiverY = new Map<string, number>()
  const usable = H - PAD * 2
  senders.forEach(([id], i) => senderY.set(id, PAD + (usable / Math.max(1, senders.length)) * (i + 0.5)))
  receivers.forEach(([id], i) => receiverY.set(id, PAD + (usable / Math.max(1, receivers.length)) * (i + 0.5)))

  const nameOf = (id: string) => lanes.find((l) => l.id === id)?.name ?? shorten(id)

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-bg-primary">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-4 py-2 text-[11px]">
        <span className="font-medium text-text-primary">消息流向</span>
        <span className="text-text-muted">左边是发送方，右边是接收方，线越粗消息越多</span>
        <button
          type="button"
          data-testid="sankey-help"
          aria-expanded={helpOpen}
          onClick={() => setHelpOpen((v) => !v)}
          className="rounded border border-border px-1.5 py-0.5 text-[10px] text-text-secondary hover:bg-bg-tertiary hover:text-text-primary"
        >
          这是什么？
        </button>
        <span className="ml-auto font-mono text-[10px] text-text-muted">
          窗口 {windowLabel(windowMs)} · {links.reduce((n, l) => n + l.count, 0)} 条
        </span>
      </div>
      {helpOpen ? (
        <div
          data-testid="sankey-help-popover"
          className="shrink-0 border-b border-border bg-bg-secondary px-4 py-2 text-[11px] leading-5 text-text-secondary"
        >
          每条线代表「A 发消息给 B」；线宽 = 这段时间内的消息条数。
          结构化消息多数落在发起人自己的会话里，只有明确写了接收方（比如请旨给首辅）
          才会出现跨部门连线，所以这里通常比活动流稀疏 —— 想看全部事件请切回「活动流」。
        </div>
      ) : null}
      {links.length === 0 ? (
        <EmptyState
          testId="sankey-empty"
          glyph="⇄"
          title="这段时间没有跨部门消息"
          description={(
            <>
              部门汇报和请旨大多留在自己的会话里，所以这里没有连线。
              <br />
              全部事件都在「活动流」里，一条不少。
            </>
          )}
        >
          <button
            type="button"
            data-testid="sankey-to-feed"
            onClick={onSwitchToFeed}
            className="rounded-md bg-app-accent px-3 py-1.5 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover"
          >
            切到活动流
          </button>
        </EmptyState>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-2">
          <svg viewBox={`0 0 ${W} ${H}`} className="h-full w-full" role="img" aria-label="消息流向图">
            <rect width={W} height={H} rx="10" style={{ fill: 'var(--bg-secondary)', stroke: 'var(--border)' }} />
            {links.map((l) => {
              const y0 = senderY.get(l.from) ?? H / 2
              const y1 = receiverY.get(l.to) ?? H / 2
              const stroke = Math.max(1.2, (l.count / maxCount) * 14)
              const color = Object.entries(l.kinds).sort((a, b) => b[1] - a[1])[0]?.[0]
              const c = KIND_COLORS[color] ?? KIND_COLORS.text
              const x0 = 150
              const x1 = W - 150
              return (
                <g key={`${l.from}-${l.to}`} className="cursor-pointer" onClick={() => onPick(l.from, l.to)}>
                  <path
                    d={`M ${x0} ${y0} C ${x0 + 170} ${y0}, ${x1 - 170} ${y1}, ${x1} ${y1}`}
                    fill="none" stroke={c} strokeOpacity={0.42} strokeWidth={stroke}
                    strokeLinecap="round"
                  >
                    <title>{nameOf(l.from)} → {nameOf(l.to)}：{l.count} 条</title>
                  </path>
                </g>
              )
            })}
            {senders.map(([id, n]) => (
              <g key={`s-${id}`}>
                <text x={140} y={(senderY.get(id) ?? 0) + 4} textAnchor="end"
                  fontSize="11" fontWeight={600} style={{ fill: 'var(--text-primary)' }}>{nameOf(id)}</text>
                <rect x={148} y={(senderY.get(id) ?? 0) - 9} width={NODE_W} height={18} rx={3}
                  style={{ fill: 'var(--accent)' }} opacity={0.85} />
                <text x={162} y={(senderY.get(id) ?? 0) + 4} fontSize="9" style={{ fill: 'var(--text-secondary)' }}>{n}</text>
              </g>
            ))}
            {receivers.map(([id, n]) => (
              <g key={`r-${id}`}>
                <rect x={W - 156} y={(receiverY.get(id) ?? 0) - 9} width={NODE_W} height={18} rx={3}
                  style={{ fill: 'var(--kind-decision)' }} opacity={0.85} />
                <text x={W - 142} y={(receiverY.get(id) ?? 0) + 4} fontSize="11" fontWeight={600}
                  style={{ fill: 'var(--text-primary)' }}>{nameOf(id)}</text>
                <text x={W - 142} y={(receiverY.get(id) ?? 0) + 16} fontSize="9" style={{ fill: 'var(--text-secondary)' }}>{n}</text>
              </g>
            ))}
          </svg>
        </div>
      )}
      <div className="flex shrink-0 flex-wrap items-center gap-3 border-t border-border px-4 py-1.5 text-[9px] text-text-muted">
        {(['report', 'ask_user', 'decision', 'command', 'event'] as const).map((k) => (
          <span key={k} className="flex items-center gap-1">
            <span className="inline-block h-[2px] w-4" style={{ background: KIND_COLORS[k] }} />
            {KIND_LABELS[k]}
          </span>
        ))}
        <span className="ml-auto">线宽 = 条数 · 点击连线可在活动流里过滤</span>
      </div>
    </div>
  )
}

function windowLabel(ms: number): string {
  if (ms <= 15 * 60e3) return '15 分钟'
  if (ms <= 3600e3) return '1 小时'
  if (ms <= 6 * 3600e3) return '6 小时'
  if (ms <= 24 * 3600e3) return '全天'
  return '全部'
}
