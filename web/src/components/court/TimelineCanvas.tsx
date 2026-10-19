/**
 * TimelineCanvas —— 指挥室主视图（v2 密度自适应版）。
 *
 * 三项核心改造：
 *  1. 非线性时间轴：把消息按 45 分钟间隔切成「事件簇」，簇内按消息数分配宽度，
 *     簇与簇之间的空白折叠成固定宽度的 `·· 19h ··` 标记 —— 事件密度均匀占满画布。
 *  2. 弧线分层：report/decision 2.5px 亮色；ask_user/answer 1.8px；text 1px + 30% 透明。
 *     同一对 agent 在同一时间桶内的密集消息聚合成一条粗束带（带宽=条数），
 *     hover 束带展开成单条弧线；选中线程/悬停 agent 时其他弧线降到 15%。
 *  3. 朝会簇可展开：点击顶部 `☀ 朝会` 标记或双击簇 → 局部 zoom；双击画布退出。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { GroupMessage, Lane } from './types'
import { KIND_COLORS, KIND_WEIGHT, STATUS_COLORS, senderAddr } from './types'

interface Props {
  lanes: Lane[]
  messages: GroupMessage[]
  selectedId: number | null
  onSelect: (m: GroupMessage | null) => void
  laneFilter: string | null
  onLaneFilter: (id: string | null) => void
  /** 回放游标（ms）：只绘制 <= cutoff 的消息。 */
  cutoff: number
}

const HEADER_W = 140
const AXIS_H = 48
const LANE_H = 92
const PAD_R = 28
const GAP_W = 52
const CLUSTER_GAP_MS = 45 * 60e3
const BUNDLE_BUCKET_MS = 4 * 60e3

interface Segment {
  start: number
  end: number
  count: number
  morning: boolean
}

interface SegLayout extends Segment {
  index: number
  x0: number
  x1: number
}

interface GapLayout {
  start: number
  end: number
  x0: number
  x1: number
}

type Layout =
  | { kind: 'overview'; segs: SegLayout[]; gaps: GapLayout[]; w: number }
  | { kind: 'focus'; start: number; end: number; w: number }

interface ArcRec {
  msg: GroupMessage
  x0: number
  y0: number
  x1: number
  y1: number
  self: boolean
}

interface BandRec {
  key: string
  from: string
  to: string
  msgs: GroupMessage[]
  x0: number
  y0: number
  x1: number
  y1: number
  width: number
  color: string
}

// ── 时间簇与非线性布局 ─────────────────────────────────────────────────────

function buildSegments(messages: GroupMessage[]): Segment[] {
  const ts = [...new Set(messages.map((m) => new Date(m.created_at).getTime()))]
    .filter(Number.isFinite)
    .sort((a, b) => a - b)
  if (ts.length === 0) return []
  const out: Segment[] = []
  for (const t of ts) {
    const last = out[out.length - 1]
    if (!last || t - last.end > CLUSTER_GAP_MS) {
      out.push({ start: t, end: t, count: 1, morning: new Date(t).getHours() < 12 })
    } else {
      last.end = t
      last.count += 1
    }
  }
  return out
}

function buildOverview(segs: Segment[], w: number): Layout {
  if (segs.length === 0) return { kind: 'overview', segs: [], gaps: [], w }
  const gapCount = Math.max(0, segs.length - 1)
  const avail = Math.max(120, w - HEADER_W - PAD_R - gapCount * GAP_W)
  const weights = segs.map((s) => Math.sqrt(Math.max(1, s.count)))
  const total = weights.reduce((a, b) => a + b, 0)
  const outSegs: SegLayout[] = []
  const gaps: GapLayout[] = []
  let x = HEADER_W
  for (let i = 0; i < segs.length; i++) {
    const sw = i === segs.length - 1
      ? Math.max(64, HEADER_W + avail - x)
      : Math.max(64, (avail * weights[i]) / total)
    outSegs.push({ ...segs[i], index: i, x0: x, x1: x + sw })
    x += sw
    if (i < segs.length - 1) {
      gaps.push({ start: segs[i].end, end: segs[i + 1].start, x0: x, x1: x + GAP_W })
      x += GAP_W
    }
  }
  return { kind: 'overview', segs: outSegs, gaps, w }
}

function xOfLayout(layout: Layout, t: number): number {
  if (layout.kind === 'focus') {
    const ratio = (t - layout.start) / Math.max(1, layout.end - layout.start)
    return HEADER_W + ratio * (layout.w - HEADER_W - PAD_R)
  }
  if (layout.segs.length === 0) return HEADER_W
  for (const s of layout.segs) {
    if (t <= s.start) return s.x0
    if (t <= s.end) return s.x0 + ((t - s.start) / Math.max(1, s.end - s.start)) * (s.x1 - s.x0)
  }
  return layout.segs[layout.segs.length - 1].x1
}

function timeAtLayout(layout: Layout, x: number): number {
  if (layout.kind === 'focus') {
    const ratio = Math.max(0, Math.min(1, (x - HEADER_W) / Math.max(1, layout.w - HEADER_W - PAD_R)))
    return layout.start + ratio * (layout.end - layout.start)
  }
  for (let i = 0; i < layout.segs.length; i++) {
    const s = layout.segs[i]
    if (x <= s.x1) {
      const r = Math.max(0, Math.min(1, (x - s.x0) / Math.max(1, s.x1 - s.x0)))
      return s.start + r * (s.end - s.start)
    }
    const g = layout.gaps[i]
    if (g && x <= g.x1) {
      const r = Math.max(0, Math.min(1, (x - g.x0) / Math.max(1, g.x1 - g.x0)))
      return g.start + r * (g.end - g.start)
    }
  }
  return layout.segs.length > 0 ? layout.segs[layout.segs.length - 1].end : 0
}

function segmentAt(layout: Layout, t: number): SegLayout | null {
  if (layout.kind !== 'overview') return null
  for (const s of layout.segs) if (t >= s.start && t <= s.end) return s
  return null
}

function gapLabel(ms: number): string {
  const h = ms / 3600e3
  if (h < 1) return `${Math.round(ms / 60e3)}m`
  if (h < 24) return `${Math.round(h)}h`
  const d = Math.floor(h / 24)
  const rh = Math.round(h - d * 24)
  return rh > 0 ? `${d}d${rh}h` : `${d}d`
}

function fmtStamp(t: number): string {
  const d = new Date(t)
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function fmtClock(t: number): string {
  const d = new Date(t)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function niceTicks(start: number, end: number, count: number): number[] {
  const span = end - start
  const raw = span / count
  const steps = [60e3, 5 * 60e3, 15 * 60e3, 30 * 60e3, 3600e3, 3 * 3600e3, 6 * 3600e3, 12 * 3600e3]
  const step = steps.find((s) => s >= raw) ?? steps[steps.length - 1]
  const out: number[] = []
  for (let t = Math.ceil(start / step) * step; t <= end; t += step) out.push(t)
  return out
}

function bezierPoint(t: number, p0: number, p1: number, p2: number, p3: number): number {
  const mt = 1 - t
  return mt * mt * mt * p0 + 3 * mt * mt * t * p1 + 3 * mt * t * t * p2 + t * t * t * p3
}

function truncateCanvas(ctx: CanvasRenderingContext2D, text: string, maxWidth: number): string {
  if (ctx.measureText(text).width <= maxWidth) return text
  let out = text
  while (out.length > 1 && ctx.measureText(`${out}…`).width > maxWidth) out = out.slice(0, -1)
  return `${out}…`
}

function dominantKind(msgs: GroupMessage[]): string {
  const score: Record<string, number> = {}
  for (const m of msgs) score[m.kind] = (score[m.kind] ?? 0) + (KIND_WEIGHT[m.kind] ?? 1)
  return Object.entries(score).sort((a, b) => b[1] - a[1])[0]?.[0] ?? 'text'
}

export function TimelineCanvas({
  lanes, messages, selectedId, onSelect, laneFilter, onLaneFilter, cutoff,
}: Props) {
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const arcsRef = useRef<ArcRec[]>([])
  const bandsRef = useRef<BandRec[]>([])
  const markersRef = useRef<{ x: number; y: number; w: number; h: number; seg: SegLayout }[]>([])
  const layoutRef = useRef<Layout | null>(null)
  const sizeRef = useRef({ w: 900, h: 600 })
  const dragRef = useRef<{ x: number; start: number; end: number } | null>(null)
  const hoverMsgRef = useRef<GroupMessage | null>(null)
  const hoverBandRef = useRef<BandRec | null>(null)
  const [hover, setHover] = useState<{ msg: GroupMessage; x: number; y: number } | null>(null)
  const [hoverBand, setHoverBand] = useState<{ band: BandRec; x: number; y: number } | null>(null)
  const [hoverLane, setHoverLane] = useState<string | null>(null)
  const [expandedKey, setExpandedKey] = useState<string | null>(null)
  const [mode, setMode] = useState<'overview' | 'focus'>('overview')
  const [focus, setFocus] = useState<{ start: number; end: number; label: string } | null>(null)

  const segments = useMemo(() => buildSegments(messages), [messages])

  const teamOf = useCallback((m: GroupMessage): string[] => {
    const from = senderAddr(m)
    return [from, ...(m.to ?? [])]
  }, [])

  const enterFocus = useCallback((seg: Segment) => {
    const pad = Math.max(20 * 60e3, (seg.end - seg.start) * 0.18)
    setFocus({ start: seg.start - pad, end: seg.end + pad, label: `${fmtStamp(seg.start)} 朝会` })
    setMode('focus')
  }, [])

  const latestMorning = useMemo(() => {
    for (let i = segments.length - 1; i >= 0; i--) if (segments[i].morning) return segments[i]
    return segments[segments.length - 1] ?? null
  }, [segments])

  const laneIndex = useMemo(() => {
    const m = new Map<string, number>()
    lanes.forEach((l, i) => m.set(l.id, i))
    return m
  }, [lanes])

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    const wrap = wrapRef.current
    if (!canvas || !wrap) return
    const dpr = Math.min(2, window.devicePixelRatio || 1)
    const w = wrap.clientWidth
    const h = wrap.clientHeight
    if (w <= 0 || h <= 0) return
    if (canvas.width !== Math.floor(w * dpr) || canvas.height !== Math.floor(h * dpr)) {
      canvas.width = Math.floor(w * dpr)
      canvas.height = Math.floor(h * dpr)
      canvas.style.width = `${w}px`
      canvas.style.height = `${h}px`
    }
    sizeRef.current = { w, h }
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, h)

    const layout: Layout = mode === 'focus' && focus
      ? { kind: 'focus', start: focus.start, end: focus.end, w }
      : buildOverview(segments, w)
    layoutRef.current = layout

    // 背景
    const bg = ctx.createLinearGradient(0, 0, 0, h)
    bg.addColorStop(0, '#0a1120')
    bg.addColorStop(1, '#080d18')
    ctx.fillStyle = bg
    ctx.fillRect(0, 0, w, h)

    const threadOf = selectedId == null ? null : messages.find((m) => m.id === selectedId)?.correlation_id ?? null
    const focusLane = hoverLane ?? laneFilter
    const laneY = (id: string) => AXIS_H + (laneIndex.get(id) ?? 0) * LANE_H + LANE_H / 2

    // ── 泳道 ──────────────────────────────────────────────────────────────
    lanes.forEach((lane, i) => {
      const y = AXIS_H + i * LANE_H
      const active = !focusLane || focusLane === lane.id
      const statusColor = STATUS_COLORS[lane.status] ?? STATUS_COLORS.idle
      if (lane.status !== 'idle') {
        ctx.fillStyle = lane.status === 'blocked'
          ? 'rgba(248,113,113,0.07)'
          : 'rgba(251,191,36,0.055)'
        ctx.fillRect(HEADER_W, y, w - HEADER_W - PAD_R, LANE_H)
      }
      if (i > 0) {
        ctx.strokeStyle = 'rgba(148,163,184,0.10)'
        ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke()
      }
      ctx.globalAlpha = active ? 1 : 0.38
      ctx.fillStyle = lane.type === 'user' ? '#f1f5f9' : '#e2e8f0'
      ctx.font = '600 13px system-ui, -apple-system, "Segoe UI", sans-serif'
      ctx.textAlign = 'left'
      ctx.fillText(lane.name, 30, y + LANE_H / 2 - 12)
      ctx.font = '10.5px ui-monospace, SFMono-Regular, monospace'
      ctx.fillStyle = 'rgba(148,163,184,0.8)'
      ctx.fillText(lane.id.replace('agent:', ''), 30, y + LANE_H / 2 + 4)

      ctx.beginPath()
      ctx.arc(16, y + LANE_H / 2 - 8, 4, 0, Math.PI * 2)
      ctx.fillStyle = statusColor
      ctx.shadowColor = statusColor
      ctx.shadowBlur = 9
      ctx.fill()
      ctx.shadowBlur = 0

      const pw = 82
      ctx.fillStyle = 'rgba(148,163,184,0.18)'
      ctx.fillRect(30, y + LANE_H / 2 + 12, pw, 3)
      ctx.fillStyle = statusColor
      ctx.fillRect(30, y + LANE_H / 2 + 12, pw * lane.progress, 3)

      ctx.font = '9.5px system-ui, -apple-system, sans-serif'
      ctx.fillStyle = 'rgba(148,163,184,0.72)'
      ctx.fillText(truncateCanvas(ctx, lane.task || '暂无任务', 104), 30, y + LANE_H / 2 + 30)
      ctx.globalAlpha = 1
    })

    // ── 时间轴（分段折叠 / 聚焦）──────────────────────────────────────────
    ctx.textAlign = 'center'
    if (layout.kind === 'overview') {
      markersRef.current = []
      for (const s of layout.segs) {
        ctx.fillStyle = s.index % 2 === 0 ? 'rgba(56,189,248,0.028)' : 'rgba(148,163,184,0.02)'
        ctx.fillRect(s.x0, AXIS_H, Math.max(0, s.x1 - s.x0), h - AXIS_H)
        ctx.strokeStyle = 'rgba(148,163,184,0.10)'
        ctx.beginPath(); ctx.moveTo(s.x0, AXIS_H); ctx.lineTo(s.x0, h); ctx.stroke()

        if (s.morning) {
          const label = `☀ ${new Date(s.start).getMonth() + 1}/${new Date(s.start).getDate()} 朝会`
          ctx.font = '600 9.5px system-ui, sans-serif'
          const pw = ctx.measureText(label).width + 12
          ctx.fillStyle = 'rgba(192,132,252,0.18)'
          ctx.fillRect(s.x0 + 4, 4, pw, 17)
          ctx.strokeStyle = 'rgba(192,132,252,0.55)'
          ctx.strokeRect(s.x0 + 4.5, 4.5, pw - 1, 16)
          ctx.fillStyle = '#e9d5ff'
          ctx.textAlign = 'left'
          ctx.fillText(label, s.x0 + 10, 16)
          markersRef.current.push({ x: s.x0 + 4, y: 4, w: pw, h: 17, seg: s })
        }
        if (s.x1 - s.x0 > 52) {
          ctx.fillStyle = 'rgba(148,163,184,0.78)'
          ctx.font = '10px ui-monospace, SFMono-Regular, monospace'
          ctx.textAlign = 'center'
          ctx.fillText(fmtStamp(s.start), (s.x0 + s.x1) / 2, 38)
        }
      }
      for (const g of layout.gaps) {
        ctx.fillStyle = 'rgba(2,6,23,0.72)'
        ctx.fillRect(g.x0, AXIS_H, g.x1 - g.x0, h - AXIS_H)
        ctx.strokeStyle = 'rgba(148,163,184,0.06)'
        for (let gx = g.x0 - (h - AXIS_H); gx < g.x1; gx += 14) {
          ctx.beginPath(); ctx.moveTo(gx, h); ctx.lineTo(gx + (h - AXIS_H), AXIS_H); ctx.stroke()
        }
        ctx.strokeStyle = 'rgba(148,163,184,0.18)'
        ctx.setLineDash([3, 4])
        ctx.beginPath(); ctx.moveTo(g.x0, AXIS_H); ctx.lineTo(g.x0, h); ctx.stroke()
        ctx.beginPath(); ctx.moveTo(g.x1, AXIS_H); ctx.lineTo(g.x1, h); ctx.stroke()
        ctx.setLineDash([])
        ctx.fillStyle = 'rgba(148,163,184,0.7)'
        ctx.font = '9px ui-monospace, SFMono-Regular, monospace'
        ctx.textAlign = 'center'
        ctx.fillText(`·· ${gapLabel(g.end - g.start)} ··`, (g.x0 + g.x1) / 2, 22)
      }
    } else {
      const ticks = niceTicks(layout.start, layout.end, 7)
      ctx.font = '10px ui-monospace, SFMono-Regular, monospace'
      for (const t of ticks) {
        const x = xOfLayout(layout, t)
        ctx.strokeStyle = 'rgba(148,163,184,0.10)'
        ctx.beginPath(); ctx.moveTo(x, AXIS_H); ctx.lineTo(x, h); ctx.stroke()
        ctx.fillStyle = 'rgba(203,213,225,0.8)'
        ctx.textAlign = 'center'
        ctx.fillText(fmtClock(t), x, 36)
      }
      ctx.textAlign = 'left'
      ctx.fillStyle = 'rgba(192,132,252,0.9)'
      ctx.font = '600 10px system-ui, sans-serif'
      ctx.fillText(`聚焦 · ${focus?.label ?? ''} · ${fmtClock(layout.start)}–${fmtClock(layout.end)}  （双击退出）`, HEADER_W + 4, 18)
    }

    // ── 消息 / 束带 ──────────────────────────────────────────────────────
    const inView = messages.filter((m) => {
      const t = new Date(m.created_at).getTime()
      if (t > cutoff) return false
      if (layout.kind === 'focus') return t >= layout.start && t <= layout.end
      return true
    })

    interface Entry {
      msg: GroupMessage
      x: number
      from: string
      to: string
      y0: number
      y1: number
      t: number
    }
    const entries: Entry[] = []
    for (const m of inView) {
      const from = senderAddr(m)
      const y0 = laneY(from)
      if (!laneIndex.has(from)) continue
      const t = new Date(m.created_at).getTime()
      const x = xOfLayout(layout, t)
      const targets = (m.to ?? []).filter((a) => laneIndex.has(a))
      if (targets.length === 0) {
        entries.push({ msg: m, x, from, to: from, y0, y1: y0, t })
      } else {
        for (const to of targets) entries.push({ msg: m, x, from, to, y0, y1: laneY(to), t })
      }
    }

    const isDim = (m: GroupMessage): boolean => {
      if (threadOf && m.correlation_id !== threadOf) return true
      if (focusLane) {
        const team = teamOf(m)
        if (!team.includes(focusLane)) return true
      }
      return false
    }

    const drawArc = (m: GroupMessage, x0: number, y0: number, x1: number, y1: number, self: boolean) => {
      const color = KIND_COLORS[m.kind] ?? KIND_COLORS.text
      const weight = KIND_WEIGHT[m.kind] ?? 1.2
      const dim = isDim(m)
      const selected = m.id === selectedId
      const text = m.kind === 'text'
      const baseAlpha = text ? 0.30 : 0.86
      const alpha = dim ? 0.15 : selected ? 1 : baseAlpha
      const lw = selected ? weight + 1.2 : weight
      if (self) {
        ctx.beginPath()
        ctx.arc(x0, y0 - 2, 11, Math.PI * 0.15, Math.PI * 1.7)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha
        ctx.lineWidth = lw
        ctx.stroke()
      } else {
        const dx = Math.max(30, Math.abs(x1 - x0) * 0.5)
        ctx.beginPath()
        ctx.moveTo(x0, y0)
        ctx.bezierCurveTo(x0 + dx, y0, x1 - dx, y1, x1, y1)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha
        ctx.lineWidth = lw
        ctx.shadowColor = selected ? color : 'transparent'
        ctx.shadowBlur = selected ? 11 : 0
        ctx.stroke()
        ctx.shadowBlur = 0
      }
      ctx.beginPath()
      ctx.arc(x1, y1, selected ? 4.2 : 2.8, 0, Math.PI * 2)
      ctx.fillStyle = color
      ctx.globalAlpha = dim ? 0.2 : selected ? 1 : 0.9
      ctx.fill()
      if (!self && !text && !dim) {
        const dx = Math.max(30, Math.abs(x1 - x0) * 0.5)
        const phase = ((performance.now() / 3000) + (m.id % 7) * 0.14) % 1
        const px = bezierPoint(phase, x0, x0 + dx, x1 - dx, x1)
        const py = bezierPoint(phase, y0, y0, y1, y1)
        ctx.beginPath()
        ctx.arc(px, py, selected ? 3.2 : 2, 0, Math.PI * 2)
        ctx.fillStyle = '#f8fafc'
        ctx.globalAlpha = 0.9
        ctx.fill()
      }
      ctx.globalAlpha = 1
      arcsRef.current.push({ msg: m, x0, y0, x1, y1, self })
    }

    // 聚合：同一对 agent + 同一时间桶 → 束带
    type Bucket = { key: string; from: string; to: string; msgs: GroupMessage[]; ys: [number, number] }
    const buckets = new Map<string, Bucket>()
    for (const e of entries) {
      if (e.from === e.to) continue
      const bucket = layout.kind === 'overview'
        ? String(segmentAt(layout, e.t)?.index ?? Math.floor(e.t / BUNDLE_BUCKET_MS))
        : String(Math.floor(e.t / BUNDLE_BUCKET_MS))
      const key = `${e.from}|${e.to}|${bucket}`
      const b = buckets.get(key) ?? { key, from: e.from, to: e.to, msgs: [], ys: [e.y0, e.y1] }
      if (!b.msgs.some((m) => m.id === e.msg.id)) b.msgs.push(e.msg)
      buckets.set(key, b)
    }

    arcsRef.current = []
    bandsRef.current = []
    const bucketList = [...buckets.values()].sort((a, b) => {
      const ka = KIND_WEIGHT[a.msgs[0].kind] ?? 1
      const kb = KIND_WEIGHT[b.msgs[0].kind] ?? 1
      return ka - kb
    })

    for (let bi = 0; bi < bucketList.length; bi++) {
      const b = bucketList[bi]
      const expanded = expandedKey === b.key
      const msgs = b.msgs.slice().sort((a, c) => new Date(a.created_at).getTime() - new Date(c.created_at).getTime())
      const xs = msgs.map((m) => xOfLayout(layout, new Date(m.created_at).getTime()))
      const xStart = Math.min(...xs)
      const xEnd = Math.max(...xs)
      const dim = msgs.every(isDim)
      const stagger = ((bi % 3) - 1) * 3
      if (msgs.length > 1 && !expanded) {
        const kind = dominantKind(msgs)
        const color = KIND_COLORS[kind] ?? KIND_COLORS.text
        const weight = Math.min(10, 1.6 + msgs.length * 1.15)
        const alpha = dim ? 0.08 : kind === 'text' ? 0.18 : 0.36
        const y0 = b.ys[0] + stagger
        const y1 = b.ys[1] + stagger
        const dx = Math.max(58, Math.abs(xEnd - xStart) * 0.85)
        ctx.beginPath()
        ctx.moveTo(xStart, y0)
        ctx.bezierCurveTo(xStart + dx, y0, xEnd - dx, y1, xEnd, y1)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha
        ctx.lineWidth = weight
        ctx.lineCap = 'round'
        ctx.stroke()
        // 中心亮线
        ctx.globalAlpha = dim ? 0.14 : 0.9
        ctx.lineWidth = 1.15
        ctx.stroke()
        ctx.globalAlpha = 1
        // 端点
        for (const [ex, ey] of [[xStart, y0], [xEnd, y1]] as const) {
          ctx.beginPath()
          ctx.arc(ex, ey, 2.6, 0, Math.PI * 2)
          ctx.fillStyle = color
          ctx.globalAlpha = dim ? 0.16 : 0.9
          ctx.fill()
        }
        ctx.globalAlpha = 1
        bandsRef.current.push({
          key: b.key, from: b.from, to: b.to, msgs, x0: xStart, y0, x1: xEnd, y1,
          width: weight, color,
        })
        // 条数标签（沿曲线错开，避免六条带子标签叠在一点）
        const midT = 0.38 + (bi % 3) * 0.12
        const mx = bezierPoint(midT, xStart, xStart + dx, xEnd - dx, xEnd)
        const my = bezierPoint(midT, y0, y0, y1, y1)
        const label = `×${msgs.length}`
        ctx.font = '600 8.5px ui-monospace, SFMono-Regular, monospace'
        const tw = ctx.measureText(label).width
        ctx.fillStyle = 'rgba(2,6,23,0.82)'
        ctx.fillRect(mx - tw / 2 - 3, my - 6, tw + 6, 12)
        ctx.fillStyle = color
        ctx.globalAlpha = dim ? 0.15 : 1
        ctx.textAlign = 'center'
        ctx.fillText(label, mx, my + 2)
        ctx.globalAlpha = 1
      } else {
        for (const m of msgs) {
          const t = new Date(m.created_at).getTime()
          const x = xOfLayout(layout, t)
          drawArc(m, x, b.ys[0] + stagger, x, b.ys[1] + stagger, false)
        }
      }
    }

    // 自环（无接收者）
    for (const e of entries) if (e.from === e.to) drawArc(e.msg, e.x, e.y0, e.x, e.y1, true)

    // 选中消息始终单独高亮（即使它在束带里）
    if (selectedId != null) {
      const sel = entries.find((e) => e.msg.id === selectedId)
      if (sel) drawArc(sel.msg, sel.x, sel.y0, sel.x, sel.y1, sel.from === sel.to)
    }

    // 未回答 ask_user 呼吸脉冲
    for (const m of inView) {
      if (m.kind !== 'ask_user') continue
      const pending = !messages.some((x) => x.correlation_id && x.correlation_id === m.correlation_id && x.kind === 'answer')
      if (!pending || isDim(m)) continue
      const t = new Date(m.created_at).getTime()
      const x = xOfLayout(layout, t)
      const phase = (performance.now() % 1800) / 1800
      for (const to of m.to ?? []) {
        if (!laneIndex.has(to)) continue
        ctx.beginPath()
        ctx.arc(x, laneY(to), 6 + phase * 17, 0, Math.PI * 2)
        ctx.strokeStyle = KIND_COLORS.ask_user
        ctx.globalAlpha = (1 - phase) * 0.8
        ctx.lineWidth = 2
        ctx.stroke()
        ctx.globalAlpha = 1
      }
    }

    // 回放遮罩
    const domainMax = messages.reduce((mx, m) => Math.max(mx, new Date(m.created_at).getTime()), 0)
    if (cutoff < domainMax) {
      const cx = xOfLayout(layout, Math.min(cutoff, layout.kind === 'focus' ? layout.end : domainMax))
      if (cx > HEADER_W && cx < w - PAD_R) {
        const grad = ctx.createLinearGradient(cx, 0, w, 0)
        grad.addColorStop(0, 'rgba(8,13,24,0.0)')
        grad.addColorStop(1, 'rgba(8,13,24,0.86)')
        ctx.fillStyle = grad
        ctx.fillRect(cx, AXIS_H, w - cx - PAD_R, h - AXIS_H)
        ctx.strokeStyle = 'rgba(56,189,248,0.9)'
        ctx.lineWidth = 1.5
        ctx.beginPath(); ctx.moveTo(cx, AXIS_H); ctx.lineTo(cx, h); ctx.stroke()
      }
    }

    // 图例
    const kinds = Object.keys(KIND_COLORS)
    let lx = HEADER_W + 12
    const ly = h - 12
    ctx.font = '10px system-ui, sans-serif'
    ctx.textAlign = 'left'
    for (const k of kinds) {
      ctx.fillStyle = KIND_COLORS[k]
      ctx.fillRect(lx, ly - 7, 12, 3)
      ctx.fillStyle = 'rgba(226,232,240,0.82)'
      ctx.fillText(k, lx + 16, ly - 1)
      lx += 26 + ctx.measureText(k).width + 16
    }
  }, [lanes, messages, laneFilter, laneIndex, selectedId, cutoff, segments, mode, focus, hoverLane, expandedKey, teamOf])

  // 重绘循环（脉冲/流动点/束带）
  useEffect(() => {
    let raf = 0
    const loop = () => { draw(); raf = requestAnimationFrame(loop) }
    raf = requestAnimationFrame(loop)
    return () => cancelAnimationFrame(raf)
  }, [draw])

  useEffect(() => {
    const wrap = wrapRef.current
    if (!wrap) return
    const ob = new ResizeObserver(() => draw())
    ob.observe(wrap)
    return () => ob.disconnect()
  }, [draw])

  // focus 模式没有 focus 值时，自动聚焦最近一次朝会。
  useEffect(() => {
    if (mode === 'focus' && !focus && latestMorning) enterFocus(latestMorning)
  }, [mode, focus, latestMorning, enterFocus])

  const onWheel = useCallback((e: React.WheelEvent) => {
    e.preventDefault()
    const wrap = wrapRef.current
    const layout = layoutRef.current
    if (!wrap || !layout) return
    const rect = wrap.getBoundingClientRect()
    const t = timeAtLayout(layout, e.clientX - rect.left)
    if (layout.kind === 'focus') {
      const span = layout.end - layout.start
      const next = Math.max(10 * 60e3, Math.min(7 * 24 * 3600e3, span * (e.deltaY > 0 ? 1.25 : 0.8)))
      const ratio = (t - layout.start) / Math.max(1, span)
      setFocus({ start: t - next * ratio, end: t + next * (1 - ratio), label: focus?.label ?? '' })
      return
    }
    const seg = segmentAt(layout, t) ?? layout.segs[0]
    if (seg) enterFocus(seg)
  }, [enterFocus, focus])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    ;(e.target as HTMLElement).setPointerCapture?.(e.pointerId)
    if (mode === 'focus' && focus) dragRef.current = { x: e.clientX, start: focus.start, end: focus.end }
  }, [mode, focus])

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    const wrap = wrapRef.current
    const layout = layoutRef.current
    if (!wrap || !layout) return
    const rect = wrap.getBoundingClientRect()
    const mx = e.clientX - rect.left
    const my = e.clientY - rect.top

    const drag = dragRef.current
    if (drag) {
      const dx = e.clientX - drag.x
      const span = drag.end - drag.start
      const perPx = span / Math.max(1, rect.width - HEADER_W - PAD_R)
      setFocus({ start: drag.start - dx * perPx, end: drag.end - dx * perPx, label: focus?.label ?? '' })
      return
    }

    // 泳道 hover → focus 模式
    if (mx < HEADER_W) {
      const idx = Math.floor((my - AXIS_H) / LANE_H)
      const lane = idx >= 0 ? lanes[idx] : null
      setHoverLane(lane?.id ?? null)
      hoverMsgRef.current = null
      hoverBandRef.current = null
      setHover(null)
      setHoverBand(null)
      return
    }
    setHoverLane(null)

    // 单条弧线命中
    let best: { m: GroupMessage; d: number } | null = null
    for (const a of arcsRef.current) {
      let d: number
      if (a.self) {
        d = Math.hypot(mx - a.x0, my - (a.y0 - 12))
      } else {
        d = Infinity
        const dx = Math.max(30, Math.abs(a.x1 - a.x0) * 0.5)
        for (let i = 1; i <= 18; i++) {
          const t = i / 18
          const px = bezierPoint(t, a.x0, a.x0 + dx, a.x1 - dx, a.x1)
          const py = bezierPoint(t, a.y0, a.y0, a.y1, a.y1)
          d = Math.min(d, Math.hypot(mx - px, my - py))
        }
      }
      if (d < 9 && (!best || d < best.d)) best = { m: a.msg, d }
    }
    if (best) {
      hoverMsgRef.current = best.m
      hoverBandRef.current = null
      setHover({ msg: best.m, x: mx, y: my })
      setHoverBand(null)
      return
    }

    // 束带命中
    let bestBand: BandRec | null = null
    let bestD = Infinity
    for (const b of bandsRef.current) {
      const dx = Math.max(30, Math.abs(b.x1 - b.x0) * 0.5)
      for (let i = 1; i <= 20; i++) {
        const t = i / 20
        const px = bezierPoint(t, b.x0, b.x0 + dx, b.x1 - dx, b.x1)
        const py = bezierPoint(t, b.y0, b.y0, b.y1, b.y1)
        const d = Math.hypot(mx - px, my - py)
        if (d < Math.max(7, b.width / 2 + 4) && d < bestD) { bestD = d; bestBand = b }
      }
    }
    if (bestBand) {
      setExpandedKey(bestBand.key)
      hoverBandRef.current = bestBand
      hoverMsgRef.current = null
      setHoverBand({ band: bestBand, x: mx, y: my })
      setHover(null)
      return
    }

    hoverMsgRef.current = null
    hoverBandRef.current = null
    setHover(null)
    setHoverBand(null)
    setExpandedKey(null)
  }, [lanes, focus, mode])

  const onPointerUp = useCallback(() => { dragRef.current = null }, [])

  const onClick = useCallback((e: React.MouseEvent) => {
    const wrap = wrapRef.current
    if (!wrap) return
    const rect = wrap.getBoundingClientRect()
    const mx = e.clientX - rect.left
    const my = e.clientY - rect.top

    // 朝会标记 → 局部 zoom
    for (const m of markersRef.current) {
      if (mx >= m.x && mx <= m.x + m.w && my >= m.y && my <= m.y + m.h) {
        enterFocus(m.seg)
        return
      }
    }
    if (hoverMsgRef.current) { onSelect(hoverMsgRef.current); return }
    if (hoverBandRef.current) { onSelect(hoverBandRef.current.msgs[0]); return }
    if (mx < HEADER_W) {
      const idx = Math.floor((my - AXIS_H) / LANE_H)
      const lane = lanes[idx]
      if (lane) onLaneFilter(laneFilter === lane.id ? null : lane.id)
    }
  }, [enterFocus, lanes, laneFilter, onLaneFilter, onSelect])

  const onDoubleClick = useCallback((e: React.MouseEvent) => {
    if (mode === 'focus') { setMode('overview'); setFocus(null); return }
    const layout = layoutRef.current
    const wrap = wrapRef.current
    if (!layout || !wrap) return
    const rect = wrap.getBoundingClientRect()
    const t = timeAtLayout(layout, e.clientX - rect.left)
    const seg = segmentAt(layout, t)
    if (seg) enterFocus(seg)
  }, [mode, enterFocus])

  const onPointerLeave = useCallback(() => {
    setHover(null)
    setHoverBand(null)
    setHoverLane(null)
    setExpandedKey(null)
    hoverMsgRef.current = null
    hoverBandRef.current = null
    dragRef.current = null
  }, [])

  return (
    <div ref={wrapRef} className="relative min-h-0 min-w-0 flex-1 overflow-hidden">
      <canvas
        ref={canvasRef}
        className="block h-full w-full cursor-crosshair"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={onPointerLeave}
        onClick={onClick}
        onDoubleClick={onDoubleClick}
        aria-label="通信时间轴"
      />

      {/* 全景 / 聚焦切换 */}
      <div className="absolute right-3 top-2 z-10 flex overflow-hidden rounded-md border text-[10px]"
        style={{ borderColor: 'rgba(148,163,184,0.22)', background: 'rgba(2,6,23,0.72)' }}>
        <button type="button" data-testid="court-overview"
          onClick={() => { setMode('overview'); setFocus(null) }}
          className="px-2 py-1"
          style={{
            background: mode === 'overview' ? 'rgba(56,189,248,0.18)' : 'transparent',
            color: mode === 'overview' ? '#7dd3fc' : 'rgba(148,163,184,0.75)',
          }}>
          全景
        </button>
        <button type="button" data-testid="court-focus"
          onClick={() => { setMode('focus'); if (!focus && latestMorning) enterFocus(latestMorning) }}
          className="px-2 py-1"
          style={{
            background: mode === 'focus' ? 'rgba(192,132,252,0.18)' : 'transparent',
            color: mode === 'focus' ? '#d8b4fe' : 'rgba(148,163,184,0.75)',
          }}>
          聚焦朝会
        </button>
      </div>

      {hover ? (
        <div
          className="pointer-events-none absolute z-10 max-w-[300px] rounded-lg border px-3 py-2 text-[11px] shadow-2xl backdrop-blur"
          style={{
            left: Math.min(hover.x + 14, sizeRef.current.w - 316),
            top: Math.min(hover.y + 14, sizeRef.current.h - 110),
            background: 'rgba(10,17,32,0.96)',
            borderColor: 'rgba(148,163,184,0.26)',
            color: '#e2e8f0',
          }}
        >
          <div className="mb-1 flex items-center gap-2">
            <span className="inline-block size-2 rounded-full" style={{ background: KIND_COLORS[hover.msg.kind] }} />
            <span className="font-semibold">{hover.msg.sender_name}</span>
            <span style={{ color: 'rgba(148,163,184,0.8)' }}>{hover.msg.kind}</span>
            <span className="ml-auto font-mono" style={{ color: 'rgba(148,163,184,0.65)' }}>
              {fmtClock(new Date(hover.msg.created_at).getTime())}
            </span>
          </div>
          <div className="leading-4" style={{ color: 'rgba(226,232,240,0.92)' }}>{hover.msg.content.slice(0, 140)}</div>
          {hover.msg.to && hover.msg.to.length > 0 ? (
            <div className="mt-1 font-mono text-[10px]" style={{ color: 'rgba(148,163,184,0.72)' }}>
              → {hover.msg.to.join('  ')}
            </div>
          ) : null}
        </div>
      ) : null}

      {hoverBand ? (
        <div
          className="pointer-events-none absolute z-10 max-w-[300px] rounded-lg border px-3 py-2 text-[11px] shadow-2xl backdrop-blur"
          style={{
            left: Math.min(hoverBand.x + 14, sizeRef.current.w - 316),
            top: Math.min(hoverBand.y + 14, sizeRef.current.h - 110),
            background: 'rgba(10,17,32,0.96)',
            borderColor: 'rgba(148,163,184,0.26)',
            color: '#e2e8f0',
          }}
        >
          <div className="mb-1 flex items-center gap-2">
            <span className="inline-block size-2 rounded-full" style={{ background: hoverBand.band.color }} />
            <span className="font-semibold">{hoverBand.band.from.replace('agent:', '')} → {hoverBand.band.to.replace('agent:', '')}</span>
            <span className="ml-auto font-mono" style={{ color: '#7dd3fc' }}>×{hoverBand.band.msgs.length}</span>
          </div>
          <div className="text-[10px]" style={{ color: 'rgba(148,163,184,0.8)' }}>
            {hoverBand.band.msgs.slice(0, 3).map((m) => `${fmtClock(new Date(m.created_at).getTime())} ${m.kind}`).join(' · ')}
            {hoverBand.band.msgs.length > 3 ? ' …' : ''}
          </div>
          <div className="mt-1 text-[9px]" style={{ color: 'rgba(148,163,184,0.55)' }}>hover 已展开为单条弧线</div>
        </div>
      ) : null}
    </div>
  )
}
