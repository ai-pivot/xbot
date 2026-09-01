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
import { memo, useState, useCallback } from 'react'
import { Zap, X, Bell, ChevronDown, ChevronRight, Trash2, Inbox } from 'lucide-react'
import type { QueueItemPayload } from '@/types/shared'
import { cn } from '@/lib/utils'

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
}: {
  item: QueueItemPayload
  index: number
  isHead: boolean
  busy: boolean
  onCancel: (msgID: string) => void
  onInterject: (msgID: string) => void
  leaving: boolean
}) {
  const isNotification = item.source === 'notification' || item.source === 'resume'

  return (
    <div
      className={cn(
        'group relative rounded-lg border px-3 py-2 transition-colors',
        leaving ? 'staging-card-leave' : 'staging-card-enter',
        isHead
          ? 'border-indigo-400/60 bg-indigo-500/[0.07] dark:border-indigo-500/50'
          : 'border-border bg-bg-tertiary/40',
      )}
    >
      {/* 队首呼吸进度条 */}
      {isHead && busy && (
        <div className="absolute bottom-0 left-2 right-2 h-0.5 overflow-hidden rounded-full bg-indigo-500/10">
          <div className="staging-shimmer-bar h-full w-full rounded-full bg-gradient-to-r from-indigo-500/40 via-indigo-400 to-indigo-500/40" />
        </div>
      )}

      <div className="flex items-center gap-2.5">
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
          {isNotification ? <Bell className="size-3.5" /> : <span className="text-[13px]">👤</span>}
        </span>

        {/* preview 文本 */}
        <span className="min-w-0 flex-1 truncate text-xs text-text-secondary">
          {item.preview || '(empty)'}
        </span>

        {/* Turn N 标签 */}
        <span className="shrink-0 rounded bg-bg-tertiary/60 px-1.5 py-px font-mono text-[10px] text-text-muted">
          Turn {item.turn_id}
        </span>

        {/* hover 操作 */}
        <div className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
          {!isNotification && (
            <button
              type="button"
              aria-label="转插话"
              title="⚡ 转为插话（立即注入当前 Turn）"
              onClick={(e) => {
                e.stopPropagation()
                onInterject(item.msg_id)
              }}
              className="flex size-5.5 items-center justify-center rounded text-violet-500 hover:bg-violet-500/15 hover:text-violet-400"
            >
              <Zap className="size-3.5" />
            </button>
          )}
          <button
            type="button"
            aria-label="取消"
            title="取消排队"
            onClick={(e) => {
              e.stopPropagation()
              onCancel(item.msg_id)
            }}
            className="flex size-5.5 items-center justify-center rounded text-text-muted hover:bg-destructive/10 hover:text-destructive"
          >
            <X className="size-3.5" />
          </button>
        </div>
      </div>

      {/* 队首 "下一个" 标签 */}
      {isHead && busy && (
        <div className="mt-1 pl-8.5 text-[10px] font-medium text-indigo-500/80 dark:text-indigo-400/80">
          ▸ 下一个执行
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
}: StagingTrayProps) {
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

  // 队列为空时不渲染任何 DOM（hooks must be called before early return — React rules-of-hooks）
  if (items.length === 0) return null

  const MAX_VISIBLE = 3
  const hasOverflow = items.length > MAX_VISIBLE
  const visibleItems = expanded || !hasOverflow ? items : items.slice(0, MAX_VISIBLE)
  const hiddenCount = items.length - visibleItems.length

  return (
    <div className="border-t border-border/50 px-3 py-1.5">
      {/* Header 行 — 可折叠（默认折叠，只显示 count bar） */}
      <button
        type="button"
        onClick={() => setCollapsed((v) => !v)}
        className="flex w-full items-center justify-between gap-2 text-left"
      >
        <div className="flex items-center gap-1.5 text-xs text-text-muted">
          <Inbox className="size-3.5 shrink-0" />
          <span className="font-medium">
            📨 待发队列
            <span className="ml-1 rounded-full bg-bg-tertiary/80 px-1.5 py-px text-[10px] tabular-nums">
              {items.length}
            </span>
          </span>
          {items.length > 0 && (
            <span className="text-[10px] text-text-muted/70">
              · 下一条 Turn {items[0].turn_id}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          {expanded ? (
            <button
              type="button"
              aria-label="收起"
              onClick={(e) => { e.stopPropagation(); setExpanded(false) }}
              className="flex items-center gap-0.5 rounded px-1 py-0.5 text-[10px] text-text-muted/70 transition-colors hover:bg-bg-tertiary hover:text-text-secondary"
            >
              <ChevronDown className="size-3" />
            </button>
          ) : null}
          {collapsed ? (
            <ChevronRight className="size-3 text-text-muted/50" />
          ) : (
            <ChevronDown className="size-3 text-text-muted/50" />
          )}
        </div>
      </button>

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
                ? '收起'
                : `还有 ${hiddenCount} 条`}
            </button>
          )}

          {/* 清空按钮 */}
          <div className="mt-1 flex justify-end">
            <button
              type="button"
              aria-label="清空队列"
              title="清空队列"
              onClick={handleClear}
              className="flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] text-text-muted/70 transition-colors hover:bg-destructive/10 hover:text-destructive"
            >
              <Trash2 className="size-3" />
              清空
            </button>
          </div>
        </>
      )}
    </div>
  )
})
