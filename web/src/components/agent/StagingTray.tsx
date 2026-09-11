/**
 * StagingTray — 待发队列托盘（排在 MessageList 和 MessageInput 之间）。
 *
 * 数据源：ChatStore state.queue（queue_state SSE 事件全量替换语义）。
 * 队列为空时不渲染任何 DOM（height 0）。
 *
 * 卡片设计：
 *   - 队首：indigo accent 边框 + 发光序号 + 底部呼吸进度条 + "下一个" 标签
 *   - 非队首：muted 边框 + 普通序号
 *   - 🔔 通知项：只显示 ✕（不可转插话）
 *   - >3 条自动折叠
 *
 * 动画：CSS keyframes（fadeUp 入场、左滑淡出取消、队首呼吸进度条）
 */
import { memo, useState, useCallback, useRef } from 'react'
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
          ? 'border-indigo-400/60 bg-indigo-500/[0.07] dark:border-indigo-500/50'
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

      <div className="flex items-center gap-2.5">
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

        {/* preview 文本 */}
        <span className="min-w-0 flex-1 truncate text-xs text-text-secondary">
          {item.preview || '(empty)'}
        </span>

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

      {/* 队首 "下一个" 标签 */}
      {isHead && busy && (
        <div className="mt-1 pl-8.5 text-[10px] font-medium text-indigo-500/80 dark:text-indigo-400/80">
          ▸ {t('agent.staging.next')}
        </div>
      )}
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

  const [expanded, setExpanded] = useState(false)
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

  const handleHandleDown = useCallback((e: React.PointerEvent, msgID: string) => {
    if (!onReorder || !msgID) return
    e.preventDefault()
    e.stopPropagation()
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      // capture 不可用（合成事件/老浏览器）—— 拖拽降级为"仅按下即取消"
    }
    dragIDRef.current = msgID
    dropRef.current = null
    setDropTarget(null)
    setDragID(msgID)
  }, [onReorder])

  const handleDragMove = useCallback((e: React.PointerEvent) => {
    const drag = dragIDRef.current
    if (!drag) return
    const el = document.elementFromPoint(e.clientX, e.clientY)
    const card = (el as HTMLElement | null)?.closest?.('[data-queue-id]') as HTMLElement | null
    if (!card) return
    const id = card.getAttribute('data-queue-id')
    if (!id || id === drag) {
      if (dropRef.current) { dropRef.current = null; setDropTarget(null) }
      return
    }
    const rect = card.getBoundingClientRect()
    const before = e.clientY < rect.top + rect.height / 2
    const cur = dropRef.current
    if (cur && cur.id === id && cur.before === before) return
    dropRef.current = { id, before }
    setDropTarget({ id, before })
  }, [])

  const handleDragEnd = useCallback((commit: boolean) => {
    const drag = dragIDRef.current
    const target = dropRef.current
    dragIDRef.current = null
    dropRef.current = null
    setDragID(null)
    setDropTarget(null)
    if (!drag || !commit || !target || !onReorder) return
    // computeReorder returns null when the drag is a no-op (dropped back in
    // place) — skip the RPC entirely then.
    const next = computeReorder(items.map((i) => i.msg_id), drag, target.id, target.before)
    if (next) onReorder(next)
  }, [items, onReorder])

  // 队列为空时不渲染任何 DOM（hooks must be called before early return — React rules-of-hooks）
  if (items.length === 0) return null

  const MAX_VISIBLE = 3
  const hasOverflow = items.length > MAX_VISIBLE
  const visibleItems = expanded || !hasOverflow ? items : items.slice(0, MAX_VISIBLE)
  const hiddenCount = items.length - visibleItems.length

  return (
    <div data-testid="staging-tray" className="border-t border-border/50 bg-bg-primary px-3 py-1.5">
      {/* Header 行 — 可折叠（默认折叠，只显示 count bar）
          ⚠️ 外层必须是 <div>，不能是 <button>：折叠开关与「收起列表」是两个
          不同动作，嵌套 <button> 是无效 HTML 且点击会双触发（同 AskUserPanel
          的嵌套 checkbox 坑）。状态 chevron 只保留一个。 */}
      <div className="flex w-full items-center justify-between gap-2">
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
          {items.length > 0 && (
            <span className="shrink-0 text-[10px] text-text-muted/70">
              · {t('agent.staging.nextTurn', { turn: items[0].turn_id })}
            </span>
          )}
          {collapsed ? (
            <ChevronRight className="size-3 shrink-0 text-text-muted/50" />
          ) : (
            <ChevronDown className="size-3 shrink-0 text-text-muted/50" />
          )}
        </button>
        {expanded ? (
          <button
            type="button"
            aria-label={t('agent.staging.collapse')}
            title={t('agent.staging.collapse')}
            onClick={() => setExpanded(false)}
            className="flex shrink-0 items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-text-muted/70 transition-colors hover:bg-bg-tertiary hover:text-text-secondary"
          >
            {t('agent.staging.collapse')}
            <ChevronDown className="size-3" />
          </button>
        ) : null}
      </div>

      {/* 队列卡片列表（折叠时不渲染） */}
      {collapsed ? null : (
        <>
          <div className="mt-1 flex flex-col gap-1">
            {visibleItems.map((item, i) => (
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

          {/* 折叠展开按钮 */}
          {hasOverflow && (
            <button
              type="button"
              onClick={() => setExpanded((v) => !v)}
              className="mt-1 flex items-center gap-1 text-[11px] text-text-muted/70 transition-colors hover:text-text-secondary"
            >
              {expanded ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
              {expanded
                ? t('agent.staging.collapse')
                : t('agent.staging.more', { count: hiddenCount })}
            </button>
          )}

          {/* 清空按钮 */}
          <div className="mt-1 flex justify-end">
            <button
              type="button"
              aria-label={t('agent.staging.clearQueue')}
              title={t('agent.staging.clearQueue')}
              onClick={handleClear}
              className="flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] text-text-muted/70 transition-colors hover:bg-destructive/10 hover:text-destructive"
            >
              <Trash2 className="size-3" />
              {t('agent.staging.clear')}
            </button>
          </div>
        </>
      )}
    </div>
  )
})
