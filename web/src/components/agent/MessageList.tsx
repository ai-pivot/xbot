/**
 * MessageList — virtualized chat message list (Spec A §3+§4).
 *
 * Rewritten scroll logic with strict user-intent priority:
 *   - `stickToBottomRef` controls auto-follow; once false, no content increments
 *     trigger auto-scroll.
 *   - One cancellable RAF coalesces all application-level bottom scrolling.
 *   - Bottom "↓ new content" bubble appears while follow mode is paused.
 *   - Right-side floating navigation button group (top/prev-user/next-user/bottom).
 *
 * Uses @tanstack/react-virtual with dynamic measurement. The committed list
 * comes from useChatMessages; a single live streaming message is appended as
 * the last row when present.
 */
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  useVirtualizer,
  observeElementOffset as defaultObserveElementOffset,
  observeElementRect as defaultObserveElementRect,
  measureElement as defaultMeasureElement,
} from '@tanstack/react-virtual'
import {
  createHeightAwareMeasureElement,
  createRowHeightMemory,
  createWidthTracker,
  rowSignature,
} from './rowHeightMemory'
import { AnimatePresence, motion } from 'framer-motion'
import { ChevronRight, Loader2, Sparkles } from 'lucide-react'

import { MessageItem } from './MessageItem'
import { MessageUserNav } from './MessageUserNav'
import { ShimmerThinking } from './ShimmerThinking'
import { bindTurnIDs, orderMessageRows } from './messageOrder'
import { useI18n } from '@/providers/i18n'
import { commands } from '@/lib/commandRouter'
import { frameScheduler } from '@/lib/frameScheduler'
import type { ChatMessage, LiveProgress } from '@/types/agent'

interface MessageListProps {
  /** Stable chat/session identity; changing it forces initial scroll to bottom. */
  chatKey?: string | null
  /** Increment to force TUI-style follow mode after local user actions. */
  followResetToken?: number
  messages: ChatMessage[]
  /** Live progress snapshot handed only to the streaming row (方案 A：live 行
   *  已在 messages 里，liveId 匹配 isPartial 行）。 */
  liveProgress: LiveProgress | null
  /** Whether the agent is busy (thinking/processing) — shows placeholder when
   *  no live row yet (e.g. session just started, no iterations arrived). */
  busy?: boolean
  loading: boolean
  /** True while loading older messages (scroll-up pagination). */
  loadingMore?: boolean
  /** True if there are older messages available to load. */
  hasMore?: boolean
  /** Called when the user scrolls to the top — load older messages. */
  onLoadMore?: () => Promise<boolean>
  error: string | null
  /** Rewind callback — receives the edited content string. */
  onRewind?: (editedContent: string, originalMessage: ChatMessage) => void
  /** ID of the message currently being edited, or null. */
  editingMessageId?: string | null
  /** Callback to start editing a message. */
  onStartEdit?: (messageId: string) => void
  /** Callback to end editing (cancel or confirm). */
  onEndEdit?: () => void
  /** Optional footer rendered after the message list (e.g. AskUserPanel). */
  footer?: ReactNode
}

const ESTIMATE = 120
// GenUI 行（顶层面板）：内容高度是动态的（createRoot 渲染的 UI + max-h-[70vh]
// 容器，图片/图表/折叠展开都会改变高度）。禁止任何"测一次固化"缓存 —— 固化后
// 内容长高时 virtualizer 仍按旧尺寸 translateY 定位，后续行与 GenUI 面板重叠
// （2026-08-28 文字重叠事故）。统一走 measureElement（内部 ResizeObserver 持续
// 跟踪高度变化并 resizeItem），estimate 只需给接近的初值。
const GENUI_PANEL_ESTIMATE = 560
const EDGE_EPSILON = 2
/**
 * 「钉到底」用的超界 `top`：跟随底部时写 `el.scrollTo({ top: SCROLL_PIN_MAX })`，
 * 浏览器自行 clamp 到真实底部 —— **无需读 `scrollHeight`**（读它紧跟 React 提交会触发
 * 强制同步布局，2026-09-18 生产 trace 实测该点 27% CPU）。任何真实内容高度都远小于它。
 */
const SCROLL_PIN_MAX = 1e9

// ── 历史高度抖动根治：高度记忆 + 内容感知估算 ──────────────────────────────
// 抖动机制：历史加载时 estimateSize 返回常数（120），而实际高度 200–900px →
// TanStack 逐行 measureElement 修正 → 行位置级联跳动。两层修复（都不跳过测量，
// ResizeObserver 仍持续跟踪，与已删除的"固化"缓存本质不同）：
//   1. heightMemory：module 级 Map<rowKey, 实测高度>。measureElement 时记录
//      实测值；下次同一行 estimate 直接命中记忆值 → 二次加载零修正、零抖动。
//      记忆的是初值，不是固化 —— 高度变化仍由 ResizeObserver 修正。
//   2. estimateRowByContent：首次访问（无记忆）时按内容长度/迭代数/工具数粗估，
//      比常数 120 的误差缩小数倍。
const heightMemory = createRowHeightMemory()
/** 布局宽度追踪：宽度变化 ⇒ 行高记忆作废（高度不变性的前提）。 */
const heightLayoutWidth = createWidthTracker()
heightLayoutWidth.onChange(() => heightMemory.clear())

/**
 * 记忆感知的容器几何：照常忽略"没有布局的测量"（0×0），额外记录布局宽度 ——
 * 宽度一变就清空行高记忆（否则旧宽度的行高会被当成新宽度的高度）。
 */
const widthAwareObserveElementRect: typeof defaultObserveElementRect = (instance, cb) =>
  nonDegenerateObserveElementRect(instance, (rect) => {
    heightLayoutWidth.observe(rect.width)
    cb(rect)
  })

/** 与 getItemKey 相同的稳定行键（turn-N-role / row.id）。 */
function rowMemoryKey(row: ChatMessage, index: number): string {
  if (row.turnID > 0 && row.turnID < Number.MAX_SAFE_INTEGER) {
    return `turn-${row.turnID}-${row.role}`
  }
  return row.id ?? `row-${index}`
}

/** 首次访问（无记忆）时的内容感知估算：量级正确即可，精度由实测修正。 */
/**
 * 行高估算 —— **按 row 对象记忆化**（WeakMap）。
 *
 * ⚠️ 为什么必须记忆化（2026-09-13「加载的历史消息长了就卡」根治）：
 * TanStack 每次重算 offsets 都会为**尚未实测的每一行**调用 `estimateSize` →
 * `estimateRowByContent`。函数体对 assistant row **两次遍历该行所有迭代**
 * （tools + iterLen）→ 每帧总代价 = **O(已加载的全部迭代数)**，与用户两条观察
 * 完全吻合（busy 长 turn 卡；loadMore 拉长历史后同样卡；新 turn 很小也卡）。
 * 未变化的 row 对象身份稳定（integrate/derive 的恒等复用），WeakMap 命中即 O(1)。
 */
const estimateCache = new WeakMap<ChatMessage, number>()
/** 测试钩子：统计真正的计算次数（记忆化命中不计）。 */
export const __estimateRowByContentComputeCount = { value: 0 }

export function estimateRowByContent(row: ChatMessage): number {
  const cached = estimateCache.get(row)
  if (cached !== undefined) return cached
  __estimateRowByContentComputeCount.value++
  let result: number
  if (row.role === 'user') {
    const len = (row.content || '').length
    result = Math.min(Math.max(52 + Math.ceil(len / 60) * 19, 60), 400)
  } else {
    const iters = row.iterations ?? []
    // 单趟遍历同时累加 tools / iterLen（旧实现两趟 reduce + 每趟遍历全迭代）。
    let tools = 0
    let iterLen = 0
    for (const it of iters) {
      tools += it.tools?.length ?? 0
      iterLen += (it.content?.length ?? 0) + (it.reasoning?.length ?? 0)
    }
    // 高度估算必须计入 iteration 的 content/reasoning（展开思考后巨块的主体，
    // iteration_count × thinking 字段不在 row.content 里）。宁可高估：overscan
    // 覆盖的像素提前量随 estimate 增大，大行在滚到视口前就完成 mount；
    // 实测后 heightMemory 覆盖估算。
    const len = (row.content || '').length + iterLen
    const lines = Math.ceil(len / 90) || 1
    // 估算**只作未渲染行的初值提示**（渲染中的行一律以浏览器实测为准 —— 见
    // createHeightAwareMeasureElement 的根因修复）。⛔ 不得再按元素类型写特判：
    // 任何"高度与字符数不成比例"的元素（表格 / 代码块 / mermaid / 图片 / KaTeX /
    // 嵌套列表…）都会被低估，逐类型打补丁永远追不上。
    result = Math.min(Math.max(70 + lines * 21 + iters.length * 34 + Math.ceil(tools / 4) * 20, 140), 6000)
  }
  estimateCache.set(row, result)
  return result
}

// ── scroll → rAF 合帧（2026-08-29 滚动掉帧根治，Trace-20260829T181624）──────
// TanStack 默认 observeElementOffset 在每个 scroll 事件里同步 cb（onChange →
// virtualizer 内部 setState）。两个放大器让它在高刷屏 + 大 reasoning 行下
// 变成掉帧主源：
//   1) scroll/wheel 事件频率 = 合成器滚动事件率（120-135Hz），每次都触发
//      React render —— 新行 mount 的大 commit（remark parse + KaTeX DOM +
//      measureElement 强制全树 style recalc）83-110ms 直接阻塞滚动关键帧。
//   2) wheel 的 React discrete 事件窗口内，同帧的 scroll listener setState
//      被提升为 sync flush（fp@vendor-react dispatch 68ms 实测）——
//      "滚轮一下 = wheel sync render + scroll sync render" 双路叠加。
// 修复：包装默认 observer —— 滚动中的 offset 通知（isScrolling=true）经
// rAF 合帧（一帧最多一次 notify，rAF 里读最新 scrollTop，vsync 对齐）；
// isScrolling=false（scrollend / isScrollingResetDelay debounce 的停止通知）
// 保持同步直达（取消 pending rAF）——isScrolling 的状态语义不变，仅通知
// 频率锁帧率。渲染结果零变化（合帧不改语义，只改时机）。
// ⛔ trace 13.gz（2026-09-18）实测：rAF 回调里读 `el.scrollTop` 在 21 万节点 DOM 上
// 每次强制布局 ~7ms，90 次 = 643ms（8.5s trace 的 7.5%）。现在改成：scroll 事件里
// 同步读 scrollTop（此时布局已完成，零强制布局），只把 cb(setState) 延迟到共享
// frameScheduler 的一个 rAF（一帧最多一次 React 通知，且与 TurnBody / store 共用
// 同一个 rAF → 自动批处理）。
const rafCoalescedObserveElementOffset: typeof defaultObserveElementOffset = (instance, cb) => {
  const win = instance.targetWindow
  if (!win || typeof win.requestAnimationFrame !== 'function') {
    return defaultObserveElementOffset(instance, cb)
  }
  // scroll 事件里同步读 offset（免费——scroll 事件本身意味着布局刚做完），
  // 只把 setState 延迟到 frameScheduler。
  let pendingOffset: number | null = null
  let pendingIsScrolling = false
  const flushTask = () => {
    if (pendingOffset === null) return
    cb(pendingOffset, pendingIsScrolling)
    pendingOffset = null
  }
  const wrappedCb: (offset: number, isScrolling: boolean) => void = (offset, isScrolling) => {
    if (!isScrolling) {
      // 滚动停止通知：取消 pending 合帧，同步直达（isScrolling=false 语义
      // 是"滚动已停"，延迟它会让 TanStack 的 isScrolling 状态晚一帧）。
      frameScheduler.cancel(flushTask)
      pendingOffset = null
      cb(offset, false)
      return
    }
    // 滚动中：在 scroll 事件里**同步读 scrollTop**（免费），只把 cb 延迟到
    // frameScheduler（一帧最多一次 React 通知，与 TurnBody / store 共用 rAF）。
    const el = instance.scrollElement
    if (!el) {
      cb(offset, true)
      return
    }
    const { horizontal, isRtl } = instance.options
    pendingOffset = horizontal ? el.scrollLeft * (isRtl ? -1 : 1) : el.scrollTop
    pendingIsScrolling = true
    frameScheduler.schedule(flushTask)
  }
  const cleanup = defaultObserveElementOffset(instance, wrappedCb)
  return () => {
    frameScheduler.cancel(flushTask)
    cleanup?.()
  }
}

/**
 * ── 滚动容器几何：忽略「没有布局的测量」（2026-09-13 交互卡顿的根因）────────────
 *
 * 与 `rafCoalescedObserveElementOffset` 同源的包装，防的是另一类事件：**容器被隐藏**
 * 时的 0×0 矩形。
 *
 * ⛔ 为什么必须忽略（实测根因，不是防御性编程）：手机端打开工具页会把 AgentPanel
 * 外壳置 `display:none`（`MobileAppShell` 的视图切换），消息滚动容器随之变成 0×0。
 * virtual-core 的 `observeElementRect` 把这次「没有布局的测量」当成真实几何
 * （`this.scrollRect = rect` → `getSize() === 0`）→ `getVirtualItems()` 塌成空 →
 * **所有 virt-row 卸载** → `CommittedTurn` 实例销毁 → 实测高度缓存/复核裁决整体丢失
 * → 返回时每个迭代块的内容全部重新挂载 + markdown 全量重解析。
 * 实测（390×844 / mock 40 turn × 40 迭代）：一次交互 320 个 `.iter-block` 卸载再重挂、
 * DOM 节点 528→3708、muted 318→0。
 *
 * 与 `TurnBody` / `iterationHeight` 里同一条铁律一致：**没有布局的测量不是测量**
 * （元素无渲染盒 / 宽高为 0）。容器重新可见时 ResizeObserver 照常上报真实几何，
 * 因此这里只丢弃退化读数，不丢任何真实的尺寸变化。
 */
const nonDegenerateObserveElementRect: typeof defaultObserveElementRect = (instance, cb) =>
  defaultObserveElementRect(instance, (rect) => {
    if (rect.width === 0 && rect.height === 0) return
    cb(rect)
  })

/**
 * ── 行尺寸：同样忽略「没有布局的测量」（同一条铁律的第二个入口）──────────────
 *
 * TanStack 对**每一行**都挂了 ResizeObserver（`_measureElement` → `options.measureElement`）。
 * 容器被隐藏（手机端开工具页 `display:none`）时它给每行报 0×0 → `resizeItem(index, 0)`
 * → 所有已挂载行的尺寸塌成 0 → 总高塌陷 → **可见窗口按 0 高度铺开**。实测：返回 agent
 * 视图的那一帧会多挂 **14 行 = 280 个迭代块**（DOM 320→600）再被修正回来 —— 一次交互
 * 白白重挂 280 个块（每个块的 markdown 都要重新解析一次）。
 *
 * 修法同 `nonDegenerateObserveElementRect`：元素**没有渲染盒**时量到的 0 不是尺寸，
 * 返回"上次已知尺寸"（`resizeItem` 里 delta === 0 ⇒ 完全无副作用）；元素可见时的 0
 * 照实返回（那才是真实的 0 尺寸）。
 */
/**
 * ⛔ 不变式：**行高永远不允许是 0**。
 *
 * 虚拟行是 `position: absolute; top: 0; transform: translateY(start)`（见 render）。
 * TanStack 用 `start` 定位，而 `start` 是前面所有行 size 的累加 —— **只要某行 size=0，
 * 它的下一行就与它共享同一个 start ⇒ 两层内容画在同一 y 区间**（用户 2026-09-18 报的
 * P0：偶发消息正文互相穿插）。所以 0 高度不是"小"，而是**布局破坏**。
 *
 * 0 测量的两个来源都不可信：
 *   1. 元素当前没有渲染盒（隐藏 tab / 脱离文档 / `display:none` 祖先）——TanStack 的
 *      `observeElementRect` 那侧已由 `nonDegenerateObserveElementRect` 保住容器尺寸，
 *      但**行级**测量仍会拿到 0；
 *   2. 元素可见但内容尚未定形（刚挂载的异步 markdown / mermaid / 字体）—— 稍后
 *      ResizeObserver 会用真实高度修正。
 * 因此一律退回：**记住的实测高度 → 该行估算高度 → 1px 占位**（永不 0）。
 * 旧实现在「可见元素」分支直接返回 0，正是这条 P0 的口子。
 */
export const noDegenerateMeasureElement: typeof defaultMeasureElement = (element, entry, instance) => {
  const size = defaultMeasureElement(element, entry, instance)
  const el = element as unknown as HTMLElement
  const index = Number(el.dataset?.index ?? -1)
  const v = instance as unknown as {
    measurementsCache?: { key: unknown; size: number }[]
    itemSizeCache?: Map<unknown, number>
    options?: { estimateSize?: (index: number) => number }
  }
  if (size > 0) return size
  const item = index >= 0 ? v.measurementsCache?.[index] : undefined
  const remembered = item ? (v.itemSizeCache?.get(item.key) ?? item.size) : 0
  if (remembered > 0) return remembered
  const est = v.options?.estimateSize?.(index)
  return est && est > 0 ? est : 1
}

export function latestCompactBoundaryIndex(rows: Pick<ChatMessage, 'role' | 'content'>[]): number {
  let idx = -1
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i]
    if (isCompactMarker(row)) idx = i
  }
  return idx
}

/** isCompactMarker 的按行缓存（行对象在流式帧之间引用稳定）。
 *  ⛔ 不能每帧对全表做 `content.trimStart()`（每行一个新字符串）—— 代价 ∝
 *  已加载历史总量，正是「长历史 + 极小新 turn 也卡」的一部分（2026-09-13）。 */
const compactMarkerByMsg = new WeakMap<object, boolean>()

export function isCompactMarker(row: Pick<ChatMessage, 'role' | 'content'>): boolean {
  if (row.role !== 'user') return false
  const cached = compactMarkerByMsg.get(row as object)
  if (cached !== undefined) return cached
  const v = row.content.trimStart().startsWith('[Compacted context]')
  compactMarkerByMsg.set(row as object, v)
  return v
}


export const MessageList = memo(function MessageList({
  chatKey,
  followResetToken = 0,
  messages,
  liveProgress,
  busy = false,
  loading,
  loadingMore = false,
  hasMore = false,
  onLoadMore,
  error,
  onRewind,
  editingMessageId,
  onStartEdit,
  onEndEdit,
  footer,
}: MessageListProps) {
  // PERF（Trace-20260912T100816）：行级回调必须引用稳定。inline 箭头
  // （`(c) => onRewind(c, row)`）在每个流式帧都换引用 → MessageItem 的 memo
  // 被击穿 → 可见行全部重渲染。改为稳定 handler + 由 MessageItem 回填 row。
  const handleRewindRow = useCallback(
    (editedContent: string, row: ChatMessage) => onRewind?.(editedContent, row),
    [onRewind],
  )
  const handleStartEditRow = useCallback(
    (rowId: string) => onStartEdit?.(rowId),
    [onStartEdit],
  )
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  /** virtualizer 坐标原点在滚动容器里的 y（padding-top + 顶部哨兵高度）。
   *  **只量一次**（见下方 useLayoutEffect）——谓词里做纯算术，绝不逐行读 DOM。 */
  const contentOriginRef = useRef(0)
  const originRefEl = useRef<HTMLDivElement>(null)
  const stickToBottomRef = useRef(true)
  const pendingFollowRafRef = useRef<number | null>(null)
  // Generation counter — each scheduleFollow call increments this. The
  // tryScroll loop checks it to know if it's the latest follow (cancel
  // old loops when a new scheduleFollow supersedes them).
  const followGenRef = useRef(0)
  // Marks scrolls caused by our own scheduleFollow (el.scrollTop = scrollHeight).
  // Set before the write and cleared via queueMicrotask after — so the flag is
  // only true during the synchronous scroll event our write dispatches, not
  // across unrelated later scroll events.
  const programmaticScrollRef = useRef(false)
  // Track scroll velocity for dynamic overscan: fast scrolling needs more
  // pre-rendered rows to avoid blank flashes; static needs fewer (less work).
  const lastScrollTopRef = useRef(0)
  const lastScrollTimeRef = useRef(0)
  const [dynamicOverscan, setDynamicOverscan] = useState(8)
  const lastChatKeyRef = useRef<string | null | undefined>(chatKey)
  const lastRowCountRef = useRef(0)
  const lastFollowResetTokenRef = useRef(followResetToken)
  const lastTouchYRef = useRef<number | null>(null)
  const pointerScrollingRef = useRef(false)

  // React state mirrors for re-rendering UI elements (bubble, nav buttons)
  const [hasNewContent, setHasNewContent] = useState(false)
  const [visibleRange, setVisibleRange] = useState({ start: 0, end: 0 })

  const { t } = useI18n()

  // Combined row list: committed messages + optional live streaming row.
  //
  // ALWAYS remove intermediate assistant messages after the last user message.
  // ConvertMessagesToHistory can split one turn into multiple assistant
  // messages (when a Content assistant appears between ToolCalls). Without
  // this, both assistants render the same tools — once from DB iterations
  // and once from the progress snapshot — causing duplicates.
  // Only the LAST assistant after the last user message is kept; all earlier
  // ones are absorbed (their tools are in the snapshot or in the last
  // assistant's iterations).
  // Committed rows are order-stable between frames — bind+sort them ONCE per
  // `messages` change (history reload, appendAssistant, injectUserMessage).
  // 方案 A：messages 含 live 行（store.toRows() 输出，useChatMessages 订阅
  // store 每帧 syncMessages）。注意：不使用 useSyncExternalStore —— 它对
  // 高频流式 store 写（live 每帧变化 → getSnapshot 每帧新引用）会触发额外
  // re-render 循环（E2E assistantRows=0 / stream-jitter 超时）。props.messages
  // 由 useChatMessages 的订阅驱动，每帧更新即可。
  const rows = useMemo<ChatMessage[]>(
    () => orderMessageRows(bindTurnIDs(messages)),
    [messages],
  )
  /** 供 useLayoutEffect 的依赖用（`rows` 每帧换引用，只关心"有没有行"）。 */
  const rowsEmpty = rows.length === 0
  // Latest-rows ref: closures (IntersectionObserver, loadMore anchor restore)
  // must read the CURRENT rows, not a stale snapshot captured in effect deps —
  // after onLoadMore prepends older rows, the effect closure's `rows` is still
  // the pre-prepend array, so findIndex would miss the anchor.
  const rowsRef = useRef(rows)

  /**
   * 记忆感知的行测量（切会话性能修复，见 rowHeightMemory.ts）：
   * 内容指纹 + 宽度都命中 ⇒ **零 DOM 读**直接返回记忆高度；因为返回尺寸与 TanStack
   * 当前记录相等，`resizeItem` 会在 `size === item.size` 处早退 ⇒ 连
   * `shouldAdjustScrollPositionOnItemSizeChange` 的 getBoundingClientRect 环路也一并消失。
   * 依赖为空：回调只经 rowsRef/模块级单例现读，身份恒定（不能每帧新建，
   * 否则可见行的 ref 每帧重挂 → 每帧强制布局）。
   */
  const measureRow = useMemo(
    () =>
      // 边界 cast：本包装器与 TanStack 的泛型 instance 类型无关（见 rowHeightMemory.ts 的注释），
      // 只读 `dataset.index` / 记忆表 / 宽度。
      createHeightAwareMeasureElement({
        lookup: (index) => {
          const row = rowsRef.current[index]
          return row ? { key: rowMemoryKey(row, index), sig: rowSignature(row) } : undefined
        },
        // 挂载时的**初值提示**（记忆命中值，否则内容估算）——**零 DOM 读**；
        // 真实高度由上方 layout effect 的"先批量读、后批量写"在 **paint 前**校正。
        // 这样估算偏小不会压字、偏大不会留白：任何一帧渲染出的都是实测值。
        hint: (index) => {
          const row = rowsRef.current[index]
          if (!row) return undefined
          const remembered = heightMemory.get(
            rowMemoryKey(row, index),
            rowSignature(row),
            heightLayoutWidth.current(),
          )
          return remembered ?? estimateRowByContent(row)
        },
        measure: noDegenerateMeasureElement as unknown as (
          element: Element,
          entry: ResizeObserverEntry | undefined,
          instance: unknown,
        ) => number,
        memory: heightMemory,
        width: () => heightLayoutWidth.current(),
      }) as unknown as typeof defaultMeasureElement,
    [],
  )
  rowsRef.current = rows
  // ── loadMore 触发状态机（2026-09-13「一次手势 11 次请求」请求风暴根治）─────
  // 触发权：`loadMoreArmedRef` = 本轮「哨兵可见回合」的触发权是否还没用掉。
  //   arm  ← 哨兵**离开视口**（IO !isIntersecting：用户滚离顶部，或 prepend 补偿
  //          把哨兵推出视口）／哨兵**由不可见变为可见**（一次真实"滑到顶"手势）
  //   disarm ← 触发 loadMore 的那一瞬间
  // 只有 arm 状态下才允许触发。不用 setTimeout、不用重试计数 —— 触发权完全由
  // IO 的 intersection 状态驱动。
  const loadMoreArmedRef = useRef(false)
  // 哨兵上一次的可见性（null = 本 observer 尚未收到回调），用于识别"变得可见"。
  const sentinelVisibleRef = useRef<boolean | null>(null)
  // **待补偿**的锚定快照（prepend 前的 scrollTop + totalSize）。与触发权**解耦**：
  // 它记的是"还欠用户一次视口补偿"，只有补偿成功（或视口已被别处移动）才销账。
  // 绝不能在补偿成功前清掉（旧实现先清后判 delta<=0，等于白清），也绝不能拿它
  // 当触发守卫（长 turn 的页 delta 恒为 0 → 会把分页永久锁死）。
  const loadMoreRestoreRef = useRef<{ scrollTop: number; totalSize: number; deadline: number } | null>(null)
  // observer 回调必须读到**最新**的 loading/hasMore/onLoadMore/virtualizer，但这些
  // 值每次渲染都变（onLoadMore 的 useCallback deps 含 loadingMore/hasMore，身份每
  // 次 loading 翻转都变）—— 一旦进 effect deps，observer 就会反复重建，而**新建
  // observer 会立刻投递一次初始回调**（哨兵仍可见 ⇒ isIntersecting=true）⇒ 立刻又
  // 触发一次 loadMore ⇒ 请求风暴。故全部走 ref 现读，observer 只在 hasMore 变化时
  // 建/拆一次。
  const hasMoreRef = useRef(false)
  hasMoreRef.current = hasMore ?? false
  const loadingMoreRef = useRef(false)
  loadingMoreRef.current = loadingMore ?? false
  const onLoadMoreRef = useRef<MessageListProps['onLoadMore']>(undefined)
  onLoadMoreRef.current = onLoadMore
  // Invariant guard: the "thinking…" busy placeholder must never render below
  // a FINISHED assistant (copy button shown — turn complete). A finished turn
  // followed by "thinking…" would imply the completed turn is still running.
  // A committed assistant is isPartial=false with final content (approximation
  // of shouldRenderFinalContent at the row level).
  // busy placeholder 不再使用 lastIsFinishedAssistant —— committed assistant
  // 后面也可能有新 iter 在跑（busy=true），需要显示 placeholder。
  // const lastIsFinishedAssistant = ...  // 已删除
  // liveId 指向接收 liveProgress 的行（方案 A：live 行已在 messages 里，
  // isPartial 行）。没有 live 行时返回 null（liveProgress 不传给任何行）。
  // ⚠️ 必须匹配【最后一个】isPartial 行（最新 live），不能用 find（第一个）：
  // V5 让 cancel 后的 frozen 合并行也 isPartial=true，cancel 后发新消息时
  // rows 同时存在旧 frozen 行 + 新 live 行两个 isPartial。find 返回旧的
  // frozen 行 → 新 turn 的 streaming liveProgress（liveId 匹配的那行拿到的
  // 是 progress）传给旧行 → 旧行（user msg 上方）的 LiveIteration 渲染
  // "思考中…"（用户报告：cancel 后思考中显示在最新 user msg 上方）。最新的
  // isPartial 才是真正在接收 live 进度的行。
  const liveId = useMemo(() => {
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i].isPartial) return rows[i].id
    }
    return null
  }, [rows])
  const compactBoundaryIndex = useMemo(() => latestCompactBoundaryIndex(rows), [rows])
  const hasFooter = footer !== null && footer !== undefined

  // User message indices for navigation（单趟构建 —— 原实现 map+filter 每帧
  // 对全表产出两个中间数组；代价 ∝ 已加载历史总量）
  const userMessageIndices = useMemo(() => {
    const out: number[] = []
    for (let i = 0; i < rows.length; i++) {
      if (rows[i].role === 'user') out.push(i)
    }
    return out
  }, [rows])

  // TanStack Virtual —— API 返回函数，React Compiler 无法安全 memo；
  // virtualizer 按设计每次渲染重建内部映射。
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (index) => {
      const row = rows[index]
      if (!row) return ESTIMATE
      const key = rowMemoryKey(row, index)
      const remembered = heightMemory.get(key, rowSignature(row), heightLayoutWidth.current())
      if (remembered !== undefined) return remembered
      // GenUI 行给接近实际的初值（面板 header + 典型 UI 高度），真实高度由
      // measureElement 的 ResizeObserver 持续跟踪 —— 内容长高/折叠/展开自动修正。
      if (rowHasGenUI(row)) return GENUI_PANEL_ESTIMATE
      return estimateRowByContent(row)
    },
    overscan: dynamicOverscan,
    // scroll → rAF 合帧（模块级 rafCoalescedObserveElementOffset，见上方注释）：
    // 滚动中每帧最多一次 offset 通知（isScrolling=false 停止通知保持同步直达）。
    observeElementOffset: rafCoalescedObserveElementOffset,
    // 容器几何：忽略「没有布局的测量」（0×0）——否则容器被隐藏（手机端开工具页）
    // 会让可见窗口塌成空、所有行卸载（见模块级 nonDegenerateObserveElementRect）。
    observeElementRect: widthAwareObserveElementRect,
    // 行尺寸：同一个退化读数从**行**这一侧进来时同样必须忽略（见上面的
    // noDegenerateMeasureElement）——否则隐藏期间所有行塌成 0 高，返回时多挂 14 行。
    measureElement: measureRow,
    getItemKey: (index) => {
      const r = rows[index]
      if (!r) return `row-${index}`
      // 稳定 turn 键：assistant 行 live→committed 使用同一个 turnID+role（live 行
      // id="turn-N-live"、committed 行 id=assistant.id）—— 若用 row.id，提交瞬间
      // item.key 改变 → TanStack 整行 <div key> 卸载重建（"agent turn 结束后整个
      // turn DOM 重建"根因）。keying 用 turnID+role 让行在 live→committed 间保持
      // 挂载，内容由 React reconcile（不 remount）。legacy（turnID=0）与 pending
      // 用户行（MAX_SAFE_INTEGER，绑定真实 turn 前）回退 row.id。
      if (r.turnID > 0 && r.turnID < Number.MAX_SAFE_INTEGER) {
        return `turn-${r.turnID}-${r.role}`
      }
      return r.id ?? `row-${index}`
    },
  })

  // virtualizer 实例是稳定的（useVirtualizer 只更新 options），但 observer 回调
  // 必须读**当前**实例 —— 经 ref 现读，避免把 virtualizer 放进 effect deps。
  const virtualizerRef = useRef(virtualizer)
  virtualizerRef.current = virtualizer

  // Workaround: virtual-core checks `this.shouldAdjustScrollPositionOnItemSizeChange`
  // (direct instance property) in resizeItem, but setOptions only stores it in
  // `this.options` — the option is never actually applied. Assign it directly.
  // Custom condition: only correct scrollTop when the resized item is ENTIRELY
  // above the viewport (item.end < scrollTop). The default condition
  // (item.start < scrollTop) also fires for items partially in the viewport —
  // when such an item changes size (code highlighting, image loading, markdown
  // settling), the correction moves the user's viewport even though they
  // didn't scroll. Using item.end ensures only items fully above the viewport
  // trigger correction, keeping visible items stable.
  useLayoutEffect(() => {
    const v = virtualizer as unknown as {
      shouldAdjustScrollPositionOnItemSizeChange?: (item: { key: string; start: number; end: number }, delta: number, instance: unknown) => boolean
    }
    v.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) => {
      // ⚠️ 必须用 **DOM 真相**判断「是否完全在视口上方」，不能用 virtualizer 的
      // item 坐标：item.start/item.end 是「内容流从 0 开始」的坐标，**不含**滚动
      // 容器的 padding-top 与顶部哨兵（loadMore sentinel）高度；而 scrollOffset 是
      // 原始 scrollTop（含 padding）。两者差一个 padding（16px）⇒ 还剩 ≤16px 露在
      // 视口里的行会被误判成"完全在上方"，它换行长高时触发 +delta 补偿滚动
      // （stream-jitter.spec.ts「读历史时视口跳 23px」= 本 bug；是否误判取决于露出
      // 的宽度是否小于 padding ⇒ 间歇复现，CI 上偶发红灯）。
      const inst = instance as {
        scrollOffset?: number | null
        scrollElement?: HTMLElement | null
        elementsCache?: Map<string, HTMLElement>
      }
      // ⛔ 绝不在这里读 DOM（2026-09-18 生产 trace 归因，用户实测"还是卡"）：
      // 本谓词由 TanStack `resizeItem` **对每个尺寸变化的行**调用一次，而它内部
      // 紧接着会写 `scrollTop`（补偿滚动）——「读 → 写 → 读」交替 ⇒ 每次调用都
      // 触发一次**强制同步布局**。一次流式提交挂载/改高 N 行 = N 次全量布局。
      // 实测：`getBoundingClientRect` 33.3% + `get offsetHeight` 13.3%（合计 46.6% CPU），
      // 22 个 long task 累计 5.2s、单次最大 **1036ms**，调用链 = React commit → 本谓词。
      //
      // 用**只量一次**的内容原点（padding-top + 顶部哨兵高度，见 originRefEl 的
      // useLayoutEffect）把 virtualizer 坐标换成滚动容器坐标，再与 TanStack 自己维护的
      // `scrollOffset`（= 真实 scrollTop）比较 —— 语义与「元素下缘 ≤ 视口上缘」逐字等价，
      // 但 O(1) 且**零 DOM 读**。
      return item.end + contentOriginRef.current <= (inst.scrollOffset ?? 0)
    }
  }, [virtualizer])

  /**
   * 内容原点（virtualizer 坐标 0 在滚动容器里的真实 y）——**只量一次**。
   *
   * 需要它是因为 virtualizer 的 item 坐标从「内容流 0」起算，而 `scrollOffset` 是**原始
   * scrollTop**（含容器 padding-top 与顶部 loadMore 哨兵的高度）。两者差一个常量；
   * 用常量把坐标换算对齐后，`shouldAdjustScrollPositionOnItemSizeChange` 里就能做纯
   * 算术判定（见该处的 PERF 注释：逐行读 DOM 会在 resizeItem 写 scrollTop 之后
   * 触发强制同步布局，实测占 46.6% CPU）。
   *
   * 依赖只在**会改变原点**的时刻重算：会话切换（padding/结构变）、哨兵出现/消失
   * （hasMore）、哨兵内容切换（loadingMore：spinner ↔ 文本）、以及首行出现时。
   * 每次测量是一次强制布局，但只发生在这几个稀疏时刻（不是每行、不是每帧）。
   */
  useLayoutEffect(() => {
    const wrapper = originRefEl.current
    const scroller = scrollRef.current
    if (!wrapper || !scroller) return
    const origin =
      wrapper.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop
    if (Number.isFinite(origin) && origin >= 0) contentOriginRef.current = origin
  }, [chatKey, hasMore, loadingMore, rowsEmpty])

  /**
   * 迭代块「高度 / 冻结裁决」的作用域 = **会话身份 + 布局宽度**。
   *
   * 为什么是内容身份而不是组件实例（2026-09-13「开侧边栏慢 5-6 倍」根因）：行会因
   * 任何扰动布局的交互（手机端开工具页把 AgentPanel 外壳 `display:none`）整体卸载
   * 再重挂 —— 实例态作用域随卸载销毁 ⇒ 返回时无实测高度 ⇒ 每个迭代块的内容全部
   * 重新挂载 + markdown 全量重解析。同一 turn 的同一迭代块内容是**不变**的
   * （宽度不变 ⇒ 高度不变），所以同一 scope 必须能跨重挂载复用先前的实测高度与
   * 复核裁决（`sharedIterationHeightTracker`）。
   *
   * 两个成分都是硬要求：
   *   - **会话**：turnID 是每会话独立编号，不含会话会让两个会话的同 key 撞车
   *     （76b731de：新会话读到旧会话"已结算高度" → 立刻冻结成空块）；
   *   - **布局宽度**：高度不变性的前提；宽度一变旧高度一律作废（否则拿旧宽度的
   *     占位高度去定新宽度的行高）。
   *
   * 宽度取 `virtualizer.scrollRect`（已被 `nonDegenerateObserveElementRect` 保住
   * 最后一次真实宽度：容器被隐藏时不塌成 0）。首帧尚未量到 → 0，属于一个独立的
   * 初始作用域：量到真实宽度后自然切换，切换时 `CommittedTurn` 会丢弃分块决策重算。
   */
  const heightScope = `${chatKey ?? 'none'}|${Math.round(virtualizer.scrollRect?.width ?? 0)}`

  // Row measurement: wrap the official measureElement (it prunes disconnected
  // nodes on ref(null) — do NOT early-return on null, that leaked stale nodes).
  // After measureElement (which has already forced layout internally, so the
  // read is cheap) record the REAL height into heightMemory keyed by the row's
  // stable key — the next history reload estimates this row from the remembered
  // value instead of the content heuristic, making reloads zero-correction
  // (zero jitter). This is NOT the old freeze cache: nothing skips
  // measurement; the ResizeObserver keeps tracking and overrides the estimate
  // the moment real height changes. Scroll stability is handled by
  // shouldAdjustScrollPositionOnItemSizeChange above, fold transitions get an
  // authoritative re-measure via onTransitionEnd below.
  //
  // PERF: deps MUST be [virtualizer] only — `rows` changes identity every
  // streaming frame, which would recreate this callback per frame → React
  // detaches/reattaches the ref on EVERY visible row (ref identity change)
  // → forced synchronous layout (getBoundingClientRect) for every visible row
  // on every frame (~300-600 forced layouts/sec while streaming on phones).
  // Row data is read through rowsRef (kept in sync at line ~rowsRef.current
  // = rows), so the callback body always sees the CURRENT row. virtualizer
  // is a stable instance (useVirtualizer keeps one instance; only its options
  // are updated per render).
  /**
   * ⛔ 「**永远准** + **性能优秀**」两条硬要求的落点（2026-09-18，用户明确要求）。
   *
   * 虚拟列表的行位置只能由**真实高度**决定：估算偏小 ⇒ 下一行压上来（字符重合）；
   * 估算偏大 ⇒ 出现大段空白（用户截图）。而挂载时**逐行**读 DOM 又是 O(N) 强制布局
   * （切会话 10.9s 的根源）—— 所以"跳过测量"和"逐行测量"都不行。
   *
   * 正解：**同一个 commit 的 layout effect 里"先批量读、后批量写"** ——
   *   ① 读阶段：一次把所有已渲染行的真实高度读完（**读之间没有任何写** ⇒ 整批只付
   *      **一次**布局，而不是 N 次）；
   *   ② 写阶段：把真实高度喂回虚拟器（尺寸未变的行 `resizeItem` 内部早退 ⇒ 零写）。
   * layout effect 在 **paint 之前**执行 ⇒ 用户永远看不到"按估算定位"的那一帧
   * ⇒ **既无重叠也无空白**，且每 commit 只付一次布局。
   *（后续内容变化仍由 ResizeObserver 自带的 borderBoxSize 免费校正。）
   */
  useLayoutEffect(() => {
    const items = virtualizer.getVirtualItems()
    if (items.length === 0) return
    // TanStack 的 elementsCache 以 VirtualItem.key 为键（Key = string | number）
    // —— 用 Map<unknown, …> 取，避免把 key 强转成 string（运行期行为不变）。
    const inst = virtualizer as unknown as { elementsCache?: Map<unknown, HTMLElement> }
    const measured: Array<{ index: number; height: number }> = []
    // ① 读：整批（无写穿插）
    for (const it of items) {
      const el = inst.elementsCache?.get(it.key)
      if (!el) continue
      const h = Math.round(el.getBoundingClientRect().height)
      if (h > 0 && h !== Math.round(it.size)) measured.push({ index: it.index, height: h })
    }
    // ② 写：仅尺寸变化的行（并把真值记进记忆，供后续未渲染行做初值）
    for (const m of measured) {
      virtualizer.resizeItem(m.index, m.height)
      const row = rowsRef.current[m.index]
      if (row) {
        heightMemory.set(
          rowMemoryKey(row, m.index),
          rowSignature(row),
          heightLayoutWidth.current(),
          m.height,
        )
      }
    }
  })

  const measureRef = useCallback(
    (node: HTMLElement | null) => {
      if (!node) {
        virtualizer.measureElement(null)
        return
      }
      virtualizer.measureElement(node)
      const idx = Number(node.dataset?.index ?? -1)
      const row = rowsRef.current[idx]
      if (row) {
        // ⛔ 不要再读一次 DOM（2026-09-18 trace：`measureElement` 本身已经量过，
        // 紧跟一次 `getBoundingClientRect()` 会与 TanStack 内部刚做的写（scrollTop
        // 补偿）交错 ⇒ 又一次强制同步布局）。直接从 TanStack 的测量缓存取（纯内存）。
        const size =
          (virtualizer as unknown as { measurementsCache?: { size: number }[] }).measurementsCache?.[idx]
            ?.size ?? 0
        if (size > 0) {
          // 记录实测高度 + 指纹 + 宽度：同一内容的行在切会话/重挂载时**零 DOM 读**复用
          // （见 rowHeightMemory.ts；缓存上限与宽度失效都在该模块内处理）。
          heightMemory.set(
            rowMemoryKey(row, idx),
            rowSignature(row),
            heightLayoutWidth.current(),
            Math.round(size),
          )
        }
      }
    },
    // PERF note above explains deps choice; rowsRef is a stable closure ref covering row identity
    [virtualizer],
  )

  // ── RENDER-LOSS / VIRTUALIZER-DROP monitor ────────────────────────────────
  // User report: "agent turn 消失" — the live tail row vanishes from the DOM
  // until the next iteration's first SSE event. rows is the FULL array
  // (committed + live); a live tail row disappearing WITHOUT a committed
  // replacement while the turn is still busy is REAL data loss — not
  // virtualization (getVirtualItems only returns the visible window, so its
  // length shrinking on scroll is NORMAL and must NOT be treated as a signal).
  // This effect also watches getVirtualItems() tail-index regression while
  // sticking to the bottom (the user's originally requested monitor).
  const prevRowsTailRef = useRef<{
    id: string | null
    turnID: number
    isPartial: boolean
    chatKey: string | null | undefined
    len: number
  } | null>(null)
  useEffect(() => {
    const tail = rows.length > 0 ? rows[rows.length - 1] : null
    const prev = prevRowsTailRef.current
    if (prev && prev.chatKey !== chatKey) {
      // Session switch: chatKey updates on the React render BEFORE the rows
      // are reloaded (useChatMessages still holds the OLD session's rows in
      // the same frame). Comparing the old session's live tail against the
      // new session's (still empty) rows would false-fire RENDER_LOSS_ROWS
      // (observed: chatKey=web:chat_A91F476D963A, prevTail=turn-337-live,
      // rowsLen=0 right after switching). Reset the baseline so the new
      // session's first rows start clean.
      prevRowsTailRef.current = null
      return
    }
    prevRowsTailRef.current = {
      id: tail?.id ?? null,
      turnID: tail?.turnID ?? 0,
      isPartial: tail?.isPartial ?? false,
      chatKey,
      len: rows.length,
    }
    if (!prev || prev.chatKey !== chatKey) return // session switch → rows replaced legitimately
    // 1) ROWS-LEVEL: live tail row vanished without committed replacement.
    // 快路径：绝大多数帧 tail 就是 prev 行（O(1)），只有真消失时才做 O(N) 扫描。
    const liveVanished =
      prev.isPartial && prev.id !== null && tail?.id !== prev.id && !rows.some((r) => r.id === prev.id)
    if (liveVanished && busy) {
      // Legal replacement paths: (a) normal text-event finalize — a committed
      // assistant with the same turnID appears; (b) commitLiveProgressAndReset
      // (turn_started/commit) — a committed assistant appears. If rows contain
      // NO committed assistant at all, the live content was wiped with nothing
      // taking its place → the "turn 消失只剩 user msg" report.
      const sameTurnCommitted = prev.turnID > 0 &&
        rows.some((r) => r.role === 'assistant' && !r.isPartial && r.turnID === prev.turnID)
      const anyCommittedAssistant = rows.some((r) => r.role === 'assistant' && !r.isPartial)
      if (!sameTurnCommitted && !anyCommittedAssistant) {
        console.error('[RENDER_LOSS_ROWS] live turn vanished without committed replacement', {
          prevTailId: prev.id,
          prevTurnID: prev.turnID,
          prevLen: prev.len,
          rowsLen: rows.length,
          busy,
          chatKey,
          lastRow: tail ? { id: tail.id, role: tail.role, turnID: tail.turnID, isPartial: tail.isPartial } : null,
          liveId,
          rowIds: rows.map((r) => r.id).slice(-5),
        })
        console.error(new Error('[RENDER_LOSS_ROWS] stack'))
      }
    }
    // 2) VIRTUALIZER-LEVEL: only alarm when the LAST row is the LIVE row
    // (isPartial=true) yet getVirtualItems() does not cover it while sticking
    // to the bottom. Historical committed rows sitting outside the viewport is
    // NORMAL virtualization (refresh lands at the top) — NOT a bug. The live
    // tail row being unrendered is the "agent turn 消失" symptom.
    const lastRow = rows.length > 0 ? rows[rows.length - 1] : null
    if (lastRow?.isPartial && stickToBottomRef.current) {
      const items = virtualizer.getVirtualItems()
      if (items.length > 0) {
        const lastItemIdx = items[items.length - 1].index
        if (lastItemIdx < rows.length - 1) {
          const scroller = scrollRef.current
          const v = virtualizer as unknown as {
            scrollOffset?: number
            scrollRect?: { height: number } | null
            getTotalSize?: () => number
            scrollElement?: HTMLElement | null
          }
          console.error('[VIRTUALIZER_TAIL_DROP] getVirtualItems() does not cover the LIVE last row while sticking to bottom', {
            lastItemIdx,
            rowsLen: rows.length,
            itemsLen: items.length,
            lastRowId: lastRow.id,
            lastRowRole: lastRow.role,
            busy,
            // ── 决定性诊断：内部 offset vs DOM 真相 ──
            vOffset: v.scrollOffset,
            vTotal: v.getTotalSize?.(),
            domTop: scroller ? Math.round(scroller.scrollTop) : null,
            domScrollH: scroller?.scrollHeight ?? null,
            domClientH: scroller?.clientHeight ?? null,
            sameEl: v.scrollElement === scroller,
          })
          console.error(new Error('[VIRTUALIZER_TAIL_DROP] stack'))
        }
      }
    }
  }, [rows, busy, chatKey, liveId, virtualizer])

  const cancelPendingFollow = useCallback(() => {
    if (pendingFollowRafRef.current === null) return
    cancelAnimationFrame(pendingFollowRafRef.current)
    pendingFollowRafRef.current = null
  }, [])

  const pauseFollowing = useCallback(() => {
    stickToBottomRef.current = false
    cancelPendingFollow()
  }, [cancelPendingFollow])

  const resumeFollowing = useCallback(() => {
    stickToBottomRef.current = true
    setHasNewContent(false)
  }, [])

  const scheduleFollow = useCallback(() => {
    if (!stickToBottomRef.current) return
    // Coalesce: if a follow is already pending, don't cancel it — just mark
    // that a new follow was requested. The pending RAF will check the latest
    // scrollHeight. Cancelling starves the RAF when ResizeObserver fires rapidly.
    if (pendingFollowRafRef.current !== null) return
    setHasNewContent(false)
    const gen = ++followGenRef.current
    pendingFollowRafRef.current = requestAnimationFrame(() => {
      pendingFollowRafRef.current = null
      if (!stickToBottomRef.current || gen !== followGenRef.current) return
      const el = scrollRef.current
      if (el) {
        let attempts = 0
        const tryScroll = () => {
          // Increase from 15 to 30 attempts (~500ms at 60fps) — TanStack
          // Virtual's lazy measurement (measureElement via ResizeObserver) can
          // take >250ms for large lists with markdown/code highlighting.
          if (!stickToBottomRef.current || gen !== followGenRef.current || ++attempts > 30) return
          programmaticScrollRef.current = true
          const prev = el.scrollHeight
          el.scrollTop = el.scrollHeight
          queueMicrotask(() => { programmaticScrollRef.current = false })
          requestAnimationFrame(() => {
            if (stickToBottomRef.current && gen === followGenRef.current && el.scrollHeight > prev) tryScroll()
          })
        }
        tryScroll()
      }
    })
  }, [])

  // ── Scroll event handler ──────────────────────────────────────────────────
  // onScroll syncs stickToBottomRef with the true scroll position — this is
  // the ONLY event that knows whether the user is actually at the bottom,
  // including scroll paths that don't fire wheel/pointer/touch handlers
  // (e.g. scrollbar-drag on some browsers, programmatic/external scroll).
  //
  // A programmatic-scroll flag (programmaticScrollRef) distinguishes our own
  // scheduleFollow write from genuine user scroll. Without it, content growth
  // fires scheduleFollow → scrollTop=scrollHeight → onScroll fires while
  // scrollTop is momentarily at the old position (before the browser applies
  // the write) → a naive "not at bottom → pause" would kill following mid-stream.
  // ── RAF-batched onScroll: zero setState in the scroll event itself ──────────
  // Trace profile (Trace-20260826T224702): 197 scroll events → 10+ React
  // reconciles (fn=ee, 27-69ms each) + Commit (71ms). Each setState in onScroll
  // triggers React to reconcile the ENTIRE MessageList subtree (GenUI panels,
  // TurnBody, ToolRender, etc.) synchronously — 30-70ms per scroll event.
  //
  // Fix: onScroll does ONLY ref updates (no React render). A RAF callback batches
  // all pending setState calls once per frame (max 60 renders/sec instead of 197).
  // The RAF callback also skips React entirely when nothing changed.
  const scrollRafRef = useRef<number | null>(null)
  const pendingOverscanRef = useRef<number | null>(null)
  const pendingRangeRef = useRef<{ start: number; end: number } | null>(null)
  const dynamicOverscanRef = useRef(dynamicOverscan)
  dynamicOverscanRef.current = dynamicOverscan
  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const now = performance.now()

    // ── Ref-only updates (zero React render) ────────────────────────────────
    const dt = now - lastScrollTimeRef.current
    if (dt > 0) {
      const delta = Math.abs(el.scrollTop - lastScrollTopRef.current)
      const velocity = delta / dt
      const target = velocity > 2 ? 14 : velocity > 0.5 ? 8 : 5
      pendingOverscanRef.current = target
    }
    // loadMore 的锚定补偿记的是「还欠用户一次视口钉住」：视口一旦被**别的东西**
    // 移动过（用户自己滚 / virtualizer 自己的尺寸修正），这次补偿就已经没有意义，
    // 当场销账 —— 否则过期的 delta（可能很大）会在稍后把视口拽走。
    if (el.scrollTop !== lastScrollTopRef.current && !programmaticScrollRef.current) {
      loadMoreRestoreRef.current = null
    }
    lastScrollTopRef.current = el.scrollTop
    lastScrollTimeRef.current = now
    // 导航的可见范围：必须【无条件】更新（master 修复：程序化滚动期间
    // programmaticScrollRef 频繁置位，旧守卫会让 visibleRange 停留在初始
    // {0,0} → 导航 disabled / activeSeq 不更新）。程序化滚动时更新同样正确。
    const items = virtualizer.getVirtualItems()
    if (items.length > 0) {
      pendingRangeRef.current = { start: items[0].index, end: items[items.length - 1].index }
    }

    // ── Schedule ONE RAF for all setStates (max 1 React render per frame) ───
    if (scrollRafRef.current !== null) return
    scrollRafRef.current = requestAnimationFrame(() => {
      scrollRafRef.current = null
      // Apply pending overscan
      const targetOverscan = pendingOverscanRef.current
      if (targetOverscan !== null && targetOverscan !== dynamicOverscanRef.current) {
        dynamicOverscanRef.current = targetOverscan
        setDynamicOverscan(targetOverscan)
      }
      // Apply pending visible range (only for nav button states)
      const range = pendingRangeRef.current
      if (range) {
        setVisibleRange((prev) =>
          prev && prev.start === range.start && prev.end === range.end ? prev : range,
        )
      }
    })
  }, [virtualizer, cancelPendingFollow])

  // Scroll-to-top sentinel ref — used by IntersectionObserver to detect
  // when the user scrolls to the top and trigger loadMore.
  const sentinelRef = useRef<HTMLDivElement | null>(null)

  // ── 哨兵 IntersectionObserver：loadMore 的唯一触发源（arm/disarm 状态机）──
  // 旧的 effect deps 含 `loadingMore`/`onLoadMore`/`virtualizer` —— 每次 loading
  // 翻转（每次请求都有 true→false）都会 disconnect + observe 一个**新** observer，
  // 而新建 observer 会立刻投递一次初始回调；此时哨兵仍在视口内（长 turn 的一页
  // DB 行被服务端折叠进已存在的 turn slot ⇒ 渲染行数不变 ⇒ 视口不动 ⇒ 哨兵不动）
  // ⇒ 回调立刻再触发一次 loadMore ⇒ 自激请求风暴（实测一次手势 11 次请求 /
  // observe=2037）。触发端也没有"本轮已触发"的记忆，任何回调都当新手势。
  //
  // 现在：observer 只在 `hasMore` 变化时建/拆一次（其余状态经 ref 现读），触发权
  // 由 loadMoreArmedRef 显式管理 —— **触发即 disarm**，只有
  //   ① 哨兵离开视口（IO !isIntersecting：用户真的滚离顶部，或 prepend 补偿把哨兵
  //      推出视口），或
  //   ② 哨兵由不可见变为可见（一次真实"滑到顶"手势；含 observer 首次回调就看到
  //      可见 —— 内容短到视口不可滚动时"离开视口"物理上不可达，留这条退路，
  //      否则分页会永久锁死）
  // 才重新 arm。
  useEffect(() => {
    loadMoreArmedRef.current = false
    sentinelVisibleRef.current = null
    if (!hasMore) return
    const el = sentinelRef.current
    if (!el || typeof IntersectionObserver === 'undefined') return

    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[0]
        if (!entry) return
        const visible = entry.isIntersecting
        const wasVisible = sentinelVisibleRef.current
        sentinelVisibleRef.current = visible
        if (!visible) {
          // ① 哨兵离开视口 ⇒ 归还触发权（下次再回到顶部是一次新手势）
          loadMoreArmedRef.current = true
          return
        }
        // ② 由不可见变为可见 ⇒ 一次真实"到达顶部"手势
        if (wasVisible !== true) loadMoreArmedRef.current = true
        if (!loadMoreArmedRef.current) return // 本轮「可见回合」已触发过 ⇒ 不再发请求
        if (!hasMoreRef.current || loadingMoreRef.current) return
        const cb = onLoadMoreRef.current
        const scroller = scrollRef.current
        if (!cb || !scroller) return
        loadMoreArmedRef.current = false // 触发即 disarm
        // 快照必须在 onLoadMore **之前**取：prepend 落地后要用
        // ΔscrollTop == ΔtotalSize 把视口钉回原来那段内容（老行只出现在视口上方，
        // 用户只看到"数据多了"，不闪到顶部再跳回来）。
        loadMoreRestoreRef.current = {
          scrollTop: scroller.scrollTop,
          totalSize: virtualizerRef.current.getTotalSize(),
          // 补偿窗口：覆盖"前置行从估算被实测"的那几次长高（约几百 ms）
          deadline: performance.now() + 800,
        }
        void cb()
      },
      { root: scrollRef.current, threshold: 0 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasMore])

  // ── loadMore 锚定补偿：ΔscrollTop == ΔtotalSize，视口在原内容上纹丝不动 ────
  // 两处触发，缺一不可：
  //   a) `rows` 的 useLayoutEffect —— prepend 落地那一帧（paint 前）就补偿，用户
  //      看不到闪动；
  //   b) content 的 ResizeObserver —— "渲染行数不变、只有已存在的 slot 长高"的那
  //      一页（服务端把新 DB 行并进已有 turn slot；或新行从估算高度被实测修正）
  //      在 a) 那一刻 totalSize 还没变（delta<=0）⇒ **保留快照**，等 total 真的
  //      长起来再补。旧实现在 delta<=0 时先清快照再放弃 —— 既不补偿也不重试，
  //      视口不动 ⇒ 哨兵不离开视口 ⇒ 永不 re-arm 的死循环。
  const restoreLoadMoreAnchor = useCallback(() => {
    const snap = loadMoreRestoreRef.current
    if (!snap) return
    const el = scrollRef.current
    if (!el) {
      loadMoreRestoreRef.current = null
      return
    }
    const total = virtualizerRef.current.getTotalSize()
    const delta = total - snap.totalSize
    if (delta <= 0) return // 上方还没长出来：欠着，等下一次（不放弃，也不销账）
    programmaticScrollRef.current = true
    el.scrollTop = snap.scrollTop + delta
    queueMicrotask(() => { programmaticScrollRef.current = false })
    // ⚠️ **补偿后继续欠着**（2026-09-15「翻页时已渲染内容抖动」根治）：
    // prepend 落地那一刻前置行还是**估算**高度，随后被实测 ⇒ 总高**再次**变化 ⇒ 只补一次的
    // 旧实现会把这段二次长高留给浏览器 ⇒ 位置二次跳。改为把快照**平移到新基准**并保留，
    // 让后续每一次长高都继续补偿；只在「用户自己滚动」（非程序滚动）或超时后才销账。
    loadMoreRestoreRef.current = { scrollTop: el.scrollTop, totalSize: total, deadline: snap.deadline }
  }, [])

  useLayoutEffect(() => {
    restoreLoadMoreAnchor()
  }, [rows, restoreLoadMoreAnchor])

  useEffect(() => {
    const content = contentRef.current
    if (!content || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => restoreLoadMoreAnchor())
    observer.observe(content)
    return () => observer.disconnect()
  }, [restoreLoadMoreAnchor])

  // Check if we're at the bottom after a RAF (post-scroll) and resume following.
  const checkBottomAndResume = useCallback(() => {
    requestAnimationFrame(() => {
      const el = scrollRef.current
      if (el && isAtBottom(el)) resumeFollowing()
    })
  }, [resumeFollowing])

  // ── User scroll detection ─────────────────────────────────────────────────
  // Wheel: always pause first (both directions). If scrolling DOWN and we
  // end up at the bottom, resume following after the browser applies the scroll.
  const onWheel = useCallback((e: React.WheelEvent<HTMLDivElement>) => {
    pauseFollowing()
    if (e.deltaY > 0) checkBottomAndResume()
  }, [pauseFollowing, checkBottomAndResume])

  const onKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'End') {
      resumeFollowing()
      scheduleFollow()
      return
    }
    if (['ArrowUp', 'PageUp', 'Home'].includes(e.key) || (e.key === ' ' && e.shiftKey)) {
      pauseFollowing()
    } else if (['ArrowDown', 'PageDown'].includes(e.key)) {
      pauseFollowing()
      checkBottomAndResume()
    }
  }, [pauseFollowing, resumeFollowing, scheduleFollow, checkBottomAndResume])

  // Treat the live snapshot as the activity revision: any progress update while
  // paused is new content, even when it does not change the rendered height.
  // Only show "new content" when NOT following the bottom — if we're already
  // at the bottom, there's nothing the user needs to scroll to.
  // When following (stick=true) but not actually at the bottom (diff > 2px),
  // force-scroll to bottom — this is a safety net for cases where ResizeObserver
  // didn't fire (e.g. virtualizer corrected scrollHeight without resizing content).
  //
  // When NOT following (stick=false), we do NOT capture/restore scrollTop.
  // The virtualizer's own scroll correction (via its internal ResizeObserver)
  // keeps visible items stable when sizes change — it fires after useEffect
  // but before paint. A RAF restore would UNDO that correction, causing the
  // viewport to jump (jitter). The virtualizer's correction is authoritative.
  useEffect(() => {
    if (!stickToBottomRef.current) {
      setHasNewContent(true)
      return
    }
    const el = scrollRef.current
    if (!el) return
    // ⛔ 本 effect 依赖含 `liveProgress` ⇒ **每流式帧都跑**。因此这里既不能读几何
    // （`el.scrollHeight` 紧跟 React 提交 ⇒ 强制同步布局；2026-09-18 生产 trace 实测
    // 这一处 **1.58s / 27.1% CPU**，单次 long task 885ms），也不能每帧写滚动位置
    // （写会把布局弄脏 ⇒ 下一次几何读又要重排，`get scrollTop` 0.64s 就是这么来的）。
    //
    // 两个动作都换成内存数字 + 一次 clamp 写入：
    //  1) 「是否已在底部」用**同一原点**换算：内容绝对底 = totalSize + contentOrigin
    //     （origin = 容器 padding + 顶部哨兵，见内容原点 effect）——
    //     不换算就会差一个 origin ⇒ 判断恒为"没到底" ⇒ 每帧写（本 bug 的根因）。
    //  2) 需要钉底时给一个必然超界的 `top`，浏览器自行 clamp 到底：
    //     **零几何读**（`el.scrollTop = el.scrollHeight` 那种写法必须先读 scrollHeight）。
    const v = virtualizerRef.current as unknown as {
      scrollOffset?: number
      scrollRect?: { height: number } | null
      getTotalSize?: () => number
    }
    const total = v.getTotalSize?.() ?? 0
    const viewport = v.scrollRect?.height ?? 0
    const offset = v.scrollOffset ?? 0
    if (viewport > 0 && total > 0 && offset + viewport >= total + contentOriginRef.current - 2) {
      return // 已经在底部：零布局读、零写入
    }
    programmaticScrollRef.current = true
    if (typeof el.scrollTo === 'function') {
      // 生产路径：超界 top 由浏览器 clamp ⇒ **零几何读**。
      el.scrollTo({ top: SCROLL_PIN_MAX })
    } else {
      // jsdom（单测）/ 极老环境没有 `Element.prototype.scrollTo`：退回直接赋值，
      // 这条退化路径读一次 `scrollHeight` 是可接受的（测试环境没有布局成本）。
      el.scrollTop = el.scrollHeight
    }
    queueMicrotask(() => { programmaticScrollRef.current = false })
  }, [rows.length, liveProgress, hasFooter])

  // ── ResizeObserver: follow bottom when sticky ─────────────────────────────
  useEffect(() => {
    const scrollElement = scrollRef.current
    const content = contentRef.current
    if (!scrollElement || !content || typeof ResizeObserver === 'undefined') return
    // ResizeObserver fires during the browser's pre-paint phase (same as
    // useLayoutEffect), so synchronous scrolling here has no visual flicker.
    // This is critical for the virtualizer: it fires many ResizeObserver
    // callbacks during lazy measurement, and each one must immediately correct
    // scrollTop to the new scrollHeight. Using RAF (scheduleFollow) here causes
    // an active loop: the RAF cancels/reschedules faster than it can execute.
    //
    // CRITICAL: observe BOTH the content and the scroll element — they cover
    // DIFFERENT resize cases:
    //
    //  - CONTENT growth (live row height changes, iteration history append,
    //    code highlighting) changes ONLY scrollHeight. ResizeObserver reports
    //    contentRect (clientHeight), and the scroll element's clientHeight
    //    stays fixed at the viewport height — so a scroll-element observer
    //    NEVER fires for content growth. contentRef wraps the virtualizer's
    //    sizing div (height = totalSize), so ITS contentRect tracks content
    //    height and fires on every growth.
    //
    //  - VIEWPORT shrink (the composer auto-grows up to 200px and squeezes
    //    this flex-1 list) changes clientHeight with content unchanged — the
    //    content observer NEVER fires, the sticky scrollTop stays at its old
    //    value, and the last row ends up hidden behind the taller composer
    //    (user-reported bug). The scroll element's own contentRect DOES
    //    change here, so observing it re-anchors to the bottom while sticky.
    const observer = new ResizeObserver(() => {
      if (!stickToBottomRef.current) return
      const el = scrollRef.current
      if (el) {
        programmaticScrollRef.current = true
        el.scrollTop = el.scrollHeight
        queueMicrotask(() => { programmaticScrollRef.current = false })
      }
    })
    observer.observe(content)
    observer.observe(scrollElement)
    return () => {
      observer.disconnect()
      cancelPendingFollow()
    }
  }, [cancelPendingFollow])

  // ── Chat switch or new messages: follow bottom when sticky ────────────────
  useLayoutEffect(() => {
    const el = scrollRef.current
    const chatChanged = lastChatKeyRef.current !== chatKey
    const initialLoad = !chatChanged && lastRowCountRef.current === 0 && rows.length > 0
    const followReset = lastFollowResetTokenRef.current !== followResetToken
    const newMessagesAdded = !chatChanged && !initialLoad && !followReset && rows.length > lastRowCountRef.current
    lastChatKeyRef.current = chatKey
    lastRowCountRef.current = rows.length
    lastFollowResetTokenRef.current = followResetToken
    if (!el || rows.length === 0 || (!chatChanged && !initialLoad && !followReset && !newMessagesAdded)) return
    if (newMessagesAdded) {
      // User sent a message (optimistic, not yet persisted) — always resume
      // following and scroll to bottom, even if the user had scrolled up.
      const lastRow = rows[rows.length - 1]
      if (lastRow?.role === 'user' && lastRow?.persisted === false) {
        resumeFollowing()
        scheduleFollow()
        return
      }
      // Assistant/streaming or DB messages: only follow if already sticky
      if (!stickToBottomRef.current) return
    }
    resumeFollowing()
    scheduleFollow()
  }, [chatKey, followResetToken, rows.length, resumeFollowing, scheduleFollow, virtualizer, loading])

  // ── Loading→false: scroll to bottom after history is fully loaded ──────────
  // (Removed polling — was not effective. Investigating root cause.)

  // ── Navigation helpers ────────────────────────────────────────────────────
  // 用户消息导航（MessageUserNav）：跳到目标行（user turn），解除贴底跟随
  // 防流式更新拉回底部。scrollToIndex 直接支持虚拟列表外的行号。
  const navigateToRow = useCallback(
    (rowIndex: number) => {
      pauseFollowing()
      virtualizer.scrollToIndex(rowIndex, { align: 'start' })
    },
    [pauseFollowing, virtualizer],
  )

  const scrollToBottomClick = useCallback(() => {
    resumeFollowing()
    scheduleFollow()
  }, [resumeFollowing, scheduleFollow])

  return (
    <div className="relative min-h-0 flex-1 overflow-hidden">
      <div
        ref={scrollRef}
        onScroll={onScroll}
        onWheel={onWheel}
        onPointerDown={(e) => {
          if (e.pointerType === 'mouse') {
            pointerScrollingRef.current = true
            pauseFollowing()
          }
        }}
        onPointerMove={(e) => {
          if (pointerScrollingRef.current && e.pointerType === 'mouse') pauseFollowing()
        }}
        onPointerUp={() => {
          if (pointerScrollingRef.current) {
            pointerScrollingRef.current = false
            checkBottomAndResume()
          }
        }}
        onPointerCancel={() => {
          pointerScrollingRef.current = false
        }}
        onTouchMove={(e) => {
          // Only break sticky on upward touch scroll (finger moving down = content scrolling up = user reading up)
          const touch = e.touches[0]
          if (!touch) return
          if (lastTouchYRef.current !== null) {
            const delta = touch.clientY - lastTouchYRef.current
            if (delta > 0) pauseFollowing()
          }
          lastTouchYRef.current = touch.clientY
        }}
        onTouchStart={() => {
          lastTouchYRef.current = null
        }}
        onTouchEnd={() => {
          checkBottomAndResume()
        }}
        onKeyDown={onKeyDown}
        tabIndex={0}
        style={{ overflowAnchor: 'none' }}
        className="h-full overflow-y-auto overflow-x-hidden overscroll-contain px-4 py-3 contain-content md:px-3 md:py-4"
      >
        {loading && rows.length === 0 && (
          <div className="flex h-full flex-col items-center justify-center gap-3">
            <Loader2 className="size-5 animate-spin text-text-muted" />
            <span className="text-xs text-text-muted">{t('agent.loading')}</span>
          </div>
        )}
        {error && (
          <div className="mx-auto my-4 max-w-md rounded-md border border-status-error/40 bg-status-error/10 p-3 text-sm text-status-error">
            {error}
          </div>
        )}
        {rows.length === 0 && !loading && !error && (
          <div className="flex min-h-full items-center justify-center px-6 py-16">
            <div className="max-w-md text-center">
              <Sparkles className="mx-auto mb-3 size-7 text-accent" />
              <div className="mb-1 text-sm font-medium text-text-primary">{t('agent.welcomeTitle')}</div>
              <div className="mb-4 text-xs text-text-muted">{t('agent.welcomeHint')}</div>
              {/* 每一步都是可点击的 —— 直接唤起对应面板（commandRouter）。 */}
              <div className="mx-auto flex max-w-sm flex-col gap-2 text-left text-xs text-text-secondary">
                <button
                  type="button"
                  data-testid="welcome-step-configure"
                  onClick={() => void commands.execute('settings.open', { section: 'llm' })}
                  className="group flex items-center gap-2 rounded-md bg-bg-secondary/50 px-3 py-2 text-left transition-colors hover:bg-bg-tertiary"
                >
                  <span className="min-w-0 flex-1">{t('agent.welcomeStep1')}</span>
                  <ChevronRight className="size-3.5 shrink-0 text-text-muted transition-transform group-hover:translate-x-0.5" />
                </button>
                <button
                  type="button"
                  data-testid="welcome-step-session"
                  onClick={() => void commands.execute('session.new')}
                  className="group flex items-center gap-2 rounded-md bg-bg-secondary/50 px-3 py-2 text-left transition-colors hover:bg-bg-tertiary"
                >
                  <span className="min-w-0 flex-1">{t('agent.welcomeStep2')}</span>
                  <ChevronRight className="size-3.5 shrink-0 text-text-muted transition-transform group-hover:translate-x-0.5" />
                </button>
                <button
                  type="button"
                  data-testid="welcome-step-chat"
                  onClick={() => void commands.execute('input.focus')}
                  className="group flex items-center gap-2 rounded-md bg-bg-secondary/50 px-3 py-2 text-left transition-colors hover:bg-bg-tertiary"
                >
                  <span className="min-w-0 flex-1">{t('agent.welcomeStep3')}</span>
                  <ChevronRight className="size-3.5 shrink-0 text-text-muted transition-transform group-hover:translate-x-0.5" />
                </button>
              </div>
            </div>
          </div>
        )}

        <div ref={contentRef} data-message-list-content className="w-full">
          {/* Scroll-to-top sentinel: triggers loadMore via IntersectionObserver */}
          {hasMore && (
            <div ref={sentinelRef} data-loadmore-sentinel className="flex justify-center py-2">
              {loadingMore ? (
                <Loader2 className="size-4 animate-spin text-text-muted" />
              ) : (
                <span className="text-xs text-text-muted">{t('agent.scrollLoadMore')}</span>
              )}
            </div>
          )}
          {rows.length > 0 && (
            <div
              ref={originRefEl}
              style={{ height: `${virtualizer.getTotalSize()}px` }}
              className="relative w-full"
            >
              {virtualizer.getVirtualItems().map((item) => {
                const row = rows[item.index]
                if (!row) return null
                const canRewind = canRewindMessage(row, item.index, compactBoundaryIndex)
                const isEditing = editingMessageId === row.id
                const editDisabled = editingMessageId !== null && editingMessageId !== row.id
                return (
                  <div
                    key={item.key}
                    data-index={item.index}
                    ref={measureRef}
                    onTransitionEnd={(e) => {
                      // Fold/expand animations (grid-template-rows 180ms in
                      // AnimatedCollapse) resize the row OUTSIDE React's commit
                      // knowledge. If the virtualizer's ResizeObserver misses
                      // the transition frames, it keeps the pre-fold size and
                      // the space below the folded panel stays blank
                      // (2026-08-28 report). Re-measure authoritatively when
                      // any descendant fold transition settles.
                      if (e.propertyName === 'grid-template-rows') {
                        virtualizer.measureElement(e.currentTarget)
                      }
                    }}
                    style={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      width: '100%',
                      transform: `translateY(${item.start}px)`,
                    }}
                    className={`virt-row py-1.5${row.id === liveId ? ' animate-msg-in' : ''}`}
                    data-turn-id={row.turnID || undefined}
                    data-message-id={row.id}
                    data-role={row.role}
                    data-iter-count={row.iterations?.length ?? 0}
                  >
                    <MessageItem
                      message={row}
                      liveProgress={row.id === liveId ? liveProgress : null}
                      heightScope={heightScope}
                      onRewind={onRewind ? handleRewindRow : undefined}
                      isEditing={isEditing}
                      onStartEdit={onStartEdit ? handleStartEditRow : undefined}
                      onEndEdit={onEndEdit}
                      editDisabled={editDisabled || !canRewind || busy}
                    />
                  </div>
                )
              })}
            </div>
          )}
          {/* Busy placeholder: when agent is thinking but no streaming
              content has arrived yet (e.g. session just started, or
              switched to a busy tab with no iterations). Shown during
              loading when rows exist (the spinner handles the empty case),
              so the user always sees feedback on a busy session.
              INVARIANT: exactly ONE thinking indicator in every state —
              liveId === null 时（无 live 行）由本 placeholder 渲染；live 行
              存在时由 LiveIteration 的空内容分支渲染 ShimmerThinking（第一
              迭代 + 迭代边界）。旧条件 `rows 最后是 user` 在 M4 架构下失效：
              turn_started 立即创建 live 行（isPartial）→ liveId 非 null 且
              最后一行是 live assistant → 本 placeholder 不渲染，而
              LiveIteration 的旧条件（iterationHistory.length > 0）第一迭代
              也不渲染 → 完全空白（切换会话新 turn，用户报告）。收紧为
              liveId === null 与 LiveIteration 严格互斥（排队消息沉底在 live
              行之后时 rows 最后是 user，旧条件会与本组件双渲染）。 */}
          {busy && !(loading && rows.length === 0) && liveId === null && (
            <div className="px-3 py-2">
              {liveProgress?.phase === 'compressing' ? (
                <div className="flex items-center gap-2 text-xs text-text-muted">
                  <Loader2 className="size-3.5 animate-spin" />
                  <span>{t('agent.compressing')}</span>
                </div>
              ) : (
                <ShimmerThinking />
              )}
            </div>
          )}
          {footer}
        </div>
      </div>

      {/* ── Bottom new-content bubble ─────────────────────────────────────────── */}
      <AnimatePresence>
        {hasNewContent && (
          <motion.button
            initial={{ opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 10 }}
            transition={{ duration: 0.2 }}
            onClick={scrollToBottomClick}
            className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 rounded-full bg-accent px-3 py-1 text-xs text-accent-foreground shadow-md"
          >
            ↓ {t('agent.newContent')}
          </motion.button>
        )}
      </AnimatePresence>

      {/* ── 用户消息导航（右上角悬浮按钮 + hover/click 展开列表 + 点击跳转）。
          替换原 4 按钮导航组与 ChatMinimap 竖条。 ─────────────────────────── */}
      <MessageUserNav
        rows={rows}
        userRowIndexes={userMessageIndices}
        visibleStart={visibleRange.start}
        onNavigate={navigateToRow}
      />
    </div>
  )
})

// 判断一行是否含 GenUI 面板（committed：迭代里有 uiMode 工具；live：流式 genuiContent）。
// estimateSize 据此返回更大的基数，缩小 estimate 与实际高度差距 → 滚动跳变小。
export function rowHasGenUI(row: ChatMessage): boolean {
  for (const iter of row.iterations ?? []) {
    for (const tool of iter.tools ?? []) {
      if (tool.uiMode) return true
    }
  }
  if ((row as ChatMessage & { genuiContent?: string }).genuiContent) return true
  return false
}

export function canRewindMessage(
  row: ChatMessage,
  index: number,
  compactBoundaryIndex: number,
): boolean {
  return row.role === 'user' &&
    !!row.timestamp &&
    row.persisted === true &&
    index > compactBoundaryIndex &&
    !isCompactMarker(row)
}

function isAtBottom(el: HTMLDivElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= EDGE_EPSILON
}
