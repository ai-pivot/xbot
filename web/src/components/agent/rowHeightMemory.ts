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
  /** 已挂载行的**真实尺寸发生变化**（ResizeObserver 回调）。快照不可信 ⇒ 只标脏，
   *  由调用方排一次「批量读真几何」的 flush（见下）。 */
  onResize?: () => void
  /** 该 index 在虚拟器记账里的**当前尺寸**（纯内存读，零 DOM）；用于 RO 路径的返回值
   *  —— 返回"不变"⇒ TanStack 的 `resizeItem` 早退，尺寸只由 flush 的真几何读取决定。 */
  currentSize?: (index: number) => number
}

/**
 * 记忆感知的 `measureElement` —— **行尺寸的唯一真相是"它自己的当前几何"**。
 *
 * ⛔ 2026-09-23 P0（正文互相穿插 / 两行压在同一 y）根因：**ResizeObserver 的
 * `entry.borderBoxSize` 是"观察时刻的快照"，不是当前几何**。乱序/滞后投递时它比真实
 * 尺寸**小**，而它被当作尺寸写回虚拟器（`resizeItem`）后：该行比真实高度矮 ⇒
 * 下一行 `translateY(start)` 偏小 ⇒ **画到它身上（两段文字压同一 y）**；随后 DOM 不再
 * 变化 ⇒ 没有下一次回调 ⇒ 错值**永久固化**（同一流水线里 `noDegenerateMeasureElement`
 * 已经改成"读当前几何"，但那只在**无 entry 的兜底分支**生效 —— 主路径仍信快照）。
 *
 * 现在：
 *  - **RO 回调（有 entry）**：绝不把快照当尺寸 —— 只 `onResize?.()` 标脏，返回虚拟器
 *    **当前记账尺寸**（不变 ⇒ `resizeItem` 早退），真实尺寸由调用方的 batch flush
 *    在 paint 前用真几何统一写回；
 *  - **挂载（无 entry）**：返回初值提示（记忆/估算），同样由 batch flush 校正；
 *  - 于是"尺寸"永远只来自**真几何**（一次批量读 → 一次批量写），快照、记忆、估算
 *    都只能作为**初值**，不可能覆盖真值。
 */
export function createHeightAwareMeasureElement(
  deps: MeasureElementDeps,
): (element: Element, entry: ResizeObserverEntry | undefined, instance: unknown) => number {
  return (element, entry, instance) => {
    const index = Number((element as HTMLElement).dataset?.index ?? -1)

    if (entry) {
      // ⛔ 不用 entry 的尺寸（可能是过期快照）—— 只标脏，交给 batch flush 读真几何。
      deps.onResize?.()
      const cur = deps.currentSize?.(index) ?? 0
      if (cur > 0) return cur
      // 记账里还没有尺寸（极罕见：RO 先于挂载测量到达）——退回真实测量。
      return deps.measure(element, undefined, instance)
    }

    // 挂载（无 RO entry）：**不读 DOM**，返回初值提示（记忆/估算）。真实高度由调用方的
    // batch flush 在 paint 前喂回 ⇒ 既不重叠也不留白，且零强制布局。
    const hinted = deps.hint?.(index)
    if (hinted !== undefined && hinted > 0) return hinted
    const size = deps.measure(element, entry, instance)
    const row = index >= 0 ? deps.lookup(index) : undefined
    const width = deps.width()
    if (row && width > 0 && size > 0) deps.memory.set(row.key, row.sig, width, size)
    return size
  }
}

