/**
 * Sankey 宏观流量：参与者之间的消息带宽。
 *
 * 布局：左列 = 发送者，右列 = 接收者；带宽 = 时间窗口内消息条数。
 * 链路用 SVG 三次贝塞尔带绘制；hover 高亮，click 回传过滤条件。
 */
import { useMemo } from 'react'
import type { GroupMessage } from './types'
import { KIND_COLORS, senderAddr } from './types'

interface Props {
  messages: GroupMessage[]
  lanes: { id: string; name: string }[]
  windowMs: number
  now: number
  onPick: (from: string, to: string) => void
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

function shorten(id: string): string {
  return id.replace('agent:', '').replace('/main', '')
}

export function SankeyView({ messages, lanes, windowMs, now, onPick }: Props) {
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
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden" style={{ background: 'rgba(9,14,24,0.35)' }}>
      <div className="flex items-center gap-3 border-b px-4 py-2 text-[10px]"
        style={{ borderColor: 'rgba(148,163,184,0.12)', color: 'rgba(148,163,184,0.7)' }}>
        <span className="font-semibold uppercase tracking-[0.16em]">Sankey 宏观流量</span>
        <span>左=发送 · 右=接收 · 带宽=条数</span>
        <span className="ml-auto">窗口 {windowLabel(windowMs)} · {links.reduce((n, l) => n + l.count, 0)} 条</span>
      </div>
      {links.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-[12px]" style={{ color: 'rgba(148,163,184,0.6)' }}>
          当前时间窗口没有跨泳道消息
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-2">
          <svg viewBox={`0 0 ${W} ${H}`} className="h-full w-full" role="img" aria-label="消息流量 Sankey">
            <defs>
              <linearGradient id="sankey-bg" x1="0" y1="0" x2="1" y2="0">
                <stop offset="0" stopColor="#0b1220" />
                <stop offset="1" stopColor="#0a0f1a" />
              </linearGradient>
            </defs>
            <rect width={W} height={H} fill="url(#sankey-bg)" rx="10" />
            {links.map((l) => {
              const y0 = senderY.get(l.from) ?? H / 2
              const y1 = receiverY.get(l.to) ?? H / 2
              const stroke = Math.max(1.2, (l.count / maxCount) * 14)
              const color = Object.entries(l.kinds).sort((a, b) => b[1] - a[1])[0]?.[0]
              const c = KIND_COLORS[color] ?? '#94a3b8'
              const x0 = 150
              const x1 = W - 150
              return (
                <g key={`${l.from}-${l.to}`} className="cursor-pointer"
                  onClick={() => onPick(l.from, l.to)}>
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
                  fontSize="11" fill="#cbd5e1" fontWeight={600}>{nameOf(id)}</text>
                <rect x={148} y={(senderY.get(id) ?? 0) - 9} width={NODE_W} height={18} rx={3}
                  fill="#38bdf8" opacity={0.85} />
                <text x={162} y={(senderY.get(id) ?? 0) + 4} fontSize="9" fill="rgba(148,163,184,0.7)">{n}</text>
              </g>
            ))}
            {receivers.map(([id, n]) => (
              <g key={`r-${id}`}>
                <rect x={W - 156} y={(receiverY.get(id) ?? 0) - 9} width={NODE_W} height={18} rx={3}
                  fill="#a78bfa" opacity={0.85} />
                <text x={W - 142} y={(receiverY.get(id) ?? 0) + 4} fontSize="11" fill="#cbd5e1" fontWeight={600}>
                  {nameOf(id)}
                </text>
                <text x={W - 142} y={(receiverY.get(id) ?? 0) + 16} fontSize="9" fill="rgba(148,163,184,0.7)">{n}</text>
              </g>
            ))}
          </svg>
        </div>
      )}
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
