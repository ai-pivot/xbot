/**
 * 行高记忆（切会话性能修复，2026-09-17 Trace-20260918T000005）。
 *
 * 现场：切换会话时主线程 10.9s 里 —— `get offsetHeight` **21.5%**、
 * `getBoundingClientRect` **17.4%**（合计 ~39%），长任务 **1648 / 991 / 988 / 949ms**。
 * 调用链还原：`get offsetHeight ← TanStack RA.measureElement`（挂载 N 行逐行量高）、
 * `getBoundingClientRect ← (匿名)@index.js ← React commit`（TanStack 对每个"尺寸变了"的
 * 行回调 `shouldAdjustScrollPositionOnItemSizeChange`，我们内部读两个 rect）。
 * 每一量都在**上一行刚写完 DOM（markdown innerHTML）**之后 ⇒ 每次都是整表 reflow
 * ⇒ O(N²)，一次切会话就是 1.6s 级长任务。
 *
 * 修复：行高按「**行键 + 渲染内容指纹 + 布局宽度**」记忆。
 *  - 命中 ⇒ `measureElement` **直接返回记忆值，完全不碰 DOM**（不读 offsetHeight /
 *    offsetParent）⇒ 零强制布局；
 *  - 因为返回的尺寸与 TanStack 当前记录**相等**，`resizeItem` 会在 `size === item.size`
 *    处早退 ⇒ 连 `shouldAdjustScrollPositionOnItemSizeChange` 的 rect 环路也一并消失；
 *  - 指纹不匹配（内容变了 / 流式中的 partial 行）⇒ 照常真实测量并更新记忆。
 *
 * 正确性前提：**committed 历史行的内容不可变**（指纹不变 ⇒ 高度不变，宽度不变）；
 * partial（流式中）行的指纹逐帧变化 ⇒ 永不命中缓存 ⇒ 行为与修复前一致。
 * 宽度变化由 `observeElementRect` 侧清空记忆（高度不变性的前提）。
 */

export interface RowHeightLike {
  role?: string
  content?: string
  isPartial?: boolean
  iterations?: Array<{ content?: string; reasoning?: string; tools?: unknown[] }>
  toolCount?: number
}

/**
 * ⛔ 2026-09-18 P0（字符重合 / 行重叠）根因就在旧实现的两个缺陷：
 *  1) 只记**长度**（`content.length`、Σ iterations 长度）—— 长度相同而文本不同会命中陈旧高度；
 *  2) 结果按 **row 对象**记忆化（注释假设"行对象在帧间引用稳定"）—— 但 `derive.ts` 正是按
 *     源对象做 **WeakMap 恒等 memo**：**对象标识稳定、内容却会变**（流式增长、窗口化
 *     unmute、边界保留工具、折叠展开…）⇒ 指纹被永久冻结在首帧值 ⇒ 记忆高度永远偏小
 *     ⇒ `resizeItem` 在 `size === item.size` 处早退不校正 ⇒ 虚拟行
 *     `translateY(item.start)` 偏小 ⇒ **下一行画到上一行身上（两段文字压同一 y）**。
 *
 * 修法：① 指纹纳入**内容哈希**（不再只看长度）；② 记忆化必须按**输入**校验
 * （内容字符串 / iterations 引用 / 各计数）——任一输入变了就重算。
 */
interface SigCacheEntry {
  content: string
  /** 逐项的 content/reasoning（**就地改写**元素时数组/对象引用都不变，必须逐项比） */
  iterParts: readonly (readonly [string, string])[]
  itersCount: number
  iterLen: number
  toolCount: number
  partial: boolean
  sig: string
}

const sigCache = new WeakMap<object, SigCacheEntry>()

/** FNV-1a 32-bit：便宜的内容指纹（不把整段文本拼进签名字符串，避免大分配）。 */
function hashString(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

function mixHash(h: number, v: number): number {
  return Math.imul(h ^ v, 0x01000193) >>> 0
}

/** 渲染内容指纹：**内容哈希** + 计数；记忆化按输入校验（内容/引用变了必重算）。 */
export function rowSignature(row: RowHeightLike): string {
  const content = row.content ?? ''
  const iters = row.iterations ?? []
  const partial = row.isPartial === true
  const toolCount = row.toolCount ?? 0
  let iterLen = 0
  for (const it of iters) {
    iterLen += (it.content?.length ?? 0) + (it.reasoning?.length ?? 0)
  }

  const hit = sigCache.get(row as object)
  if (
    hit !== undefined &&
    hit.content === content &&
    hit.itersCount === iters.length &&
    hit.iterParts.length === iters.length &&
    iters.every((it, i) => {
      const p = hit.iterParts[i]
      return p !== undefined && p[0] === (it.content ?? '') && p[1] === (it.reasoning ?? '')
    }) &&
    hit.iterLen === iterLen &&
    hit.toolCount === toolCount &&
    hit.partial === partial
  ) {
    return hit.sig
  }

  let h = 0x811c9dc5
  for (const it of iters) {
    h = mixHash(h, hashString(it.content ?? ''))
    h = mixHash(h, hashString(it.reasoning ?? ''))
    h = mixHash(h, it.tools?.length ?? 0)
  }
  const sig = [
    row.role ?? '',
    partial ? 'p' : 'c',
    content.length,
    hashString(content).toString(36),
    iters.length,
    iterLen,
    h.toString(36),
    toolCount,
  ].join('|')
  sigCache.set(row as object, {
    content,
    iterParts: iters.map((it) => [it.content ?? '', it.reasoning ?? ''] as const),
    itersCount: iters.length,
    iterLen,
    toolCount,
    partial,
    sig,
  })
  return sig
}

export interface RememberedHeight {
  height: number
  sig: string
  width: number
}

export interface RowHeightMemory {
  /** 命中（键、指纹、宽度都一致且高度有效）⇒ 返回记忆高度；否则 undefined */
  get(key: string, sig: string, width: number): number | undefined
  set(key: string, sig: string, width: number, height: number): void
  clear(): void
  size(): number
}

export function createRowHeightMemory(limit = 3000): RowHeightMemory {
  const map = new Map<string, RememberedHeight>()
  return {
    get(key, sig, width) {
      const hit = map.get(key)
      if (!hit || hit.sig !== sig || hit.width !== width) return undefined
      return hit.height > 0 ? hit.height : undefined
    },
    set(key, sig, width, height) {
      if (!(height > 0)) return
      map.set(key, { height, sig, width })
      if (map.size > limit) map.clear()
    },
    clear() {
      map.clear()
    },
    size() {
      return map.size
    },
  }
}

/** live 区/未定型行的宽度（observeElementRect 记录；宽度变化 ⇒ 记忆作废）。 */
export function createWidthTracker(): {
  observe(width: number): void
  current(): number
  onChange(fn: () => void): void
} {
  let w = 0
  let cb: (() => void) | undefined
  return {
    observe(width: number) {
      const next = Math.round(width)
      if (next > 0 && next !== w) {
        const changed = w !== 0
        w = next
        if (changed) cb?.()
      }
    },
    current: () => w,
    onChange(fn: () => void) {
      cb = fn
    },
  }
}

export interface MeasureElementDeps {
  /** 挂载时的**初值提示**（记忆命中值或估算）——**绝不允许读 DOM**。
   *  真实高度由调用方在同一 commit 的 layout effect 里"先批量读、后批量写"校正，
   *  因此挂载路径可以零强制布局，而画面仍是准的（paint 前已校正）。 */
  hint?: (index: number) => number | undefined
  /** index → { key, sig }（由调用方经 rowsRef 现读） */
  lookup: (index: number) => { key: string; sig: string } | undefined
  /** 真实测量（TanStack defaultMeasureElement 的包装）。**instance 与我们无关**：
   *  TanStack 的 instance 类型是泛型（`Virtualizer<any, TItemElement>`），把它写进本模块
   *  签名会引入无谓的泛型摩擦 —— 只声明我们不用的那个参数即可。 */
  measure: (element: Element, entry: ResizeObserverEntry | undefined, instance: unknown) => number
  memory: RowHeightMemory
  width: () => number
}

/**
 * 记忆感知的 `measureElement`：
 * 命中 ⇒ **零 DOM 读**直接返回（这是切会话/回访旧会话的关键路径）；否则真实测量并记忆。
 *
 * ⛔ 但**每个元素实例只允许"首次测量"走缓存**（`measuredOnce`）：
 * 折叠/展开、图片加载、mermaid 渲染、字体替换都会**在不改行内容的前提下改变高度** ——
 * 若之后仍返回旧缓存值，这些真实的高度变化会被永久吞掉（虚拟列表总高与实际不符）。
 * 因此：元素实例的第一次测量（挂载爆发期，含 RO 的初始回调）可命中缓存；
 * 之后的每一次测量（RO 因真实尺寸变化而回调）一律真实读取并刷新缓存。
 */
/**
 * 记忆感知的 `measureElement` —— **只信浏览器的真实尺寸**。
 *
 * ⛔ 根因修复（2026-09-18 P0 字符重合 / 行重叠）：旧实现有两条"说谎"路径，且都与
 * 元素类型无关，因此**无法靠按类型打补丁解决**：
 *   ① **首次测量直接返回记忆值** —— 内容/宽度指纹只要不完全等价，就会返回偏小的
 *      高度；TanStack `resizeItem` 在 `size === item.size` 处早退 ⇒ 不再校正 ⇒
 *      下一行 `translateY(item.start)` 偏小 ⇒ **两行压在同一 y**；
 *   ② 依赖**估算**定位已渲染的行 —— 任何"高度与字符数不成比例"的元素（表格 /
 *      代码块 / mermaid / 图片 / KaTeX / 嵌套列表…）都会被低估 ⇒ 同样重叠。
 *
 * 现在：ResizeObserver 回调**自带真实块尺寸**（`borderBoxSize`）——**零 DOM 读、
 * 零强制布局，完全免费**；挂载时（无 entry）才真实测量。`memory` 仅作为
 * `estimateSize` 的**初值提示**，**绝不**作为已渲染行的定位依据。
 */
export function createHeightAwareMeasureElement(
  deps: MeasureElementDeps,
): (element: Element, entry: ResizeObserverEntry | undefined, instance: unknown) => number {
  return (element, entry, instance) => {
    const index = Number((element as HTMLElement).dataset?.index ?? -1)
    const row = index >= 0 ? deps.lookup(index) : undefined
    const width = deps.width()

    const observed = readObservedBlockSize(entry)
    if (observed > 0) {
      if (row && width > 0) deps.memory.set(row.key, row.sig, width, observed)
      return observed
    }
    // 挂载（无 RO entry）：**不读 DOM**，返回初值提示（记忆/估算）。真实高度由
    // 调用方的批量 pass 在 paint 前喂回 ⇒ 既不重叠也不留白，且零强制布局。
    const hinted = deps.hint?.(index)
    if (hinted !== undefined && hinted > 0) return hinted
    const size = deps.measure(element, entry, instance)
    if (row && width > 0 && size > 0) deps.memory.set(row.key, row.sig, width, size)
    return size
  }
}

/** ResizeObserver 回调自带的真实块尺寸（免费；兼容数组/单值与新旧字段名）。 */
export function readObservedBlockSize(entry: ResizeObserverEntry | undefined): number {
  if (!entry) return 0
  const border = entry.borderBoxSize as unknown
  const borderSize = Array.isArray(border)
    ? (border[0] as { blockSize?: number } | undefined)
    : (border as { blockSize?: number } | undefined)
  if (borderSize && typeof borderSize.blockSize === 'number' && borderSize.blockSize > 0) {
    return borderSize.blockSize
  }
  const content = entry.contentBoxSize as unknown
  const contentSize = Array.isArray(content)
    ? (content[0] as { blockSize?: number } | undefined)
    : (content as { blockSize?: number } | undefined)
  if (contentSize && typeof contentSize.blockSize === 'number' && contentSize.blockSize > 0) {
    return contentSize.blockSize
  }
  const h = entry.contentRect?.height
  return typeof h === 'number' && h > 0 ? h : 0
}
