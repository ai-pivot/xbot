/**
 * TimelineCanvas —— 指挥室主视图。
 *
 * 纵向泳道（参与者）× 横向时间轴；每条消息 = 发送泳道到接收泳道的贝塞尔弧线。
 * 纯 Canvas 2D：消息 < 2000 时 60fps 无压力；只绘制可视时间窗内的弧线。
 *
 * 交互：滚轮缩放（指针锚定）、拖拽平移、hover 高亮、click 选中、双击回到全览。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { GroupMessage, Lane } from './types'
import { KIND_COLORS, STATUS_COLORS, senderAddr } from './types'

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

interface Arc {
  msg: GroupMessage
  x0: number
  y0: number
  x1: number
  y1: number
  self: boolean
  color: string
}

const HEADER_W = 132
const AXIS_H = 44
const LANE_H = 92
const PAD_R = 26

function niceTicks(start: number, end: number, count: number): number[] {
  const span = end - start
  const raw = span / count
  const steps = [60e3, 5 * 60e3, 15 * 60e3, 30 * 60e3, 3600e3, 3 * 3600e3, 6 * 3600e3,
    12 * 3600e3, 24 * 3600e3, 2 * 24 * 3600e3]
  const step = steps.find((s) => s >= raw) ?? steps[steps.length - 1]
  const first = Math.ceil(start / step) * step
  const out: number[] = []
  for (let t = first; t <= end; t += step) out.push(t)
  return out
}

function fmtTick(t: number, span: number): string {
  const d = new Date(t)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  if (span > 2 * 24 * 3600e3) return `${d.getMonth() + 1}/${d.getDate()}`
  if (span > 26 * 3600e3) return `${d.getMonth() + 1}/${d.getDate()} ${hh}:${mm}`
  return `${hh}:${mm}`
}

function bezierPoint(t: number, p0: number, p1: number, p2: number, p3: number): number {
  const mt = 1 - t
  return mt * mt * mt * p0 + 3 * mt * mt * t * p1 + 3 * mt * t * t * p2 + t * t * t * p3
}

/** 按像素宽度截断 Canvas 文本（超出补 …）。 */
function truncateCanvas(ctx: CanvasRenderingContext2D, text: string, maxWidth: number): string {
  if (ctx.measureText(text).width <= maxWidth) return text
  let out = text
  while (out.length > 1 && ctx.measureText(`${out}…`).width > maxWidth) out = out.slice(0, -1)
  return `${out}…`
}

export function TimelineCanvas({
  lanes, messages, selectedId, onSelect, laneFilter, onLaneFilter, cutoff,
}: Props) {
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const arcsRef = useRef<Arc[]>([])
  const sizeRef = useRef({ w: 900, h: 600 })
  const dragRef = useRef<{ x: number; start: number; end: number } | null>(null)
  const [hover, setHover] = useState<GroupMessage | null>(null)
  const [hoverPos, setHoverPos] = useState({ x: 0, y: 0 })
  const [viewport, setViewport] = useState<{ start: number; end: number } | null>(null)

  const domain = useMemo(() => {
    const ts = messages.map((m) => new Date(m.created_at).getTime()).filter(Number.isFinite)
    if (ts.length === 0) return null
    const min = Math.min(...ts)
    const max = Math.max(...ts)
    const pad = Math.max(90e3, (max - min) * 0.03)
    return { min: min - pad, max: max + pad }
  }, [messages])

  // 初始 / 数据变化时回到全览（用户已手动缩放则保留，除非范围失效）。
  useEffect(() => {
    if (!domain) { setViewport(null); return }
    setViewport((vp) => {
      if (!vp || vp.end - vp.start <= 0) return { start: domain.min, end: domain.max }
      if (vp.start < domain.min || vp.end > domain.max) return { start: domain.min, end: domain.max }
      return vp
    })
  }, [domain])

  const view = viewport ?? (domain ? { start: domain.min, end: domain.max } : { start: 0, end: 1 })

  const laneIndex = useMemo(() => {
    const m = new Map<string, number>()
    lanes.forEach((l, i) => m.set(l.id, i))
    return m
  }, [lanes])

  const xOf = useCallback((t: number, w: number) => {
    const ratio = (t - view.start) / Math.max(1, view.end - view.start)
    return HEADER_W + ratio * (w - HEADER_W - PAD_R)
  }, [view.start, view.end])

  const laneY = useCallback((id: string) => AXIS_H + (laneIndex.get(id) ?? 0) * LANE_H + LANE_H / 2, [laneIndex])

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

    // 背景
    const bg = ctx.createLinearGradient(0, 0, 0, h)
    bg.addColorStop(0, '#0b1220')
    bg.addColorStop(1, '#0a0f1a')
    ctx.fillStyle = bg
    ctx.fillRect(0, 0, w, h)

    const anim = performance.now()
    const visible = messages.filter((m) => {
      const t = new Date(m.created_at).getTime()
      return t <= cutoff && t >= view.start && t <= view.end
    })
    const threadOf = selectedId == null ? null : messages.find((m) => m.id === selectedId)?.correlation_id ?? null

    // 泳道背景 + 标签
    lanes.forEach((lane, i) => {
      const y = AXIS_H + i * LANE_H
      const active = !laneFilter || laneFilter === lane.id
      const statusColor = STATUS_COLORS[lane.status] ?? STATUS_COLORS.idle
      if (lane.status !== 'idle') {
        ctx.fillStyle = lane.status === 'blocked'
          ? 'rgba(239,68,68,0.055)'
          : 'rgba(245,165,36,0.045)'
        ctx.fillRect(HEADER_W, y, w - HEADER_W - PAD_R, LANE_H)
      }
      if (i > 0) {
        ctx.strokeStyle = 'rgba(148,163,184,0.09)'
        ctx.lineWidth = 1
        ctx.beginPath()
        ctx.moveTo(0, y)
        ctx.lineTo(w, y)
        ctx.stroke()
      }
      // 左侧标签
      if (i > 0) {
        ctx.strokeStyle = 'rgba(148,163,184,0.10)'
        ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke()
      }
      ctx.globalAlpha = active ? 1 : 0.32
      ctx.fillStyle = lane.type === 'user' ? '#e2e8f0' : '#cbd5e1'
      ctx.font = '600 13px system-ui, -apple-system, "Segoe UI", sans-serif'
      ctx.textAlign = 'left'
      ctx.fillText(lane.name, 30, y + LANE_H / 2 - 12)
      ctx.font = '11px ui-monospace, SFMono-Regular, monospace'
      ctx.fillStyle = 'rgba(148,163,184,0.75)'
      ctx.fillText(lane.id.replace('agent:', ''), 30, y + LANE_H / 2 + 4)

      // 状态灯
      ctx.beginPath()
      ctx.arc(16, y + LANE_H / 2 - 8, 4, 0, Math.PI * 2)
      ctx.fillStyle = statusColor
      ctx.shadowColor = statusColor
      ctx.shadowBlur = 8
      ctx.fill()
      ctx.shadowBlur = 0

      // 进度条
      const pw = 74
      ctx.fillStyle = 'rgba(148,163,184,0.16)'
      ctx.fillRect(30, y + LANE_H / 2 + 12, pw, 3)
      ctx.fillStyle = statusColor
      ctx.fillRect(30, y + LANE_H / 2 + 12, pw * lane.progress, 3)

      // 任务摘要（左下，固定宽度截断，避免溢出到画布）
      ctx.font = '10px system-ui, -apple-system, sans-serif'
      ctx.fillStyle = 'rgba(148,163,184,0.62)'
      ctx.textAlign = 'left'
      ctx.fillText(truncateCanvas(ctx, lane.task || '暂无任务', 96), 30, y + LANE_H / 2 + 30)
      ctx.textAlign = 'left'
      ctx.globalAlpha = 1
    })

    // 时间网格
    const ticks = niceTicks(view.start, view.end, 7)
    ctx.font = '10px ui-monospace, SFMono-Regular, monospace'
    ctx.textAlign = 'center'
    for (const t of ticks) {
      const x = xOf(t, w)
      ctx.strokeStyle = 'rgba(148,163,184,0.09)'
      ctx.beginPath(); ctx.moveTo(x, AXIS_H); ctx.lineTo(x, h); ctx.stroke()
      ctx.fillStyle = 'rgba(148,163,184,0.62)'
      ctx.fillText(fmtTick(t, view.end - view.start), x, 24)
    }

    // 朝会时刻 09:00 竖线 + 旗标
    const day = new Date(view.start)
    day.setHours(9, 0, 0, 0)
    if (day.getTime() < view.start) day.setDate(day.getDate() + 1)
    for (let t = day.getTime(); t <= view.end; t += 24 * 3600e3) {
      const x = xOf(t, w)
      if (x < HEADER_W) continue
      ctx.strokeStyle = 'rgba(167,139,250,0.30)'
      ctx.setLineDash([4, 5])
      ctx.beginPath(); ctx.moveTo(x, AXIS_H); ctx.lineTo(x, h); ctx.stroke()
      ctx.setLineDash([])
      ctx.fillStyle = 'rgba(167,139,250,0.9)'
      ctx.font = '9px system-ui, sans-serif'
      ctx.textAlign = 'left'
      ctx.fillText('☀ 朝会', x + 4, AXIS_H + 10)
    }

    // 弧线
    const arcs: Arc[] = []
    const visibleSorted = visible.slice().sort((a, b) => a.id - b.id)
    for (const m of visibleSorted) {
      const fromAddr = senderAddr(m)
      if (!laneIndex.has(fromAddr)) continue
      const t = new Date(m.created_at).getTime()
      const x1 = xOf(t, w)
      const y1 = laneY(fromAddr)
      const targets = (m.to && m.to.length > 0)
        ? m.to.filter((a) => laneIndex.has(a))
        : []
      const isThread = threadOf != null && m.correlation_id === threadOf
      const dim = threadOf != null && !isThread
      const selected = m.id === selectedId
      const color = KIND_COLORS[m.kind] ?? KIND_COLORS.text

      const drawOne = (x0: number, y0: number, x2: number, y2: number, selfLoop: boolean) => {
        const dx = Math.max(32, Math.abs(x2 - x0) * 0.5)
        if (selfLoop) {
          const r = 12
          ctx.beginPath()
          ctx.arc(x0, y0 - 2, r, Math.PI * 0.15, Math.PI * 1.7)
          ctx.strokeStyle = color
          ctx.globalAlpha = dim ? 0.12 : selected ? 1 : 0.55
          ctx.lineWidth = selected ? 2.6 : 1.4
          ctx.stroke()
        } else {
          ctx.beginPath()
          ctx.moveTo(x0, y0)
          ctx.bezierCurveTo(x0 + dx, y0, x2 - dx, y2, x2, y2)
          ctx.strokeStyle = color
          ctx.globalAlpha = dim ? 0.10 : selected ? 1 : 0.52
          ctx.lineWidth = selected ? 2.8 : m.kind === 'report' ? 1.9 : 1.35
          ctx.shadowColor = selected ? color : 'transparent'
          ctx.shadowBlur = selected ? 10 : 0
          ctx.stroke()
          ctx.shadowBlur = 0
        }
        // 端点
        ctx.beginPath()
        ctx.arc(x2, y2, selected ? 4.2 : 2.6, 0, Math.PI * 2)
        ctx.fillStyle = color
        ctx.globalAlpha = dim ? 0.18 : selected ? 1 : 0.85
        ctx.fill()
        // 流动光点（kind 非 text）
        if (!selfLoop && m.kind !== 'text' && !dim) {
          const phase = ((anim / 3200) + (m.id % 7) * 0.13) % 1
          const px = bezierPoint(phase, x0, x0 + dx, x2 - dx, x2)
          const py = bezierPoint(phase, y0, y0, y2, y2)
          ctx.beginPath()
          ctx.arc(px, py, selected ? 3.2 : 1.9, 0, Math.PI * 2)
          ctx.fillStyle = '#e2e8f0'
          ctx.globalAlpha = 0.85
          ctx.fill()
        }
        ctx.globalAlpha = 1
        arcs.push({ msg: m, x0, y0, x1: x2, y1: y2, self: selfLoop, color })
      }

      // 发送者端点
      ctx.beginPath()
      ctx.arc(x1, y1, selected ? 4.6 : 3.4, 0, Math.PI * 2)
      ctx.fillStyle = color
      ctx.globalAlpha = dim ? 0.2 : 1
      ctx.fill()
      ctx.globalAlpha = 1

      if (targets.length === 0) {
        drawOne(x1, y1, x1, y1, true)
      } else {
        for (const to of targets) drawOne(x1, y1, xOf(t, w), laneY(to), false)
      }

      // 未回答 ask_user：目标泳道呼吸脉冲
      if (m.kind === 'ask_user') {
        const pending = !messages.some((x) =>
          x.correlation_id && x.correlation_id === m.correlation_id && x.kind === 'answer')
        if (pending && !dim) {
          const phase = (anim % 1800) / 1800
          for (const to of (m.to ?? [])) {
            if (!laneIndex.has(to)) continue
            const ty = laneY(to)
            const tx = xOf(t, w)
            ctx.beginPath()
            ctx.arc(tx, ty, 6 + phase * 16, 0, Math.PI * 2)
            ctx.strokeStyle = KIND_COLORS.ask_user
            ctx.globalAlpha = (1 - phase) * 0.75
            ctx.lineWidth = 2
            ctx.stroke()
            ctx.globalAlpha = 1
          }
        }
      }
    }
    arcsRef.current = arcs

    // 回放拖影
    if (cutoff < (domain?.max ?? cutoff)) {
      const cx = xOf(cutoff, w)
      const grad = ctx.createLinearGradient(cx, 0, w, 0)
      grad.addColorStop(0, 'rgba(10,15,26,0.0)')
      grad.addColorStop(1, 'rgba(10,15,26,0.82)')
      ctx.fillStyle = grad
      ctx.fillRect(cx, AXIS_H, w - cx - PAD_R, h - AXIS_H)
      ctx.strokeStyle = 'rgba(56,189,248,0.85)'
      ctx.lineWidth = 1.5
      ctx.beginPath(); ctx.moveTo(cx, AXIS_H); ctx.lineTo(cx, h); ctx.stroke()
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
      ctx.fillStyle = 'rgba(203,213,225,0.75)'
      ctx.fillText(k, lx + 16, ly - 1)
      lx += 26 + ctx.measureText(k).width + 14
    }
  }, [lanes, messages, laneFilter, laneIndex, laneY, view, xOf, selectedId, cutoff, domain])

  // 重绘循环（脉冲/流动点）
  useEffect(() => {
    let raf = 0
    const loop = () => { draw(); raf = requestAnimationFrame(loop) }
    raf = requestAnimationFrame(loop)
    return () => cancelAnimationFrame(raf)
  }, [draw])

  // Resize
  useEffect(() => {
    const wrap = wrapRef.current
    if (!wrap) return
    const ob = new ResizeObserver(() => draw())
    ob.observe(wrap)
    return () => ob.disconnect()
  }, [draw])

  const timeAt = useCallback((clientX: number) => {
    const wrap = wrapRef.current
    if (!wrap) return view.start
    const rect = wrap.getBoundingClientRect()
    const x = clientX - rect.left
    const ratio = Math.max(0, Math.min(1, (x - HEADER_W) / Math.max(1, rect.width - HEADER_W - PAD_R)))
    return view.start + ratio * (view.end - view.start)
  }, [view])

  const onWheel = useCallback((e: React.WheelEvent) => {
    e.preventDefault()
    const anchor = timeAt(e.clientX)
    const factor = e.deltaY > 0 ? 1.22 : 0.82
    const nextSpan = Math.max(40e3, Math.min((domain ? domain.max - domain.min : 1) * 1.2,
      (view.end - view.start) * factor))
    const ratio = (anchor - view.start) / Math.max(1, view.end - view.start)
    let start = anchor - nextSpan * ratio
    let end = start + nextSpan
    if (domain) {
      if (start < domain.min) { start = domain.min; end = start + nextSpan }
      if (end > domain.max) { end = domain.max; start = end - nextSpan }
    }
    setViewport({ start, end })
  }, [timeAt, view, domain])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    (e.target as HTMLElement).setPointerCapture?.(e.pointerId)
    dragRef.current = { x: e.clientX, start: view.start, end: view.end }
  }, [view])

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    const wrap = wrapRef.current
    if (!wrap) return
    const rect = wrap.getBoundingClientRect()
    const mx = e.clientX - rect.left
    const my = e.clientY - rect.top
    // 拖拽平移
    const drag = dragRef.current
    if (drag) {
      const dx = e.clientX - drag.x
      const span = drag.end - drag.start
      const perPx = span / Math.max(1, rect.width - HEADER_W - PAD_R)
      let start = drag.start - dx * perPx
      if (domain) {
        start = Math.max(domain.min, Math.min(domain.max - span, start))
      }
      setViewport({ start, end: start + span })
      return
    }
    // hover 命中：采样贝塞尔距离
    let best: { m: GroupMessage; d: number } | null = null
    for (const a of arcsRef.current) {
      let d: number
      if (a.self) {
        d = Math.hypot(mx - a.x0, my - (a.y0 - 12))
      } else {
        d = Infinity
        const dx = Math.max(32, Math.abs(a.x1 - a.x0) * 0.5)
        for (let i = 1; i <= 18; i++) {
          const t = i / 18
          const px = bezierPoint(t, a.x0, a.x0 + dx, a.x1 - dx, a.x1)
          const py = bezierPoint(t, a.y0, a.y0, a.y1, a.y1)
          d = Math.min(d, Math.hypot(mx - px, my - py))
        }
      }
      if (d < 9 && (!best || d < best.d)) best = { m: a.msg, d }
    }
    setHover(best?.m ?? null)
    setHoverPos({ x: mx, y: my })
  }, [domain])

  const onPointerUp = useCallback(() => { dragRef.current = null }, [])

  const onClick = useCallback((e: React.MouseEvent) => {
    if (hover) { onSelect(hover); return }
    const wrap = wrapRef.current
    if (!wrap) return
    const rect = wrap.getBoundingClientRect()
    // 点击左侧标签 → 过滤泳道
    if (e.clientX - rect.left < HEADER_W) {
      const idx = Math.floor((e.clientY - rect.top - AXIS_H) / LANE_H)
      const lane = lanes[idx]
      if (lane) onLaneFilter(laneFilter === lane.id ? null : lane.id)
    }
  }, [hover, lanes, laneFilter, onLaneFilter, onSelect])

  const onDoubleClick = useCallback(() => {
    if (domain) setViewport({ start: domain.min, end: domain.max })
  }, [domain])

  return (
    <div ref={wrapRef} className="relative min-h-0 min-w-0 flex-1 overflow-hidden">
      <canvas
        ref={canvasRef}
        className="block h-full w-full cursor-crosshair"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={() => { setHover(null); dragRef.current = null }}
        onClick={onClick}
        onDoubleClick={onDoubleClick}
        aria-label="通信时间轴"
      />
      {hover ? (
        <div
          className="pointer-events-none absolute z-10 max-w-[300px] rounded-lg border px-3 py-2 text-[11px] shadow-2xl backdrop-blur"
          style={{
            left: Math.min(hoverPos.x + 14, sizeRef.current.w - 316),
            top: Math.min(hoverPos.y + 14, sizeRef.current.h - 110),
            background: 'rgba(15,23,42,0.94)',
            borderColor: 'rgba(148,163,184,0.22)',
            color: '#e2e8f0',
          }}
        >
          <div className="mb-1 flex items-center gap-2">
            <span className="inline-block size-2 rounded-full" style={{ background: KIND_COLORS[hover.kind] }} />
            <span className="font-semibold">{hover.sender_name}</span>
            <span style={{ color: 'rgba(148,163,184,0.8)' }}>{hover.kind}</span>
            <span className="ml-auto font-mono" style={{ color: 'rgba(148,163,184,0.65)' }}>
              {new Date(hover.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
            </span>
          </div>
          <div className="leading-4" style={{ color: 'rgba(226,232,240,0.9)' }}>{hover.content.slice(0, 140)}</div>
          {hover.to && hover.to.length > 0 ? (
            <div className="mt-1 font-mono text-[10px]" style={{ color: 'rgba(148,163,184,0.7)' }}>
              → {hover.to.join('  ')}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
