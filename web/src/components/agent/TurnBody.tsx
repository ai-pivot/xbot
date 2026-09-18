/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * 唯一渲染形态（用户要求，2026-09-12）：**每个迭代独立渲染**（T 折叠 / O 文本 /
 * C 工具 pills）。跨迭代合并工具（mergeTools）、折叠级别（CollapseLevel）、
 * 「已处理 N 次迭代」摘要行已【彻底删除】。
 *
 * PERF-1（Trace-20260912T100816）：已提交迭代的渲染抽进 <CommittedTurn>（memo 边界），
 * 流式帧只重渲染 LiveIteration —— **React 侧**代价与迭代数无关。
 *
 * PERF-2（2026-09-13「手机上 iter 多了还是很卡」）：交互成本 ∝ **DOM 规模**
 * （移动端 + CPU 4×：N=15 → 653 节点/样式失效 89ms/打开设置 307ms；
 * N=60 → 2348 节点/262ms/655ms；`contain: layout|paint` 三变体无差别）⇒ **迭代级
 * 窗口化**：远离视口的块只留外壳 + 固定高度，内容卸载；每屏真实挂载的迭代内容由
 * **视口**决定，与 N 无关（实测 N=15 → 2 块；N=60 → 5 块；节点 2348→~350）。
 *
 * PERF-3（2026-09-13「busy 且 turn 特别长（几千 iter）时手机端必卡，切会话/刷新都没用」）：
 * PERF-1 只挡住了"**子组件重渲染**"，没挡住 **CommittedTurn 自己每帧创建 N 个元素**：
 * 每个渲染帧 `contiguous.map(...)` → N 个 <IterationBlock> 元素 + React reconcile
 * 克隆 N 个 fiber + GC。busy 时每秒几十帧、N=3000 ≈ **9 万元素/秒** → 主线程饱和，
 * 于是 busy 必卡、turn 越长越卡、刷新无效（流还在继续）。**几何/高度模型保持不变**
 * （每块一个外壳；离屏且已结算的块卸载内容、用实测高度占位），只把每帧分配量从
 * O(N) 降到 O(新增/变化)：
 *   1. **分块冻结**：迭代按 `COMMITTED_CHUNK_SIZE` 塞进 memo 的 <CommittedChunk>；
 *      已冻结 chunk 的元素对象与 props（items/mutedHeights 数组）逐帧**原样复用** →
 *      React 在该子树直接 bail（连 fiber 都不重建）。父层每帧只处理 N/64 个 chunk 元素。
 *   2. **决策脏标记**：窗口化决策（muted / 占位高度）只在"决策输入变化"的帧
 *      （IO / RO / settle / 复核 / verifying）重算；其余帧只做一次 O(N) **指针比较**
 *      （零分配、零对象创建）。
 *   3. **hKey 缓存**：`turnID:iteration` 字符串按迭代对象身份（WeakMap）缓存，
 *      不再每帧为每个迭代重建。
 *   4. **连续前缀增量扫描**：`continuousIterations` 的增量版，只扫"新增的尾部"
 *      （锚点校验已确认前缀；不符即退化为全量扫描，语义与 canonical 实现一致）。
 *
 * ⚠️ 正确性铁律（三条都是真实事故的教训）：
 *   1. **高度缓存/复核裁决的身份必须是"内容"**（`(会话, turnID, iteration, 布局宽度)`，
 *      见下），**不是**"组件实例"，也**不是**裸 `turnID:iteration`：
 *      - 裸 key 的模块级缓存 → 跨会话撞车 → 空块（`76b731de`）；
 *      - 纯实例作用域 → 任何**扰动布局**的交互（手机端开工具页会把 AgentPanel 外壳
 *        `display:none`）让消息行整体卸载/重挂（实例销毁）→ 返回时无实测高度 ⇒
 *        每个迭代块的内容全部重新挂载 + markdown 全量重解析（2026-09-13 实测
 *        nodes 528→3708、muted 318→0、一次交互 320 个 `.iter-block` 卸了又重挂）。
 *      ⇒ `heightScope = 会话身份 + 布局宽度`，经 `sharedIterationHeightTracker` 跨
 *      重挂载复用（`MessageList` 负责构造并透传）。
 *   2. **只有 settled 的高度才允许冻结** + **冻结后一次性复核**（首版把瞬态测量
 *      当成可信高度 → 卸载后内容永久消失）。`iterationHeight.ts` 里 settle 语义
 *      要求同值连续两次测量（间隔 ≥200ms）；`scheduleSettleSample` 负责补第二次
 *      采样（ResizeObserver 只在尺寸变化时回调，不会自己再报一次）；冻结后
 *      `VERIFY_DELAY_MS` 再挂一帧实测，不符即解冻重稳。复核裁决也在 tracker 里
 *      （内容身份作用域）—— 否则重挂载后裁决为空 ⇒ 谁都不能冻结 ⇒ 白重挂一次。
 *   3. 只对迭代号可解析（Number.isFinite）的块窗口化；从未渲染过的块必须挂载
 *      （否则永远量不到高度）；jsdom / 无 IO+RO 环境退化为全量渲染（分块/复用
 *      逻辑照常生效，只是永不 muted）。
 */
import { memo, type ReactElement, useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { reasoningKey } from './reasoningOpenState'
import { continuousIterations } from './progressStore'
import {
  ITERATION_HEIGHT_SETTLE_MS,
  createIterationHeightTracker,
  hasStableTurnKey,
  iterationHeightKey,
  sharedIterationHeightTracker,
  type IterationHeightTracker,
} from './iterationHeight'
import { createSettleScheduler, type SettleScheduler } from './iterationSettleScheduler'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

interface TurnBodyProps {
  iterations: WebIteration[]
  /** Live progress for an in-flight turn; null for committed history. */
  liveProgress?: ProgressSnapshot | null
  /** TurnID for data-attribute debugging (data-turn-id on each block). */
  turnID?: number
  /**
   * 高度/复核裁决的作用域 = 「会话身份 + 布局宽度」（由 `MessageList` 构造）。
   * 省略时退化为实例作用域（独立渲染 / 单测路径）。
   */
  heightScope?: string
}

interface CommittedTurnProps {
  /** 连续前缀迭代（增量扫描于 iterations —— 无变化时引用稳定）。 */
  contiguous: WebIteration[]
  turnID?: number
  heightScope?: string
}

/** 窗口化可用环境判定（jsdom / 老浏览器没有 IO/RO → 退化为全量渲染）。 */
const canWindow = (): boolean =>
  typeof IntersectionObserver !== 'undefined' && typeof ResizeObserver !== 'undefined'

/** 冻结后的复核延迟：足够覆盖字体/异步 markdown 的定形时间。 */
const VERIFY_DELAY_MS = 400

/**
 * 「这次测量是不是一次真实测量」—— 元素必须在文档里、有渲染盒、且有正的宽高。
 *
 * ⛔ 没有布局的测量（面板被移动端外壳 `display:none`、元素已脱离文档、宽高为 0）
 * 永远不能成为高度缓存 / 结算 / 冻结的依据：`display:none` 时报的是 0，混进
 * "同值两次测量"就会把内容冻成空块（用户报告：「切换会话时正在 stream 思考 →
 * 思考之前的已提交内容整段不渲染」）。这类结果一律忽略，等元素可见后由 RO 重报。
 */
function isLayoutable(el: HTMLElement, rect: { width: number; height: number }): boolean {
  // ⛔ 不读 `offsetParent`（2026-09-18 dev-build trace：`get offsetParent` 0.16s，
  // 同一「强制同步布局」家族）。它当初只是为了排除 `display:none` —— 而这类元素
  // 的尺寸读数本就是 0，`rect.width/height > 0` 已经把它排除；`isConnected` 覆盖
  // 「已脱离文档」。少一次布局读，语义不变。
  return el.isConnected && rect.width > 0 && rect.height > 0
}

/**
 * 分块冻结单元大小（PERF-3）：每 64 个迭代一个 chunk。
 *
 * 为什么必须分块（只"复用迭代元素对象"还不够）：即使每个 <IterationBlock> 元素
 * 对象被复用，父层每帧仍要把 N 个子元素交给 React reconcile（N 次 fiber 克隆 +
 * key 比较 = O(N) 分配）。把 64 个迭代塞进一个 memo 的 <CommittedChunk> 后，父层
 * 每帧只面对 N/64 个 chunk 元素；已冻结 chunk 连 props 都不变 → memo 直接 bail，
 * 其内部 64 个迭代连 fiber 都不重建。⇒ 每帧代价 = 尾部 chunk（≤64）+ O(N/64)。
 */
const COMMITTED_CHUNK_SIZE = 64

interface IterationBlockProps {
  iter: WebIteration
  turnID?: number
  /** true = 窗口化卸载（只留外壳 + `height` 固定高度）；false = 渲染内容。 */
  muted: boolean
  /** muted 时的占位高度（**必须**是实测值，绝不允许常数）。 */
  mutedHeight?: number
  register: (hKey: string, el: HTMLDivElement | null) => void
}

/**
 * IterationBlock — 单个迭代块（外壳 + 内容/占位）。
 *
 * memo 的 props 只有 `iter`（chunk 内引用稳定）、`turnID`、`muted`/`mutedHeight`
 * （窗口化决策，仅跨越视口边界时翻转）与稳定的 `register` —— 流式帧不会重渲染已提交
 * 迭代的内容（turn_perf.test.tsx 守护）。
 */
const IterationBlock = memo(function IterationBlock({
  iter,
  turnID,
  muted,
  mutedHeight,
  register,
}: IterationBlockProps) {
  const elRef = useRef<HTMLDivElement | null>(null)
  const hKey = iterationHeightKey(turnID, iter.iteration)
  const setRef = useCallback(
    (el: HTMLDivElement | null) => {
      elRef.current = el
      register(hKey, el)
    },
    [hKey, register],
  )

  return (
    <div
      ref={setRef}
      className="iter-block"
      data-iter-id={iter.iteration}
      data-turn-id={turnID}
      data-height-key={hKey}
      data-window-muted={muted ? 'true' : undefined}
      style={muted ? { height: mutedHeight, overflow: 'hidden' } : undefined}
    >
      {!muted && (
        <>
          <IterationGroup
            iteration={iter}
            reasoningStateKey={reasoningKey(turnID, iter.iteration ?? 0)}
          />
          {iter.subAgents && iter.subAgents.length > 0 && (
            <SubAgentProgressTree nodes={iter.subAgents} />
          )}
        </>
      )}
    </div>
  )
})

interface CommittedChunkProps {
  /** 冻结后的迭代切片：**一旦冻结，数组引用不再变化**（尾部 chunk 除外）。 */
  items: WebIteration[]
  turnID?: number
  /** 与 items 一一对应：undefined = 渲染内容；number = 窗口化卸载并以此高度占位。 */
  mutedHeights: (number | undefined)[]
  register: (hKey: string, el: HTMLDivElement | null) => void
}

/**
 * CommittedChunk — 64 个迭代的冻结单元（PERF-3 的 memo 边界）。
 *
 * props 全引用比较：`items`/`mutedHeights` 数组一旦冻结就不再变化，`turnID`/
 * `register` 恒定 ⇒ 流式帧、以及窗口化决策未变的脏帧，本子树整体 bail（不重建
 * 内部 64 个 <IterationBlock>）。
 */
const CommittedChunk = memo(function CommittedChunk({
  items,
  turnID,
  mutedHeights,
  register,
}: CommittedChunkProps) {
  return (
    <>
      {items.map((iter, i) => (
        <IterationBlock
          key={iter.iteration ?? i}
          iter={iter}
          turnID={turnID}
          muted={mutedHeights[i] !== undefined}
          mutedHeight={mutedHeights[i]}
          register={register}
        />
      ))}
    </>
  )
})

/** 一个已冻结/正在流式的 chunk 的缓存条目（实例作用域）。 */
interface ChunkEntry {
  /** 该 chunk 的迭代切片（引用稳定 = 可复用）。 */
  items: WebIteration[]
  /** 与 items 一一对应的占位高度（undefined = 渲染内容）。 */
  mutedHeights: (number | undefined)[]
  /** 上一帧创建的元素对象（props 未变时原样复用 → React bail）。 */
  element: ReactElement
}

// ── 连续前缀的增量扫描（PERF-3 #4） ─────────────────────────────────────────

/** 锚点步长：每 32 项记一个元素引用，用于廉价校验"已确认前缀没被换掉"。 */
const CONTIGUOUS_ANCHOR_STRIDE = 32

interface ContiguousScan {
  /** 本次扫描对应的输入数组。 */
  input: WebIteration[]
  /** 连续前缀（= 渲染输入）。 */
  out: WebIteration[]
  /** out 内每 ANCHOR_STRIDE 项记一个引用（校验前缀未变）。 */
  anchors: WebIteration[]
  /** 已确认的前缀长度（恒等于 out.length）。 */
  scanned: number
  /** 是否已遇到断点（此后新增元素不再影响结果）。 */
  stopped: boolean
}

/** 记录锚点：out 内每 ANCHOR_STRIDE 项的第一个元素引用（anchors[0] = input[0]）。 */
function buildAnchors(iters: WebIteration[], upto: number): WebIteration[] {
  const anchors: WebIteration[] = []
  for (let i = 0; i < upto; i += CONTIGUOUS_ANCHOR_STRIDE) anchors.push(iters[i])
  return anchors
}

/**
 * 全量扫描 = 直接复用 canonical 实现（`progressStore.continuousIterations`），
 * 保证回退路径的语义与既有实现/测试单一来源。
 */
function fullScan(iters: WebIteration[]): ContiguousScan {
  const out = continuousIterations(iters)
  return {
    input: iters,
    out,
    anchors: buildAnchors(iters, out.length),
    scanned: out.length,
    stopped: out.length < iters.length,
  }
}

/**
 * 从已有前缀继续扫描（逻辑与 `progressStore.continuousIterations` 的循环逐行同构：
 * 同号记录跳过、遇到断点即停）。只在"已确认前缀未变 + 输入变长"时调用。
 */
function scanTail(iters: WebIteration[], out: WebIteration[]): ContiguousScan {
  let stopped = false
  for (let i = out.length; i < iters.length; i++) {
    const prev = out[out.length - 1]
    const curr = iters[i]
    if (curr.iteration === prev.iteration) continue
    if (curr.iteration !== prev.iteration + 1) {
      stopped = true
      break
    }
    out.push(curr)
  }
  return {
    input: iters,
    out,
    anchors: buildAnchors(iters, out.length),
    scanned: out.length,
    stopped,
  }
}

/** 已确认前缀 [0, scanned) 是否仍是同一批元素（锚点 + 首项 + 断点前一项）。 */
function prefixUnchanged(prev: ContiguousScan, iters: WebIteration[]): boolean {
  if (prev.scanned === 0) return false // 无缓存前缀可用 → 交给全量扫描
  if (prev.scanned > iters.length) return false
  if (iters[0] !== prev.anchors[0]) return false
  if (iters[prev.scanned - 1] !== prev.input[prev.scanned - 1]) return false
  for (let k = 1; k < prev.anchors.length; k++) {
    if (iters[k * CONTIGUOUS_ANCHOR_STRIDE] !== prev.anchors[k]) return false
  }
  return true
}

/**
 * 增量版 `continuousIterations`：输入在实践中**只追加**（appendIterations /
 * mergeIterations 的 union 只增语义，见 reduce.ts 的 I4；同号快照是"权威覆盖"，
 * 不会换成别号），因此只需扫新增的尾部。已确认前缀用锚点引用廉价校验，任何一项
 * 不符（数组变短 / 前缀被换）即退化为全量扫描 —— 语义与 canonical 实现一致。
 */
function extendContiguous(prev: ContiguousScan | null, iters: WebIteration[]): ContiguousScan {
  if (prev === null) return fullScan(iters)
  if (iters === prev.input) return prev
  if (iters.length >= prev.scanned && prefixUnchanged(prev, iters)) {
    if (prev.stopped || iters.length === prev.scanned) {
      // 已断（尾部进不了前缀）或没有新增 → 结果不变，只更新 input 引用
      return {
        input: iters,
        out: prev.out,
        anchors: prev.anchors,
        scanned: prev.scanned,
        stopped: prev.stopped,
      }
    }
    return scanTail(iters, prev.out.slice())
  }
  return fullScan(iters)
}

/**
 * CommittedTurn — 已提交迭代的唯一渲染点（memo 边界 + 迭代级窗口化 + 冻结复核 +
 * 分块冻结/元素复用）。
 */
const CommittedTurn = memo(function CommittedTurn({ contiguous, turnID, heightScope }: CommittedTurnProps) {
  const [, bumpTick] = useReducer((n: number) => n + 1, 0)
  const nearRef = useRef<Set<number>>(new Set())
  const elements = useRef<Map<string, HTMLDivElement>>(new Map())
  const roRef = useRef<ResizeObserver | null>(null)
  const ioRef = useRef<IntersectionObserver | null>(null)
  /**
   * 高度/结算/复核裁决：**内容身份作用域**（`heightScope` = 会话身份 + 布局宽度）。
   *
   * 身份必须是"内容"而不是"组件实例"：实例态在行重挂载时全部丢失 → 返回时无实测
   * 高度 ⇒ 每个迭代块的内容重新挂载 + markdown 全量重解析（见文件头铁律 1）。
   *
   * ⛔ 但"内容身份"的前提是 `turnID:iteration` 在该作用域内**唯一**（同一 turn 的同一
   * 迭代号只对应一块内容）。`turnID` 是每会话独立编号，而 **legacy 行（turnID 缺失/0）
   * 不满足这个前提** —— 同一会话里多条 legacy 行的块会共用 `0:iteration` → 互相读到
   * 对方的高度（冻结成错块）。判据与 `MessageList.getItemKey` 的"稳定 turn 键"一致：
   * `0 < turnID < MAX_SAFE_INTEGER`；不满足就退化为**实例作用域**（= 本行自己，
   * 绝不与别的行共享 key —— 这正是这次改动之前的行为，不会更糟）。
   */
  const scoped = heightScope !== undefined && hasStableTurnKey(turnID)
  const localTracker = useRef<IterationHeightTracker | null>(null)
  if (!scoped && localTracker.current === null) {
    localTracker.current = createIterationHeightTracker()
  }
  const tracker: IterationHeightTracker = scoped
    ? sharedIterationHeightTracker(heightScope as string)
    : (localTracker.current as IterationHeightTracker)
  const verifyTimers = useRef<Map<string, number>>(new Map())
  /** 正在复核（临时重新挂载内容）的 key。 */
  const [verifying, setVerifying] = useState<ReadonlySet<string>>(() => new Set())
  /** 待补充的"第二次一致采样"定时器（合并式：一个定时器 + 每 key deadline，见 iterationSettleScheduler.ts）。 */
  const settleSchedulerRef = useRef<SettleScheduler | null>(null)
  /** 采样实现经 ref 读**最新**的 tracker/invalidate（调度器只建一次，不能钉住旧引用）。
   *  ⛔ 初值必须是 null：`invalidate` 在本组件里声明于此处**之后**（TDZ），
   *  在渲染期引用它 → `Cannot access 'invalidate' before initialization`（实测炸 5 个测试）。 */
  const settleDepsRef = useRef<{ tracker: IterationHeightTracker; invalidate: () => void } | null>(null)
  useEffect(() => {
    settleDepsRef.current = { tracker, invalidate }
  })
  if (settleSchedulerRef.current === null) {
    settleSchedulerRef.current = createSettleScheduler({
      delayMs: ITERATION_HEIGHT_SETTLE_MS + 50,
      onSample: (hKey) => {
        const deps = settleDepsRef.current
        if (!deps) return
        const el = elements.current.get(hKey)
        if (!el) return
        const rect = el.getBoundingClientRect()
        if (!isLayoutable(el, rect)) return
        const res = deps.tracker.record(hKey, rect.height, performance.now(), true)
        if (res.settled || res.changed) deps.invalidate()
      },
    })
  }
  /** 分块元素缓存：**实例作用域**（元素对象天然绑定这一次挂载，不跨挂载复用）。 */
  const chunkCache = useRef<Map<number, ChunkEntry>>(new Map())
  /**
   * 决策脏标记（PERF-3）：窗口化决策的**任何一个输入**变化都要置位 ——
   * IO（near）/ RO（高度）/ settle 采样 / 冻结复核 / verifying。脏帧才重算
   * heights；干净帧只做 O(N) 指针比较并复用冻结 chunk（零分配）。
   */
  const dirty = useRef(true)
  /** `turnID:iteration` 字符串按迭代对象身份缓存（免掉每帧 N 次字符串拼接）。 */
  const hKeyCache = useRef(new WeakMap<WebIteration, { turnID: number | undefined; key: string }>())
  const turnIDRef = useRef<number | undefined>(turnID)
  /** 上一帧的作用域（作用域变化 ⇒ 高度/裁决来源整体换人，分块决策必须重算）。 */
  const scopeRef = useRef(heightScope)
  /** 本帧新冻结、待复核的 key（渲染期收集，effect 里建定时器）。 */
  const pendingVerify = useRef<string[]>([])

  /** 任何"决策输入"变化 → 置脏 + 触发一次重渲染。 */
  const invalidate = useCallback(() => {
    dirty.current = true
    bumpTick()
  }, [])

  /** `turnID:iteration` —— 按迭代对象身份缓存（对象不可变，iteration 不会变）。 */
  const hKeyFor = (iter: WebIteration): string => {
    const cached = hKeyCache.current.get(iter)
    if (cached !== undefined && cached.turnID === turnID) return cached.key
    const key = iterationHeightKey(turnID, iter.iteration)
    hKeyCache.current.set(iter, { turnID, key })
    return key
  }

  /**
   * 本帧该迭代的占位高度：undefined = 渲染内容；number = 窗口化卸载（实测高度）。
   * 决策：canWindow && 高度已测 && settled && **已通过"内容已挂载"的复核确认** &&
   * 不在视口附近 && 不在复核中。
   *
   * ⛔ 为什么 settled 还不够（2026-09-13 回归）：settle 只证明"连续两次测量一致"，
   * 而**首帧的瞬态/压扁高度同样能连续两次一致**（手机端切换会话时内容尚未定形：
   * 字体/异步 markdown/图片）。那时冻结 → 内容卸载 → 盒子被钉在压扁高度 → RO 再也
   * 报不出变化 → 400ms 复核是唯一纠错通路。所以冻结必须再要求一次**内容真的挂回来
   * 后的实测确认**（`tracker.isVerified`，见下面的复核 effect）—— 裁决同样存在
   * tracker（内容作用域）里，否则重挂载后裁决为空 ⇒ 谁都不能冻结 ⇒ 白重挂一次。
   */
  const mutedHeightFor = (
    iter: WebIteration,
    win: boolean,
    near: Set<number>,
    verifyingSet: ReadonlySet<string>,
  ): number | undefined => {
    if (!win || !Number.isFinite(iter.iteration)) return undefined
    const hKey = hKeyFor(iter)
    const height = tracker.get(hKey)
    if (height === undefined || !tracker.isSettled(hKey)) return undefined
    if (!tracker.isVerified(hKey)) return undefined
    if (near.has(iter.iteration as number)) return undefined
    if (verifyingSet.has(hKey)) return undefined
    return height
  }

  /**
   * 安排一次 settle 采样：`ITERATION_HEIGHT_SETTLE_MS` 后重新量一次。
   * 这是"同值二次测量"的来源 —— 没有它，RO 只报一次尺寸，永远无法结算
   * （2026-09-13 实测：窗口化因此完全失效，mountedContents 15/15、60/60）。
   *
   * ⛔ 采样同样只认**有布局的测量**：无渲染盒（面板 display:none / 元素已脱离文档）
   * 时直接放弃这次采样，等元素可见后由 RO 重新报告（而不是把 0 当成高度）。
   */
  const scheduleSettleSample = useCallback((hKey: string) => {
    // ⚠️ 语义仍是 debounce（每次变化都**重排**采样）：尺寸"变化"可能落在上一次采样
    // **待发期间** —— 那时 `record` 已把 observedAt 刷新到此刻，若因"已有定时器"直接
    // 返回，采样就会在变化后 <200ms 触发 → 判不出 settled，且**此后再无触发**
    // （高度已稳定、RO 不再报变化）→ 该块永不结算 → 永不复核 → 永不冻结
    // （2026-09-13 实测：窗口化整段失效，20 块全量挂载）。
    // ⇒ 仍然每次变化都重排（deadline 推后），但由调度器**合并成单个定时器**：
    // 旧实现每 key 一个定时器、每次变化 clear+set，RO 流式期间 ~7.5k 次/秒的
    // install/remove，clearTimeout 独占 21% CPU（Trace-20260918T000005）。
    settleSchedulerRef.current?.schedule(hKey)
  }, [])

  // 观察器只建一次（整组块共用一个 RO / 一个 IO）。
  useEffect(() => {
    if (!canWindow()) return
    const io = new IntersectionObserver(
      (entries) => {
        let changed = false
        for (const e of entries) {
          const target = e.target as HTMLElement
          const n = Number(target.dataset.iterId)
          if (!Number.isFinite(n)) continue
          // ⛔ 「没有布局的观测」不是观测：容器被隐藏（手机端开工具页把 AgentPanel 外壳
          // 置 display:none）时，所有目标都报"不可见"，而这条判定**只说明容器被藏了**，
          // 不说明块真的离开了视口。若照单全收，near 集合会被瞬时清空 → 重新可见时
          // 每个块都要等 IO 再报一次才能挂回内容（闪一帧空白）。
          // ⇒ 只在目标**真的渲染着**（有渲染盒、宽高非 0）时才采信 intersecting 判定。
          // 用 IO 自带的 boundingClientRect（不强制布局）。
          if (!isLayoutable(target, e.boundingClientRect)) continue
          const was = nearRef.current.has(n)
          if (e.isIntersecting && !was) {
            nearRef.current.add(n)
            changed = true
          } else if (!e.isIntersecting && was) {
            nearRef.current.delete(n)
            changed = true
          }
        }
        if (changed) invalidate()
      },
      // 视口上下各扩 1.2 屏 —— 滚动时下一批块已挂载好，避免"滚到才渲染"的白屏。
      { rootMargin: '120% 0px 120% 0px' },
    )
    const ro = new ResizeObserver((entries) => {
      let changed = false
      for (const e of entries) {
        const el = e.target as HTMLElement
        const key = el.dataset.heightKey
        if (!key) continue
        // ⛔ 「没有布局的测量」不是测量：display:none（面板被移动端外壳隐藏）、元素
        // 脱离文档、宽高为 0 —— 一律忽略（不写缓存、不结算、不放行冻结）。已有的
        // 高度来自上一次真实测量，仍然可信；元素可见后 RO 会重新报告并按需 unsettle。
        if (!isLayoutable(el, e.contentRect)) continue
        // 高度变了 → 复核裁决一并作废（`record` 内部已经清），必须重新稳定 + 重新复核。
        const res = tracker.record(key, e.contentRect.height, performance.now(), true)
        if (res.changed) {
          changed = true
          scheduleSettleSample(key)
        } else if (res.settled) {
          changed = true // 刚结算 → 可以进入复核（需要一次渲染把决策落下）
        }
      }
      if (changed) invalidate()
    })
    ioRef.current = io
    roRef.current = ro
    // ⚠️ 首帧竞态（历史加载的整棵 turn 永不窗口化的根因）：
    // ref 回调（register）在 commit 阶段执行，本 effect 在其**之后**运行 —— 首个
    // commit 挂载的块注册时 roRef/ioRef 还是 null（observe 落空），而 setRef 是
    // useCallback([hKey, register]) 恒定的 → React 不会二次调用它 → 这些块**永远
    // 不被观测** → 永无高度 → 永不 settle → 永不 muted。于是「从 /api/history 加载
    // 的长 turn」全量挂载（实测 40×400：3200 块，muted=0），每帧的样式/布局/绘制
    // 代价 ∝ 迭代数；而 SSE 追加的 turn 因为在 effect 之后才挂载，窗口化正常
    // （实测 400 迭代 → 395 muted）。修复：观测创建时补观测已注册元素（IO/RO 对
    // 同一元素重复 observe 幂等，初始回调本就会带上当前尺寸）。
    for (const el of elements.current.values()) {
      ro.observe(el)
      io.observe(el)
    }
    return () => {
      io.disconnect()
      ro.disconnect()
      ioRef.current = null
      roRef.current = null
      for (const t of verifyTimers.current.values()) window.clearTimeout(t)
      verifyTimers.current.clear()
      settleSchedulerRef.current?.cancelAll()
    }
  }, [scheduleSettleSample, tracker, invalidate])

  const register = useCallback((hKey: string, el: HTMLDivElement | null) => {
    const prev = elements.current.get(hKey)
    if (prev && prev !== el) {
      roRef.current?.unobserve(prev)
      ioRef.current?.unobserve(prev)
      elements.current.delete(hKey)
    }
    if (el) {
      elements.current.set(hKey, el)
      roRef.current?.observe(el)
      ioRef.current?.observe(el)
    }
  }, [])
  const registerStable = register

  // ── 分块渲染（几何模型不变：每块一个外壳；只重建"变化"的 chunk） ──
  const total = contiguous.length
  const chunkCount = Math.ceil(total / COMMITTED_CHUNK_SIZE)
  const cache = chunkCache.current
  if (turnIDRef.current !== turnID) {
    // turnID 变了 ⇒ 所有 hKey / 决策失效
    turnIDRef.current = turnID
    dirty.current = true
    cache.clear()
    pendingVerify.current.length = 0
  }
  if (scopeRef.current !== heightScope) {
    // 作用域变了（会话或布局宽度变了）⇒ 高度/复核裁决整体换人：分块里缓存的
    // mutedHeights 是旧作用域的决策，必须丢弃重算（新作用域自己的缓存在 tracker 里）。
    scopeRef.current = heightScope
    dirty.current = true
    cache.clear()
    pendingVerify.current.length = 0
  }
  const win = canWindow()
  const near = nearRef.current
  const recompute = dirty.current
  dirty.current = false

  /**
   * 收集本帧**候选复核**的 key（effect 里为它们建复核定时器）。
   *
   * 候选 = 「已 settle 但还没通过内容复核」的块 —— 冻结的门槛从"settle 过了"改成
   * "settle 过 **且** 内容挂回来后实测确认过"，所以候选不再等同于"刚被冻结的块"。
   */
  const collectPending = (items: WebIteration[]): void => {
    for (let i = 0; i < items.length; i++) {
      const hKey = hKeyFor(items[i])
      if (!tracker.isSettled(hKey)) continue
      if (tracker.isVerified(hKey) || verifyTimers.current.has(hKey)) continue
      pendingVerify.current.push(hKey)
    }
  }

  const chunks: ReactElement[] = []
  for (let c = 0; c < chunkCount; c++) {
    const start = c * COMMITTED_CHUNK_SIZE
    const len = Math.min(COMMITTED_CHUNK_SIZE, total - start)
    const prev = cache.get(c)
    if (prev !== undefined && prev.items.length === len) {
      // 1) items 只做指针比较（零分配）—— 同号覆盖/前缀被换也能立刻发现。
      let itemsSame = true
      for (let i = 0; i < len; i++) {
        if (prev.items[i] !== contiguous[start + i]) {
          itemsSame = false
          break
        }
      }
      if (itemsSame) {
        // 干净帧 → 元素对象原样复用（React 在该 chunk 子树直接 bail）
        if (!recompute) {
          chunks.push(prev.element)
          continue
        }
        // 脏帧 → 重算决策；值没变仍复用（不给 React 造无谓的 props 变更）
        const heights = new Array<number | undefined>(len)
        let heightsSame = prev.mutedHeights.length === len
        for (let i = 0; i < len; i++) {
          const h = win ? mutedHeightFor(prev.items[i], win, near, verifying) : undefined
          heights[i] = h
          if (heightsSame && prev.mutedHeights[i] !== h) heightsSame = false
        }
        // 候选复核必须在**每个脏帧**都收集：冻结门槛是「settle 过 + 内容实测确认过」，
        // 决策因此会先保持"不变"（settled 但未确认 → 不冻结）—— 若只在决策变化时收集，
        // 复核永远拿不到候选 → 永不确认 → 永不能冻结（死锁）。
        collectPending(prev.items)
        if (heightsSame) {
          chunks.push(prev.element)
          continue
        }
        const element = (
          <CommittedChunk
            key={c}
            items={prev.items}
            turnID={turnID}
            mutedHeights={heights}
            register={registerStable}
          />
        )
        cache.set(c, { items: prev.items, mutedHeights: heights, element })
        collectPending(prev.items)
        chunks.push(element)
        continue
      }
    }
    // 2) 新 chunk / items 变了 → 重建该 chunk（尾部 chunk 随流式重渲染）
    const items = contiguous.slice(start, start + len)
    const heights = new Array<number | undefined>(len)
    for (let i = 0; i < len; i++) {
      heights[i] = win ? mutedHeightFor(items[i], win, near, verifying) : undefined
    }
    const element = (
      <CommittedChunk
        key={c}
        items={items}
        turnID={turnID}
        mutedHeights={heights}
        register={registerStable}
      />
    )
    cache.set(c, { items, mutedHeights: heights, element })
    collectPending(items)
    chunks.push(element)
  }
  if (cache.size > chunkCount) {
    // 前缀被截断（弱网丢包）→ 清掉越界 chunk，避免缓存泄漏
    for (const k of Array.from(cache.keys())) if (k >= chunkCount) cache.delete(k)
  }

  // 内容复核（冻结的前置条件）：candidate（已 settle 未复核）延迟 `VERIFY_DELAY_MS`
  // 后把内容**挂回来**（verifying），并在**那次 commit 之后**（useLayoutEffect，
  // 见下面）同步量一次 —— 只有"内容真的挂着、元素真的可布局、实测高度 == 高度缓存"
  // 才算复核通过（verified），此后才允许冻结。
  //
  // ⛔ 为什么不能用 setTimeout + requestAnimationFrame 量（2026-09-13 回归根因）：
  // rAF 可能赶在 React 把内容挂回来**之前**跑（CPU 降速的手机上尤其），于是量到的
  // 就是**被冻结的占位本身**（实测 `stillMuted === true` 76/397 次），
  // `record` 返回 `changed:false` → 被当成"复核通过" → 把错误高度永久固化。
  useEffect(() => {
    if (!canWindow()) return
    const keys = pendingVerify.current
    if (keys.length === 0) return
    pendingVerify.current = []
    for (const hKey of keys) {
      if (tracker.isVerified(hKey) || verifyTimers.current.has(hKey)) continue
      const timer = window.setTimeout(() => {
        verifyTimers.current.delete(hKey)
        if (!elements.current.get(hKey)) return
        setVerifying((prev) => {
          const next = new Set(prev)
          next.add(hKey)
          return next
        })
        invalidate()
      }, VERIFY_DELAY_MS)
      verifyTimers.current.set(hKey, timer)
    }
  })

  // 复核测量：在「内容已挂回」的那次 commit 之后同步执行（layout effect 语义 ——
  // 此刻 DOM 已更新，读 rect 会强制完成布局，量到的就是**内容**的高度）。
  // 复核失败 / 无法测量时**必须解冻并保持挂载**（宁可不窗口化，也不能显示空块）。
  useLayoutEffect(() => {
    if (!canWindow()) return
    if (verifying.size === 0) return
    let changed = false
    for (const hKey of Array.from(verifying)) {
      const el = elements.current.get(hKey)
      const finish = () =>
        setVerifying((prev) => {
          const next = new Set(prev)
          next.delete(hKey)
          return next
        })
      // 内容真的挂回来了吗？muted 的块内容不渲染 —— 量它等于量占位（无意义）。
      const contentMounted = el !== undefined && el.dataset.windowMuted !== 'true'
      const rect = el?.getBoundingClientRect()
      if (!el || !contentMounted || !rect || !isLayoutable(el, rect)) {
        // 复核失败（元素没了 / 内容没挂回 / 没有布局）：解冻并保持挂载，等真实测量
        tracker.unverify(hKey, performance.now())
        finish()
        changed = true
        continue
      }
      const res = tracker.record(hKey, rect.height, performance.now(), true)
      if (res.changed) {
        // 内容实测高度 ≠ 缓存高度（冻结依据是错的）→ 高度已更新且 unsettle；
        // 必须补一次二次采样，否则内容已挂载、RO 不会再报变化 → 该块永远无法
        // 重新 settle → 永远无法冻结（窗口化收益整段丢失）。
        scheduleSettleSample(hKey)
      } else {
        tracker.markVerified(hKey) // 内容实测高度 == 缓存高度 → 复核通过
      }
      finish()
      changed = true
    }
    if (changed) invalidate()
  })

  return <>{chunks}</>
})


/** 连续「带工具」的迭代折叠成**一个** pill 行（用户 2026-09-15 定的规则）。
 *
 *  ⚠️ 关键修正（用户指出）：折叠的**头部迭代可以带 reasoning/content**（工具行与文本块同属该迭代）。
 *  只有**后续成员**才要求"只有工具"（`!content && !reasoning`）——
 *  否则头部被排除 ⇒ 它的工具单独成行（截图里"失败 chip + 失败 pill 在上、其余 pill 在下"的真因，
 *  **不是**什么置顶逻辑）。每个带文本的迭代仍是**独立块**（文本不合并、不丢）。
 */
function mergeToolRuns(iters: WebIteration[]): WebIteration[] {
  const hasTools = (it: WebIteration) => it.tools.length > 0
  const absorbs = (it: WebIteration) => hasTools(it) && !it.content && !it.reasoning
  const out: WebIteration[] = []
  for (let i = 0; i < iters.length; i++) {
    const head = iters[i]
    if (!hasTools(head)) { out.push(head); continue }
    let j = i
    const tools = [...head.tools]
    while (j + 1 < iters.length && absorbs(iters[j + 1])) { j++; tools.push(...iters[j].tools) }
    // 保留**头部**迭代号（高度缓存 / 窗口 key 稳定；文本与工具都取头部那一份 + 后续成员的工具）
    out.push(j === i ? head : { ...head, tools })
    i = j
  }
  return out
}

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  turnID,
  heightScope,
}: TurnBodyProps) {
  // Linear-consistency guard: 只渲染**连续前缀**（弱网丢中间迭代时不能出现 1,3）。
  // PERF（#4）：增量扫描 —— 只扫新增的尾部（锚点校验前缀；不符即全量扫描）。
  // 无变化时 `out` 引用稳定 → 保住 CommittedTurn 的 memo。
  const scanRef = useRef<ContiguousScan | null>(null)
  const scan = extendContiguous(scanRef.current, iterations)
  scanRef.current = scan
  const contiguous = scan.out
  // 跨迭代折叠（连续 tool-only 迭代共享一行）；`contiguous` 引用稳定 ⇒ 这个 memo 也稳定，
  // 不会击穿 CommittedTurn 的 memo / 迭代级窗口化。
  const merged = useMemo(() => mergeToolRuns(contiguous), [contiguous])

  return (
    <div
      className="iter-blocks"
      data-iter-range={
        contiguous.length > 0
          ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}`
          : undefined
      }
      data-iter-total={contiguous.length}
    >
      <CommittedTurn contiguous={merged} turnID={turnID} heightScope={heightScope} />
      {liveProgress && (
        <div
          className="iter-block"
          data-iter-id="live"
          data-iter-num={liveProgress.iteration || undefined}
          data-turn-id={liveProgress.turnID || turnID}
        >
          <LiveIteration progress={liveProgress} />
        </div>
      )}
    </div>
  )
})
