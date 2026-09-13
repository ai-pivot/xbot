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
 * 拖动调序（2026-09-13 重做：从「只有一条插入线、布局不动」改成**松手前就实时重排**）：
 *   · 按下 → 卡片就地"挖空"成**同高占位槽**（占位槽就是原卡片元素本身，内容
 *     `visibility:hidden` 保住布局 ⇒ 列表总高不变），同时把卡片快照克隆成一张
 *     `position: fixed` 的**幽灵**跟指针走（`translate3d`，合成层不触发布局）；
 *   · 移动 → 命中的卡片（`elementFromPoint` + 中点判定，**同一份逻辑**同时决定预览
 *     与最终落点）直接算出**预览顺序**，列表按预览顺序渲染 ⇒ 其它卡片立刻让位；
 *   · 松手 → 提交预览顺序本身（不重算）；顺序没变则一个请求都不发（no-op 语义不变）。
 *   命中不到卡片（卡片间隙 / 面板外 / 指针正压在被拖项自己身上）→ **保持当前预览**
 *   而不是清空 ⇒ 不会出现"插入线一抖一抖"那种闪烁。
 *
 * 动画：CSS keyframes（fadeIn 入场、左滑淡出取消、队首呼吸进度条）。入场动画**只有
 * opacity**（不带 transform）：transform 会撑大滚动容器的 scrollable overflow，拖动时
 * 列表总高会被顶出 4~6px（详见 `<StagingTray>` 里 seenIDsRef 的注释）。
 */
import { memo, useState, useCallback, useEffect, useLayoutEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { Zap, X, Bell, ChevronDown, ChevronRight, Trash2, Inbox, User, GripVertical } from 'lucide-react'
import type { QueueItemPayload } from '@/types/shared'
import { cn } from '@/lib/utils'
import { computeReorder } from '@/lib/reorder'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'

// ─── CSS keyframes（注入一次，组件级 scope） ─────────────────────────
const STAGING_TRAY_STYLES = `
@keyframes stagingFadeIn {
  from { opacity: 0; }
  to   { opacity: 1; }
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
.staging-card-enter { animation: stagingFadeIn 0.2s ease-out forwards; }
.staging-card-leave { animation: stagingSlideOut 0.2s ease-out forwards; overflow: hidden; }
.staging-shimmer-bar { animation: stagingShimmer 1.8s ease-in-out infinite; }
.staging-glow-num { animation: stagingGlow 2s ease-in-out infinite; }

/* Drag-to-reorder（2026-09-13 重做）：
   · 幽灵 = 按下那一刻的卡片 DOM 快照，fixed + translate3d 跟指针（合成层，不触发
     布局）；定位/宽高/pointer-events 走 inline style（见组件），这里只管观感。
   · 占位槽 = 被拖卡片就地挖空后的样子（内容 visibility:hidden 保布局 ⇒ 列表总高不变），
     虚线 + 极淡底色，一眼看出"会被放到这里"。*/
.staging-drag-ghost {
  z-index: 60;
  will-change: transform;
  border-radius: 0.5rem;
  box-shadow: 0 16px 34px -12px rgba(15,23,42,0.55), 0 0 0 1px rgba(129,140,248,0.55);
}
.staging-drag-ghost-inner { height: 100%; }
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

/** 一次拖拽会话：被拖项的 id + 按下那一刻的指针/卡片几何（幽灵的定位基准）。 */
interface DragSession {
  id: string
  originX: number
  originY: number
  rect: { left: number; top: number; width: number; height: number }
}

/** 卡片 DOM 快照 → 幽灵的内容：剥掉 `data-testid` / `data-queue-id`（否则幽灵里的
 *  副本会和列表里的元素抢选择器），并去掉入场/退场动画类（否则幽灵会重播一次淡入，
 *  看起来像"闪一下"）。 */
function snapshotCard(cardEl: HTMLElement): string {
  const holder = document.createElement('div')
  holder.innerHTML = cardEl.outerHTML
  holder.querySelectorAll('[data-testid]').forEach((el) => el.removeAttribute('data-testid'))
  holder.querySelectorAll('[data-queue-id]').forEach((el) => el.removeAttribute('data-queue-id'))
  holder.firstElementChild?.classList.remove('staging-card-enter', 'staging-card-leave')
  return holder.innerHTML
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
  entering,
  dragEnabled,
  slot,
  handleProps,
}: {
  item: QueueItemPayload
  index: number
  isHead: boolean
  busy: boolean
  onCancel: (msgID: string) => void
  onInterject: (msgID: string) => void
  leaving: boolean
  /** 这条目**首次出现**（入场动画只在那一刻播一次；重排移动 DOM 不重播，见组件里
   *  `enteringIDs` 的注释）。 */
  entering: boolean
  dragEnabled: boolean
  /** 这张卡此刻正被拖起 —— 内容挖空（`visibility:hidden` 保住布局），就地留下一个
   *  同高的虚线**占位槽**。卡片元素本身不卸载：卡片会被重排，而 DOM move 会让浏览器
   *  释放指针捕获（捕获挂在列表容器上，见下）。 */
  slot: boolean
  /** 拖拽柄的按下处理。指针**捕获在列表容器**上（不是柄上）：柄所在的卡片会被重排
   *  （DOM move 会释放捕获），而列表容器整个拖拽期间既不卸载也不移动 ⇒ 不需要任何
   *  全局 window 监听器。 */
  handleProps?: Pick<React.ComponentProps<'button'>, 'onPointerDown'>
}) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const isNotification = item.source === 'notification' || item.source === 'resume'

  return (
    <div
      data-queue-id={item.msg_id || undefined}
      data-testid={slot ? 'staging-placeholder' : undefined}
      className={cn(
        'staging-card group relative rounded-lg border px-3 py-2 transition-colors',
        // 入场动画只在「首次出现」时挂（重排移动 DOM 不重播，见 seenIDsRef 注释）
        leaving ? 'staging-card-leave' : entering ? 'staging-card-enter' : '',
        // 占位槽：同一张卡就地挖空 —— 外框尺寸一字不改（总高不变，不跳），
        // 只留虚线 + 极淡底色，一眼看出"这里会被放下"。
        slot
          ? 'border-dashed border-indigo-400/70 bg-indigo-500/[0.06] dark:border-indigo-400/60'
          : isHead
            ? 'border-indigo-400/60 border-l-2 border-l-indigo-500 bg-indigo-500/[0.07] dark:border-indigo-500/50 dark:border-l-indigo-400'
            : 'border-border bg-bg-tertiary/40',
      )}
    >
      {/* 队首呼吸进度条（占位槽态不显示：槽里不该有进度条） */}
      {isHead && busy && !slot && (
        <div className="absolute bottom-0 left-2 right-2 h-0.5 overflow-hidden rounded-full bg-indigo-500/10">
          <div className="staging-shimmer-bar h-full w-full rounded-full bg-gradient-to-r from-indigo-500/40 via-indigo-400 to-indigo-500/40" />
        </div>
      )}

      {/* 占位槽态：内容 `visibility:hidden` —— **保留布局**（卡片高度一字不变），
          卡片元素仍留在列表里（顺序 = 预览顺序）⇒ 列表总高与滚动位置都不动。 */}
      <div
        data-testid="staging-card-row"
        className={cn('flex items-center gap-2.5', slot && 'invisible')}
      >
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
  /** 已经播过入场动画的 msg_id。
   *
   *  入场动画只在该条目**首次出现**时播一次 —— 这不是审美问题，是结构问题：
   *  React 因重排移动 DOM 节点（insertBefore）时浏览器会**重播** CSS 动画，
   *  于是拖动中"卡片闪一下"；更糟的是动画里的 transform 会把卡片盒子挪出容器，
   *  而 scrollable overflow **包含后代已变换的盒子** ⇒ 滚动容器的 scrollHeight
   *  被顶大 4~6px（实测 272 → 276），拖动时列表总高"跳一下"。
   *  掐掉重播（+ 入场动画已改成纯 opacity），列表总高在拖动全程恒定。 */
  const seenIDsRef = useRef<Set<string>>(new Set())

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
  //
  // 2026-09-13 重做：从「只有一条插入线、布局不动」改成「松手前就按指针位置实时重排」。
  //   · 按下 → 卡片就地挖空成同高占位槽 + 克隆一张 fixed 幽灵跟指针（跟手）；
  //   · 移动 → 命中卡片（elementFromPoint + 中点判定）直接算出**预览顺序**，
  //     列表按预览顺序渲染 ⇒ 其它卡片立刻让位（松手前就能预判结果）；
  //   · 松手 → 提交预览顺序本身（不再重算一遍），顺序没变则一个请求都不发。
  //
  // 指针捕获挂在**列表容器**上，不是卡片里的柄：卡片会被重排，而 DOM move 会让浏览器
  // 释放指针捕获 ⇒ 柄一旦被移动，move/up 就再也收不到（拖拽"中途失灵"）。列表容器
  // 整个拖拽期间既不卸载也不移动，捕获稳定；move/up/cancel 全部重定向到它 ⇒
  // **不需要任何全局 window 监听器**（ESLint 亦禁止 per-session 代码监听 window）。
  const [session, setSession] = useState<DragSession | null>(null)
  /** 列表渲染顺序（拖拽中 = 预览顺序；提交后**暂留**到权威快照回来，见 handleDragEnd）。 */
  const [pendingOrder, setPendingOrder] = useState<string[] | null>(null)
  const sessionRef = useRef<DragSession | null>(null)
  /** 当前渲染/拖拽顺序（`pendingOrder` 的 ref 镜像，事件回调里读它避免闭包过期）。 */
  const orderRef = useRef<string[] | null>(null)
  /** 本次拖拽的基准顺序（= 按下那一刻列表渲染的顺序）：用来判「是不是白拖了」，
   *  也用来判「队列快照是否已经变了」（对账 effect）。提交后**继续保留**，直到 props
   *  真的换了一份快照 —— 否则松手瞬间会闪回旧顺序（松手前后不一致 = 用户看到的"跳"）。 */
  const initialOrderRef = useRef<string[] | null>(null)
  /** 被拖卡片的 DOM 快照（已剥掉 testid / data-queue-id）—— 幽灵的内容。 */
  const ghostHTMLRef = useRef('')
  const ghostRef = useRef<HTMLDivElement | null>(null)
  const ghostInnerRef = useRef<HTMLDivElement | null>(null)
  /** 列表滚动容器 —— 拖拽期间由 rAF 循环按指针位置滚动它。 */
  const listRef = useRef<HTMLDivElement | null>(null)
  /** 当前在跑的自动滚动 rAF 句柄（null = 没有循环在跑）。 */
  const rafRef = useRef<number | null>(null)
  /** 指针最后位置：容器滚动时指针可能一动没动，rAF tick 仍要知道它在哪。 */
  const pointerRef = useRef<{ x: number; y: number } | null>(null)

  /** 指针位置 → **预览顺序**（`elementFromPoint` + 卡片上下半判定）。
   *  pointermove **与**自动滚动 rAF tick 共用同一份命中逻辑：容器滚动会让「指针下方
   *  是哪张卡」变化，指针没动也必须重算，否则预览停在滚动前的旧位置。
   *  命中不到卡片（卡片之间的间隙 / 面板之外 / 指针正压在被拖项自己的占位槽上）→
   *  **保持当前预览**而不是清空 —— 旧实现此时会清掉落点，插入线来回跳，用户看不懂。*/
  const updatePreviewAt = useCallback((x: number, y: number) => {
    const drag = sessionRef.current
    const cur = orderRef.current
    if (!drag || !cur) return
    const el = document.elementFromPoint(x, y)
    const card = (el as HTMLElement | null)?.closest?.('[data-queue-id]') as HTMLElement | null
    const targetID = card?.getAttribute('data-queue-id') ?? null
    if (!card || !targetID || targetID === drag.id) return
    const rect = card.getBoundingClientRect()
    const before = y < rect.top + rect.height / 2
    // 预览顺序与最终落点用**同一个** computeReorder：松手提交的就是这里算出来的。
    const next = computeReorder(cur, drag.id, targetID, before) ?? cur
    if (next === cur) return
    orderRef.current = next
    setPendingOrder(next)
  }, [])

  /** 幽灵跟手：只写 transform（合成层，不触发布局、也不触发 React 重渲染）。
   *  `left/top/width/height` 在按下那一刻定死，之后只做位移 ⇒ 与指针的相对位置
   *  （抓手偏移）全程不变，不会"跳一下"。
   *
   *  **只跟纵向**（x 分量恒为 0）：这是一维纵向列表的重排，横向跟手会让幽灵被拖出
   *  列表所在的那一列（手机上 390px 宽直接出屏被裁掉），而且会和占位槽错开成两个
   *  并排的盒子 —— 用户就看不出"它会落在哪"。纵向跟手时幽灵与占位槽天然同一列。 */
  const moveGhostTo = useCallback((_x: number, y: number) => {
    const s = sessionRef.current
    const el = ghostRef.current
    if (!s || !el) return
    el.style.transform = `translate3d(0, ${y - s.originY}px, 0) scale(1.03)`
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
      if (!sessionRef.current || !p || !list) return
      const { dir, speed } = edgeScrollFor(p.y)
      if (dir === 0) return
      const dt = Math.min(now - last, 50)
      const step = Math.max(1, Math.round(speed * (dt / FRAME_MS)))
      const prev = list.scrollTop
      list.scrollTop = prev + dir * step
      if (list.scrollTop !== prev) {
        // 几何变了 → 用**未移动的指针**重新解析落点/预览（滚完后指针下方的卡已不同）
        updatePreviewAt(p.x, p.y)
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
  }, [edgeScrollFor, updatePreviewAt])

  const handleHandleDown = useCallback((e: React.PointerEvent, msgID: string) => {
    if (!onReorder || !msgID) return
    e.preventDefault()
    e.stopPropagation()
    const cardEl = e.currentTarget.closest('[data-queue-id]') as HTMLElement | null
    const rect = cardEl?.getBoundingClientRect()
    const list = listRef.current
    if (!cardEl || !rect || !list) return
    try {
      // 捕获在列表容器上（理由见上面的注释）——之后本指针的 move/up/cancel 全部
      // 重定向到列表，卡片怎么重排都不影响拖拽。
      list.setPointerCapture(e.pointerId)
    } catch {
      // capture 不可用（合成事件/老浏览器）——事件仍会冒泡到列表，拖拽照常降级工作
    }
    stopAutoScroll()
    // 基准 = **此刻列表渲染的顺序**（可能是上一次拖动提交、服务端快照还没回来的顺序）
    const order = orderRef.current ?? items.map((i) => i.msg_id)
    const next: DragSession = {
      id: msgID,
      originX: e.clientX,
      originY: e.clientY,
      rect: { left: rect.left, top: rect.top, width: rect.width, height: rect.height },
    }
    sessionRef.current = next
    orderRef.current = order
    initialOrderRef.current = order
    ghostHTMLRef.current = snapshotCard(cardEl)
    pointerRef.current = { x: e.clientX, y: e.clientY }
    setSession(next)
    setPendingOrder(order)
  }, [items, onReorder, stopAutoScroll])

  const handleDragMove = useCallback((e: React.PointerEvent) => {
    if (!sessionRef.current) return
    pointerRef.current = { x: e.clientX, y: e.clientY }
    moveGhostTo(e.clientX, e.clientY)
    updatePreviewAt(e.clientX, e.clientY)
    // 进入上/下沿热区 → 持续滚动；离开热区 → 立即停（不残留 rAF）
    if (edgeScrollFor(e.clientY).dir === 0) stopAutoScroll()
    else startAutoScroll()
  }, [moveGhostTo, updatePreviewAt, edgeScrollFor, startAutoScroll, stopAutoScroll])

  const handleDragEnd = useCallback((commit: boolean) => {
    const s = sessionRef.current
    const order = orderRef.current
    const initial = initialOrderRef.current
    // 任何一次结束（pointerup / pointercancel）都必须停掉自动滚动循环
    stopAutoScroll()
    sessionRef.current = null
    ghostHTMLRef.current = ''
    pointerRef.current = null
    setSession(null)
    const changed = Boolean(order && initial && order.join(' ') !== initial.join(' '))
    if (!s || !commit || !changed || !order || !onReorder) {
      // 没戏了（取消 / 白拖 / 只读面板）→ 渲染权立刻交还队列 props。
      // 顺序与基准一致 ⇒ 渲染结果不变，不会闪。
      orderRef.current = null
      initialOrderRef.current = null
      setPendingOrder(null)
      return
    }
    // 真提交了变化：**先留着这份顺序继续渲染**，等服务端权威快照（props）回来再交还
    // （见下面的对账 effect）。否则松手瞬间会闪回旧顺序 —— 那正是"松手后跳一下"的来源。
    onReorder(order)
  }, [onReorder, stopAutoScroll])

  // 防御性：拖拽途中面板被卸载（收起/切会话）也要取消 rAF，不留回调
  useEffect(() => stopAutoScroll, [stopAutoScroll])

  // 队列快照（props）变化时的对账 —— 两种情形走同一条出路：
  //   · 拖拽中：队列被外部改写（并发 dequeue / 对账快照）⇒ 作废本次拖拽，别提交脏顺序；
  //   · 已松手：服务端权威顺序到位 ⇒ 把渲染权交还 props（提交顺序与之一致 ⇒ 不闪）。
  // 只比**内容**（join）不比数组引用：拖拽途中父组件因流式渲染重建 items 是常态，
  // 引用一变就交还会把预览打回原形。
  useEffect(() => {
    const baseline = initialOrderRef.current
    if (!baseline) return
    if (items.map((i) => i.msg_id).join(' ') === baseline.join(' ')) return
    handleDragEnd(false)
  }, [items, handleDragEnd])

  // 幽灵内容 = 按下那一刻的卡片快照（layout effect：不留一帧空壳造成闪烁）
  useLayoutEffect(() => {
    if (!session || !ghostInnerRef.current) return
    ghostInnerRef.current.innerHTML = ghostHTMLRef.current
  }, [session])

  // 记下已经出现过的条目（下一帧起不再是「首次出现」⇒ 不再挂入场动画类）
  useEffect(() => {
    for (const it of items) if (it.msg_id) seenIDsRef.current.add(it.msg_id)
  }, [items])

  // 列表渲染顺序：拖拽中 = 预览顺序（松手前就重排），提交后暂留到权威快照回来。
  const renderOrder = pendingOrder ?? items.map((i) => i.msg_id)
  const byID = new Map(items.map((i) => [i.msg_id, i]))
  // 本帧「首次出现」的条目 → 才配入场动画（重排/让位不配，见 seenIDsRef 注释）。
  const enteringIDs = new Set<string>()
  for (const it of items) if (it.msg_id && !seenIDsRef.current.has(it.msg_id)) enteringIDs.add(it.msg_id)

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
          N 条、也没有第二层「显示全部」（footer 已删除）。

          拖拽期间按**预览顺序**渲染（松手前就重排）；move/up/cancel 挂在容器上，
          因为指针捕获就挂在容器上（卡片会被重排，柄上捕获会丢）。 */}
      {collapsed ? null : (
        <div
          data-testid="staging-list"
          ref={listRef}
          onPointerMove={handleDragMove}
          onPointerUp={() => handleDragEnd(true)}
          onPointerCancel={() => handleDragEnd(false)}
          className={cn(
            'mt-1 flex max-h-[min(50vh,22rem)] flex-col gap-1 overflow-y-auto overscroll-contain',
            session && 'cursor-grabbing',
          )}
        >
          {renderOrder.map((id, i) => {
            const item = byID.get(id)
            if (!item) return null
            return (
              <QueueCard
                key={id}
                item={item}
                index={i}
                isHead={i === 0}
                busy={busy}
                onCancel={handleCancel}
                onInterject={onInterject}
                leaving={leavingIDs.has(id)}
                entering={enteringIDs.has(id)}
                dragEnabled={Boolean(onReorder) && Boolean(id)}
                slot={session?.id === id}
                handleProps={{ onPointerDown: (e) => handleHandleDown(e, id) }}
              />
            )
          })}
        </div>
      )}

      {/* 幽灵：按下那一刻的卡片快照，fixed 跟指针（transform 走合成层，不触发布局）。
          portal 到 body ⇒ 不被列表的滚动容器裁剪、不参与列表布局（列表总高不变）、
          也不参与命中判定（pointer-events: none），与占位槽互不干扰。 */}
      {session
        ? createPortal(
            <div
              ref={ghostRef}
              data-testid="staging-drag-ghost"
              aria-hidden="true"
              className="staging-drag-ghost staging-card"
              style={{
                position: 'fixed',
                pointerEvents: 'none',
                left: session.rect.left,
                top: session.rect.top,
                width: session.rect.width,
                height: session.rect.height,
              }}
            >
              <div ref={ghostInnerRef} className="staging-drag-ghost-inner" />
            </div>,
            document.body,
          )
        : null}
    </div>
  )
})
