/**
 * StagingTray — 待发队列托盘（排在 MessageList 和 MessageInput 之间）。
 *
 * 数据源：ChatStore state.queue（queue_state SSE 事件全量替换语义）。
 * 队列为空时不渲染任何 DOM（height 0）。
 *
 * 卡片设计：
 *   - 队首：indigo accent 边框 + 发光序号 + 底部呼吸进度条 + 行内 "Next" 徽章
 *   - 非队首：muted 边框 + 普通序号
 *   - 🔔 通知项：只显示 ✕（不可转插话）
 *
 * 结构契约（重新设计，取代「两级展开 + 缩进凑对齐」的旧版）：
 *   1. **只有一个展开概念** —— header 的 toggle（`collapsed`）。展开 = 全量渲染
 *      队列项；长队列由列表容器的**内部滚动**（有界 max-h + overflow-y-auto）容纳，
 *      不存在第二层「显示全部/收起列表」，也没有「只显示前 3 条」的截断常量。
 *   2. **对齐由结构保证** —— 队首卡加左侧 accent 条（`border-l-2 border-l-indigo-500`）
 *      + 预览行内的小 pill（`staging-next-mark`）。pill 是 `staging-card-preview`
 *      容器的第一个 in-flow 子元素、该容器无 padding，因此
 *      `pill.left === previewContainer.left`（同一条布局链推导，零缩进魔数）；
 *      footer 已删除，`Clear` 是 header 里与 toggle **不同动作**的图标按钮。
 *
 * 动画：CSS keyframes（fadeUp 入场、左滑淡出取消、队首呼吸进度条）
 */
import { memo, useState, useCallback, useEffect, useRef } from 'react'
import { Zap, X, Bell, ChevronDown, ChevronRight, Trash2, Inbox, User, GripVertical } from 'lucide-react'
import type { QueueItemPayload } from '@/types/shared'
import { cn } from '@/lib/utils'
import { computeReorder } from '@/lib/reorder'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'

// ─── CSS keyframes（注入一次，组件级 scope） ─────────────────────────
const STAGING_TRAY_STYLES = `
@keyframes stagingFadeUp {
  from { opacity: 0; transform: translateY(6px); }
  to   { opacity: 1; transform: translateY(0); }
}
@keyframes stagingSlideOut {
  from { opacity: 1; transform: translateX(0); max-height: 200px; }
  to   { opacity: 0; transform: translateX(-24px); max-height: 0; padding: 0; margin: 0; }
}
@keyframes stagingShimmer {
  0%, 100% { opacity: 0.35; }
  50%      { opacity: 1; }
}
@keyframes stagingGlow {
  0%, 100% { box-shadow: 0 0 4px rgba(99,102,241,0.4); }
  50%      { box-shadow: 0 0 10px rgba(99,102,241,0.7); }
}
.staging-card-enter { animation: stagingFadeUp 0.2s ease-out forwards; }
.staging-card-leave { animation: stagingSlideOut 0.2s ease-out forwards; overflow: hidden; }
.staging-shimmer-bar { animation: stagingShimmer 1.8s ease-in-out infinite; }
.staging-glow-num { animation: stagingGlow 2s ease-in-out infinite; }

/* Drag-to-reorder: lifted card + sliding drop indicator (transform/opacity only
   — compositor-friendly, no layout animation) */
.staging-card-dragging {
  opacity: 0.45;
  transform: scale(0.985);
  box-shadow: 0 8px 24px -8px rgba(99,102,241,0.55);
  border-color: rgba(129,140,248,0.75) !important;
}
.staging-drop-line {
  height: 2px;
  border-radius: 9999px;
  background: linear-gradient(90deg, rgba(99,102,241,0.25), rgba(129,140,248,1), rgba(99,102,241,0.25));
  animation: stagingDropIn 0.14s ease-out;
}
@keyframes stagingDropIn {
  from { opacity: 0; transform: scaleX(0.4); }
  to   { opacity: 1; transform: scaleX(1); }
}
.staging-drag-handle { touch-action: none; }

/* Touch devices (no hover): always show action buttons + enlarge tap targets */
@media (hover: none) {
  .staging-card .staging-card-actions { opacity: 1 !important; }
  .staging-card .staging-card-actions button { min-width: 32px; min-height: 32px; }
  .staging-card .staging-drag-handle { opacity: 1 !important; }
}
`

let styleInjected = false
function injectStyles() {
  if (styleInjected || typeof document === 'undefined') return
  styleInjected = true
  const el = document.createElement('style')
  el.setAttribute('data-staging-tray', '')
  el.textContent = STAGING_TRAY_STYLES
  document.head.appendChild(el)
}

// ─── 拖拽边缘自动滚动参数（长队列拖拽的根因修复）─────────────────────
//
// 拖拽用 pointer 事件实现（不是 HTML5 DnD、也不是原生滚动），**浏览器不会替我们
// 滚动容器** ⇒ 指针停在容器上/下沿时，条目永远到不了可见窗口之外。
// 因此必须自己按指针位置驱动滚动：
//   · 指针进入距容器内沿 EDGE_ZONE_PX 的热区 → 开始 rAF 滚动，指针离开热区立即停；
//   · 速度随「到边缘的距离」线性渐变：热区外沿 SCROLL_MIN，贴到边缘 SCROLL_MAX；
//   · pointerup / pointercancel / 组件卸载都必须取消 rAF（不留循环泄漏）。
const EDGE_ZONE_PX = 24
const SCROLL_MIN_PX_PER_FRAME = 6
const SCROLL_MAX_PX_PER_FRAME = 20
/** 60Hz 一帧的毫秒数 —— 把「px/帧」标定到真实帧间隔（120Hz 屏不会滚成两倍速）。 */
const FRAME_MS = 16.7

// ─── 类型 ────────────────────────────────────────────────────────────

export interface StagingTrayProps {
  items: readonly QueueItemPayload[]
  busy: boolean
  onCancel: (msgID: string) => void
  onInterject: (msgID: string) => void
  onClear: () => void
  /** Commit a new queue order (msg ids, head first) after a drag. Omit to
   *  disable dragging entirely (read-only tray). */
  onReorder?: (msgIDs: string[]) => void
}

// ─── 子组件 ─────────────────────────────────────────────────────────

function QueueCard({
  item,
  index,
  isHead,
  busy,
  onCancel,
  onInterject,
  leaving,
  dragEnabled,
  dragging,
  dropEdge,
  handleProps,
}: {
  item: QueueItemPayload
  index: number
  isHead: boolean
  busy: boolean
  onCancel: (msgID: string) => void
  onInterject: (msgID: string) => void
  leaving: boolean
  dragEnabled: boolean
  dragging: boolean
  dropEdge: 'before' | 'after' | null
  /** Pointer handlers for the drag handle. The handle captures the pointer on
   *  pointerdown, so move/up/cancel are all retargeted to this element — no
   *  global window listeners are needed. */
  handleProps?: Pick<
    React.ComponentProps<'button'>,
    'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel'
  >
}) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const isNotification = item.source === 'notification' || item.source === 'resume'

  return (
    <div
      data-queue-id={item.msg_id || undefined}
      className={cn(
        'staging-card group relative rounded-lg border px-3 py-2 transition-colors',
        leaving ? 'staging-card-leave' : 'staging-card-enter',
        dragging && 'staging-card-dragging',
        isHead
          ? 'border-indigo-400/60 border-l-2 border-l-indigo-500 bg-indigo-500/[0.07] dark:border-indigo-500/50 dark:border-l-indigo-400'
          : 'border-border bg-bg-tertiary/40',
      )}
    >
      {/* 拖拽落点插入线（上半 = before，下半 = after） */}
      {dropEdge && (
        <div
          data-testid="staging-drop-line"
          className={cn(
            'staging-drop-line pointer-events-none absolute -left-1 -right-1',
            dropEdge === 'before' ? '-top-[3px]' : '-bottom-[3px]',
          )}
        >
          <span className="absolute -left-1 top-1/2 size-1.5 -translate-y-1/2 rounded-full bg-indigo-500" />
        </div>
      )}

      {/* 队首呼吸进度条 */}
      {isHead && busy && (
        <div className="absolute bottom-0 left-2 right-2 h-0.5 overflow-hidden rounded-full bg-indigo-500/10">
          <div className="staging-shimmer-bar h-full w-full rounded-full bg-gradient-to-r from-indigo-500/40 via-indigo-400 to-indigo-500/40" />
        </div>
      )}

      <div data-testid="staging-card-row" className="flex items-center gap-2.5">
        {/* 拖拽柄（可拖动时显示；触屏常显，桌面 hover 显示） */}
        {dragEnabled ? (
          <button
            type="button"
            data-testid="staging-drag-handle"
            aria-label={t('agent.staging.dragToReorder')}
            title={t('agent.staging.dragToReorder')}
            {...handleProps}
            className={cn(
              'staging-drag-handle -ml-1 flex size-5 shrink-0 cursor-grab touch-none items-center justify-center rounded text-text-muted/60 active:cursor-grabbing hover:bg-bg-tertiary hover:text-text-secondary',
              isTouch ? 'opacity-100' : 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100',
            )}
          >
            <GripVertical className="size-3.5" />
          </button>
        ) : null}

        {/* 序号 */}
        <div
          className={cn(
            'flex size-6 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold tabular-nums',
            isHead
              ? 'staging-glow-num bg-indigo-500 text-white'
              : 'bg-bg-tertiary text-text-muted',
          )}
        >
          {index + 1}
        </div>

        {/* 图标 */}
        <span className="shrink-0 text-text-secondary">
          {isNotification ? <Bell className="size-3.5" /> : <User className="size-3.5" />}
        </span>

        {/* preview 列 —— 队首「Next」徽章与预览文本同处一条 `flex items-center gap-*`
            行。徽章是容器的**第一个 in-flow 子元素**，容器自身无 padding，故
            badge.getBoundingClientRect().left === container.getBoundingClientRect().left
            完全由结构推导（不是 pl-* / 绝对定位凑出来的）。 */}
        <div data-testid="staging-card-preview" className="flex min-w-0 flex-1 items-center gap-1.5">
          {isHead && busy && (
            <span
              data-testid="staging-next-mark"
              className="shrink-0 rounded-full bg-indigo-500/15 px-1.5 py-px text-[10px] font-medium text-indigo-600 dark:bg-indigo-400/15 dark:text-indigo-300"
            >
              {t('agent.staging.next')}
            </span>
          )}
          <span className="min-w-0 truncate text-xs text-text-secondary">
            {item.preview || '(empty)'}
          </span>
        </div>

        {/* Turn N 标签 */}
        <span className="shrink-0 rounded bg-bg-tertiary/60 px-1.5 py-px font-mono text-[10px] text-text-muted">
          Turn {item.turn_id}
        </span>

        {/* hover/touch 操作（触屏始终可见，桌面 hover 显示） */}
        <div className={`staging-card-actions flex shrink-0 items-center gap-0.5 transition-opacity ${isTouch ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'}`}>
          {!isNotification && (
            <button
              type="button"
              aria-label={t('agent.staging.toInterject')}
              title={t('agent.staging.toInterjectTitle')}
              onClick={(e) => {
                e.stopPropagation()
                onInterject(item.msg_id)
              }}
              className="flex size-6 items-center justify-center rounded text-violet-500 hover:bg-violet-500/15 hover:text-violet-400"
            >
              <Zap className="size-3.5" />
            </button>
          )}
          <button
            type="button"
            aria-label={t('common.cancel')}
            title={t('agent.staging.cancelQueued')}
            onClick={(e) => {
              e.stopPropagation()
              onCancel(item.msg_id)
            }}
            className="flex size-6 items-center justify-center rounded text-text-muted hover:bg-destructive/10 hover:text-destructive"
          >
            <X className="size-3.5" />
          </button>
        </div>
      </div>
    </div>
  )
}

// ─── 主组件 ─────────────────────────────────────────────────────────

export const StagingTray = memo(function StagingTray({
  items = [],
  busy,
  onCancel,
  onInterject,
  onClear,
  onReorder,
}: StagingTrayProps) {
  const { t } = useI18n()
  injectStyles()

  const [leavingIDs, setLeavingIDs] = useState<Set<string>>(new Set())
  const [collapsed, setCollapsed] = useState(true)

  const handleCancel = useCallback((msgID: string) => {
    setLeavingIDs((prev) => new Set(prev).add(msgID))
    // 等动画完成再真正取消（让卡片滑出）
    setTimeout(() => {
      onCancel(msgID)
      setLeavingIDs((prev) => {
        const next = new Set(prev)
        next.delete(msgID)
        return next
      })
    }, 200)
  }, [onCancel])

  const handleClear = useCallback(() => {
    // 逐条触发 leave 动画
    const ids = items.map((i) => i.msg_id)
    setLeavingIDs(new Set(ids))
    setTimeout(() => {
      onClear()
      setLeavingIDs(new Set())
    }, 200)
  }, [items, onClear])

  // ── 拖动调序（pointer 事件：鼠标 / 触屏 / 触控笔通用）──
  // dragID 只驱动重渲染（抬升态）；落点存在 ref 里（pointermove 不触发
  // state 之外的副作用）。pointerdown 时对拖拽柄 setPointerCapture —— 之后
  // 该指针的 move/up/cancel 全部重定向到柄上（React 合成事件照收），因此
  // **不需要 window 监听器**（ESLint 禁止 per-session 代码监听全局 window 事件）。
  const [dragID, setDragID] = useState<string | null>(null)
  const [dropTarget, setDropTarget] = useState<{ id: string; before: boolean } | null>(null)
  const dropRef = useRef<{ id: string; before: boolean } | null>(null)
  const dragIDRef = useRef<string | null>(null)
  /** 列表滚动容器 —— 拖拽期间由 rAF 循环按指针位置滚动它。 */
  const listRef = useRef<HTMLDivElement | null>(null)
  /** 当前在跑的自动滚动 rAF 句柄（null = 没有循环在跑）。 */
  const rafRef = useRef<number | null>(null)
  /** 指针最后位置：容器滚动时指针可能一动没动，rAF tick 仍要知道它在哪。 */
  const pointerRef = useRef<{ x: number; y: number } | null>(null)

  /** 指针位置 → 落点（`elementFromPoint` + 卡片上下半判定）。
   *  pointermove **与**自动滚动 rAF tick 共用：容器滚动会让「指针下方是哪张卡」
   *  变化，指针没动也必须重算，否则落点停在滚动前的旧位置。 */
  const resolveDropTargetAt = useCallback((x: number, y: number) => {
    const drag = dragIDRef.current
    if (!drag) return
    const el = document.elementFromPoint(x, y)
    const card = (el as HTMLElement | null)?.closest?.('[data-queue-id]') as HTMLElement | null
    if (!card) return
    const id = card.getAttribute('data-queue-id')
    if (!id || id === drag) {
      if (dropRef.current) { dropRef.current = null; setDropTarget(null) }
      return
    }
    const rect = card.getBoundingClientRect()
    const before = y < rect.top + rect.height / 2
    const cur = dropRef.current
    if (cur && cur.id === id && cur.before === before) return
    dropRef.current = { id, before }
    setDropTarget({ id, before })
  }, [])

  /** 取消自动滚动循环（幂等）。pointerup / pointercancel / 离开热区 / 卸载都走这里。 */
  const stopAutoScroll = useCallback(() => {
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
  }, [])

  /** y 在哪个热区 + 该处速度：速度随「离内沿的距离」线性渐变（6→20px/帧）。 */
  const edgeScrollFor = useCallback((y: number): { dir: -1 | 0 | 1; speed: number } => {
    const list = listRef.current
    if (!list) return { dir: 0, speed: 0 }
    const rect = list.getBoundingClientRect()
    const ramp = (distFromEdge: number) =>
      SCROLL_MIN_PX_PER_FRAME +
      (SCROLL_MAX_PX_PER_FRAME - SCROLL_MIN_PX_PER_FRAME) *
        (1 - Math.min(Math.max(distFromEdge, 0), EDGE_ZONE_PX) / EDGE_ZONE_PX)
    if (y < rect.top + EDGE_ZONE_PX) return { dir: -1, speed: ramp(y - rect.top) }
    if (y > rect.bottom - EDGE_ZONE_PX) return { dir: 1, speed: ramp(rect.bottom - y) }
    return { dir: 0, speed: 0 }
  }, [])

  /** 启动自动滚动循环（已在跑则不重复启动）。 */
  const startAutoScroll = useCallback(() => {
    if (rafRef.current !== null) return
    let last = performance.now()
    const tick = (now: number) => {
      rafRef.current = null
      const list = listRef.current
      const p = pointerRef.current
      // 拖拽已结束 / 组件已卸载 / 没有指针位置 → 彻底停（不再排下一帧，无泄漏）
      if (!dragIDRef.current || !p || !list) return
      const { dir, speed } = edgeScrollFor(p.y)
      if (dir === 0) return
      const dt = Math.min(now - last, 50)
      const step = Math.max(1, Math.round(speed * (dt / FRAME_MS)))
      const prev = list.scrollTop
      list.scrollTop = prev + dir * step
      if (list.scrollTop !== prev) {
        // 几何变了 → 用**未移动的指针**重新解析落点（滚完后指针下方的卡已不同）
        resolveDropTargetAt(p.x, p.y)
      }
      // 只在「热区内 + 还能滚」时续帧：到顶/到底就自然停，不空转 rAF
      const max = list.scrollHeight - list.clientHeight
      const canScroll = dir === 1 ? list.scrollTop < max : list.scrollTop > 0
      if (canScroll) {
        last = now
        rafRef.current = requestAnimationFrame(tick)
      }
    }
    rafRef.current = requestAnimationFrame(tick)
  }, [edgeScrollFor, resolveDropTargetAt])

  const handleHandleDown = useCallback((e: React.PointerEvent, msgID: string) => {
    if (!onReorder || !msgID) return
    e.preventDefault()
    e.stopPropagation()
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      // capture 不可用（合成事件/老浏览器）—— 拖拽降级为"仅按下即取消"
    }
    stopAutoScroll()
    dragIDRef.current = msgID
    pointerRef.current = { x: e.clientX, y: e.clientY }
    dropRef.current = null
    setDropTarget(null)
    setDragID(msgID)
  }, [onReorder, stopAutoScroll])

  const handleDragMove = useCallback((e: React.PointerEvent) => {
    if (!dragIDRef.current) return
    pointerRef.current = { x: e.clientX, y: e.clientY }
    resolveDropTargetAt(e.clientX, e.clientY)
    // 进入上/下沿热区 → 持续滚动；离开热区 → 立即停（不残留 rAF）
    if (edgeScrollFor(e.clientY).dir === 0) stopAutoScroll()
    else startAutoScroll()
  }, [resolveDropTargetAt, edgeScrollFor, startAutoScroll, stopAutoScroll])

  const handleDragEnd = useCallback((commit: boolean) => {
    const drag = dragIDRef.current
    const target = dropRef.current
    // 任何一次结束（pointerup / pointercancel）都必须停掉自动滚动循环
    stopAutoScroll()
    pointerRef.current = null
    dragIDRef.current = null
    dropRef.current = null
    setDragID(null)
    setDropTarget(null)
    if (!drag || !commit || !target || !onReorder) return
    // computeReorder returns null when the drag is a no-op (dropped back in
    // place) — skip the RPC entirely then.
    const next = computeReorder(items.map((i) => i.msg_id), drag, target.id, target.before)
    if (next) onReorder(next)
  }, [items, onReorder, stopAutoScroll])

  // 防御性：拖拽途中面板被卸载（收起/切会话）也要取消 rAF，不留回调
  useEffect(() => stopAutoScroll, [stopAutoScroll])

  // 队列为空时不渲染任何 DOM（hooks must be called before early return — React rules-of-hooks）
  if (items.length === 0) return null

  return (
    <div data-testid="staging-tray" className="border-t border-border/50 bg-bg-primary px-3 py-1.5">
      {/* Header 行 —— 一行两个控件，动作互不重叠：
            · staging-toggle：**唯一**的展开/收起开关（Inbox + 标题 + 数量 +
              · 下一条 Turn + 唯一一个 chevron）。外层是 <div> 不是 <button>：
              嵌套 <button> 是无效 HTML 且会导致点击双触发（同 AskUserPanel 坑）。
            · staging-clear：清空队列（独立动作，Trash2 图标按钮，无文字，
              因此不会出现「收起」文案，也不与 chevron 的收起语义重复）。 */}
      <div data-testid="staging-header" className="flex w-full items-center justify-between gap-2">
        <button
          type="button"
          data-testid="staging-toggle"
          onClick={() => setCollapsed((v) => !v)}
          aria-expanded={!collapsed}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left text-xs text-text-muted"
        >
          <Inbox className="size-3.5 shrink-0" />
          <span className="truncate font-medium">
            {t('agent.staging.title')}
            <span className="ml-1 rounded-full bg-bg-tertiary/80 px-1.5 py-px text-[10px] tabular-nums">
              {items.length}
            </span>
          </span>
          <span className="shrink-0 text-[10px] text-text-muted/70">
            · {t('agent.staging.nextTurn', { turn: items[0].turn_id })}
          </span>
          {collapsed ? (
            <ChevronRight className="size-3 shrink-0 text-text-muted/50" />
          ) : (
            <ChevronDown className="size-3 shrink-0 text-text-muted/50" />
          )}
        </button>
        <button
          type="button"
          data-testid="staging-clear"
          aria-label={t('agent.staging.clearQueue')}
          title={t('agent.staging.clearQueue')}
          onClick={handleClear}
          className="flex size-6 shrink-0 items-center justify-center rounded text-text-muted/70 transition-colors hover:bg-destructive/10 hover:text-destructive"
        >
          <Trash2 className="size-3.5" />
        </button>
      </div>

      {/* 队列卡片列表 —— 唯一的「展开」就是全量渲染：长队列由容器**内部滚动**
          容纳（有界 max-h + overflow-y-auto + overscroll-contain），既不截断到
          N 条、也没有第二层「显示全部」（footer 已删除）。 */}
      {collapsed ? null : (
        <div
          data-testid="staging-list"
          ref={listRef}
          className="mt-1 flex max-h-[min(50vh,22rem)] flex-col gap-1 overflow-y-auto overscroll-contain"
        >
          {items.map((item, i) => (
            <QueueCard
              key={item.msg_id}
              item={item}
              index={i}
              isHead={i === 0}
              busy={busy}
              onCancel={handleCancel}
              onInterject={onInterject}
              leaving={leavingIDs.has(item.msg_id)}
              dragEnabled={Boolean(onReorder) && Boolean(item.msg_id)}
              dragging={dragID === item.msg_id}
              dropEdge={dropTarget?.id === item.msg_id ? (dropTarget.before ? 'before' : 'after') : null}
              handleProps={{
                onPointerDown: (e) => handleHandleDown(e, item.msg_id),
                onPointerMove: handleDragMove,
                onPointerUp: () => handleDragEnd(true),
                onPointerCancel: () => handleDragEnd(false),
              }}
            />
          ))}
        </div>
      )}
    </div>
  )
})
