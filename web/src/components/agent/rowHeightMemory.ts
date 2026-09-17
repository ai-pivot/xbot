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

const sigCache = new WeakMap<object, string>()

/** 渲染内容指纹（按 row 对象记忆化：行对象在帧间引用稳定，切会话时是新对象 ⇒ 只算一次）。 */
export function rowSignature(row: RowHeightLike): string {
  const cached = sigCache.get(row as object)
  if (cached !== undefined) return cached
  const iters = row.iterations ?? []
  let iterLen = 0
  for (const it of iters) {
    iterLen += (it.content?.length ?? 0) + (it.reasoning?.length ?? 0)
  }
  const sig = [
    row.role ?? '',
    row.isPartial ? 'p' : 'c',
    (row.content ?? '').length,
    iters.length,
    iterLen,
    row.toolCount ?? 0,
  ].join('|')
  sigCache.set(row as object, sig)
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
export function createHeightAwareMeasureElement(
  deps: MeasureElementDeps,
): (element: Element, entry: ResizeObserverEntry | undefined, instance: unknown) => number {
  const measuredOnce = new WeakSet<Element>()
  return (element, entry, instance) => {
    const index = Number((element as HTMLElement).dataset?.index ?? -1)
    const row = index >= 0 ? deps.lookup(index) : undefined
    const width = deps.width()
    const firstMeasure = !measuredOnce.has(element)
    measuredOnce.add(element)

    if (row && width > 0 && firstMeasure) {
      const remembered = deps.memory.get(row.key, row.sig, width)
      if (remembered !== undefined) return remembered
    }
    const size = deps.measure(element, entry, instance)
    if (row && width > 0 && size > 0) deps.memory.set(row.key, row.sig, width, size)
    return size
  }
}
