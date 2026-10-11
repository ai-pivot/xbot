/**
 * B0 同构契约测试：前端 `mergeToolRuns`（`TurnBody.tsx:941`，规范唯一实现）
 * 必须与后端 `RegionRuns`（`channel/region_view.go`）产出**完全相同**的展示区域边界。
 *
 * 规范唯一来源 = `docs/plan-history-fold-windowing.md` §2.1 伪代码：
 *
 *     hasTools(it) = it.tools.length > 0
 *     absorbs(it)  = hasTools(it) && !it.content && !it.reasoning   // 纯工具迭代
 *     head         = 首个 hasTools 迭代（**可带 content/reasoning** —— 用户点名的坑）
 *     贪心吸收 head 之后**连续**的 absorbs 成员；块 = {...head, tools: 合并}，
 *     保留 **head 的迭代号**（高度缓存 / 窗口 key 稳定）
 *
 * 「展示区域」= 本函数输出块（折叠的工具组算 **1** 个区域）—— 区域数是
 * `regions_before` / `/api/regions` 段边界 / 尾部窗口 K 的**同一把尺子**：
 * 两端判定漂移 ⇒ 段边界错 ⇒ 前端拼接断号（方案 R1）。所以两侧跑**同一组 fixture**：
 *
 *   ⇒ fixture F1–F11 **逐条对齐** `channel/region_view_test.go` 的 `regionFixtures()`
 *     （同一输入：迭代号、content、工具名；同一期望：区域边界 HeadIteration /
 *      FirstIteration / LastIteration / IterationCount / ToolCount，与 Go 侧
 *      `[]RegionRun` 字面 1:1）。
 *
 * `M*` 组是**判别力自证**（改错必红），不占 F 编号。
 */
import { describe, expect, it } from 'vitest'

import { mergeToolRuns } from '@/components/agent/TurnBody'
import type { WebIteration, WebToolProgress } from '@/types/shared'

// ── fixture 构造器（对齐 Go region_view_test.go 的 regionRec/plainTool/genuiTool）──

function tool(name: string, uiMode?: string): WebToolProgress {
  return {
    name,
    label: name,
    status: 'done',
    elapsedMs: 1,
    summary: '',
    detail: '',
    args: '',
    toolHints: '',
    ...(uiMode ? { uiMode } : {}),
  }
}

interface IterSpec {
  content?: string
  reasoning?: string
  /** 普通工具名列表（对齐 Go 侧 `plainTool("A")` 等）。 */
  tools?: string[]
  /** GenUI 工具名（uiMode='genui'）—— 对齐 Go 侧 `genuiTool(...)`。 */
  genui?: string[]
  /** 折叠视图标记（REST 历史的轻字段迭代 = true；live/全量 = 缺省）。 */
  folded?: boolean
}

function iter(iteration: number, spec: IterSpec = {}): WebIteration {
  const tools = [...(spec.tools ?? []).map((n) => tool(n)), ...(spec.genui ?? []).map((n) => tool(n, 'genui'))]
  return {
    iteration,
    content: spec.content ?? '',
    reasoning: spec.reasoning ?? '',
    tools,
    toolCount: tools.length,
    ...(spec.folded ? { toolsFolded: true } : {}),
  }
}

/** 区域视图 —— 字段与 Go `RegionRun` 同名同义（1:1 对照）。 */
interface RegionRunView {
  HeadIteration: number
  FirstIteration: number
  LastIteration: number
  IterationCount: number
  ToolCount: number
}

/**
 * 把 `mergeToolRuns` 的输出块表成 Go `RegionRun` 形状。
 *
 * `FirstIteration` = 块的迭代号（head）；`LastIteration` = 「下一个块的 head - 1」（末块 =
 * 输入末迭代）—— 成立前提是 fixture 输入迭代号连续 1..N（与 DB 不变量一致：turn 内迭代号
 * 连续；合并块成员是连续的 absorbs 迭代）。
 */
function regionRuns(iters: WebIteration[], out = mergeToolRuns(iters)): RegionRunView[] {
  const idxByIter = new Map(iters.map((it, i) => [it.iteration, i]))
  return out.map((blk, i) => {
    const start = idxByIter.get(blk.iteration)!
    const end = i + 1 < out.length ? idxByIter.get(out[i + 1].iteration)! - 1 : iters.length - 1
    return {
      HeadIteration: blk.iteration,
      FirstIteration: iters[start].iteration,
      LastIteration: iters[end].iteration,
      IterationCount: end - start + 1,
      ToolCount: blk.tools.length,
    }
  })
}

const toolsOf = (it: WebIteration): string[] => it.tools.map((t) => t.name)

// ── F 组：共享 fixture F1–F11（与 channel/region_view_test.go 同一组、同编号、同输入）──

/** F1 `[text]`：纯文本迭代独立成块。 */
const F1: WebIteration[] = [iter(1, { content: 'hello' })]

/** F2 `[text+tools, tool-only, tool-only]`：1 区域，head=iter1 **带文本**。 */
const F2: WebIteration[] = [
  iter(1, { content: 'assistant text', tools: ['A'] }),
  iter(2, { tools: ['B'] }),
  iter(3, { tools: ['C'] }),
]

/** F3 `[tool-only, tool-only, text]`：2 区域，head 是纯工具迭代。 */
const F3: WebIteration[] = [iter(1, { tools: ['A'] }), iter(2, { tools: ['B'] }), iter(3, { content: 'final answer' })]

/** F4 `[text+tools, tool-only, text, tool-only]`：3 区域（run 被文本断开）。 */
const F4: WebIteration[] = [
  iter(1, { content: 't1', tools: ['A'] }),
  iter(2, { tools: ['B'] }),
  iter(3, { content: 't2' }),
  iter(4, { tools: ['C'] }),
]

/** F5 `[genui-only]`：1 区域 + GenUI 豁免（区域边界不因 uiMode 改变）。 */
const F5: WebIteration[] = [iter(1, { genui: ['display_html'] })]

/** F6 `[text+tools(1 普通+1 GenUI), tool-only(2 普通)]`：1 区域。 */
const F6: WebIteration[] = [
  iter(1, { content: 'hi', tools: ['Read'], genui: ['display_html'] }),
  iter(2, { tools: ['Shell', 'Grep'] }),
]

/** F7 `[tool-only ×3]`：1 区域，head=iter1。 */
const F7: WebIteration[] = [iter(1, { tools: ['A'] }), iter(2, { tools: ['B'] }), iter(3, { tools: ['C'] })]

/** F8 空序列：0 区域。 */
const F8: WebIteration[] = []

/**
 * F9 12 区域 / 18 迭代（尾部 k=6 区域 ⇒ 迭代 7..18）—— 与 Go 侧同一构造：
 * 迭代 1..6 纯文本；其后 6 组「带文本 head + 纯工具成员」。
 */
function f9Recs(): WebIteration[] {
  const recs: WebIteration[] = []
  for (let i = 1; i <= 6; i++) recs.push(iter(i, { content: `text-${i}` }))
  let it = 7
  for (let r = 0; r < 6; r++) {
    recs.push(iter(it, { content: `head-${r}`, tools: [`H${r}`] }))
    it++
    recs.push(iter(it, { tools: [`F${r}`] }))
    it++
  }
  return recs
}
const F9: WebIteration[] = f9Recs()

/** F10 `k ≥ 区域总数`：与 F2 同输入（全量下发路径）。 */
const F10: WebIteration[] = [
  iter(1, { content: 'assistant text', tools: ['A'] }),
  iter(2, { tools: ['B'] }),
  iter(3, { tools: ['C'] }),
]

/** F11 单迭代 turn（text+tools）：1 区域。 */
const F11: WebIteration[] = [iter(1, { content: 'only', tools: ['A'] })]

describe('mergeToolRuns ↔ RegionRuns 同构契约（F 组 = T4 共享 fixture，编号与 Go 一致）', () => {
  it('F1 `[text]` ⇒ 1 区域（无工具迭代独立成块）', () => {
    expect(regionRuns(F1)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 0 },
    ])
    // 未发生吸收 ⇒ **原引用**透传（下游 memo / 迭代级窗口化靠引用稳定）
    expect(mergeToolRuns(F1)[0]).toBe(F1[0])
  })

  it('F2 `[text+tools, tool-only, tool-only]` ⇒ 1 区域 head=1，成员工具按时间序并进 head', () => {
    expect(regionRuns(F2)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3 },
    ])
    const out = mergeToolRuns(F2)
    // head 号保留（不是成员号 3）、工具按时间序拼接、head 的文本/思考保留
    expect(out[0].iteration).toBe(1)
    expect(toolsOf(out[0])).toEqual(['A', 'B', 'C'])
    expect(out[0].content).toBe('assistant text')
    expect(out[0].reasoning).toBe('')
    // 被吸收成员按定义 content/reasoning 为空 ⇒ 吸收不丢任何文本信息
    expect(F2[1].content).toBe('')
    expect(F2[2].reasoning).toBe('')
    // 折叠成块 ⇒ 新对象（head 号 + 合并工具），不是原 head 引用
    expect(out[0]).not.toBe(F2[0])
  })

  it('F3 `[tool-only, tool-only, text]` ⇒ 2 区域（head 本身是纯工具迭代）', () => {
    expect(regionRuns(F3)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0 },
    ])
    expect(toolsOf(mergeToolRuns(F3)[0])).toEqual(['A', 'B'])
  })

  it('F4 `[text+tools, tool-only, text, tool-only]` ⇒ 3 区域（文本切断 run，尾部工具独立成区）', () => {
    expect(regionRuns(F4)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 4, FirstIteration: 4, LastIteration: 4, IterationCount: 1, ToolCount: 1 },
    ])
    const out = mergeToolRuns(F4)
    expect(toolsOf(out[0])).toEqual(['A', 'B'])
    expect(toolsOf(out[2])).toEqual(['C'])
    expect(out[1].content).toBe('t2')
  })

  it('F5 `[genui-only]` ⇒ 1 区域（GenUI 只影响载荷瘦身，不影响区域边界）', () => {
    expect(regionRuns(F5)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1 },
    ])
    expect(mergeToolRuns(F5)[0].tools[0].uiMode).toBe('genui')
  })

  it('F6 `[text+tools(1 普通+1 GenUI), tool-only(2 普通)]` ⇒ 1 区域，4 工具', () => {
    expect(regionRuns(F6)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 2, IterationCount: 2, ToolCount: 4 },
    ])
    expect(toolsOf(mergeToolRuns(F6)[0])).toEqual(['Read', 'display_html', 'Shell', 'Grep'])
  })

  it('F7 `[tool-only ×3]` ⇒ 1 区域 head=1（head 可以是纯工具迭代）', () => {
    expect(regionRuns(F7)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 3, IterationCount: 3, ToolCount: 3 },
    ])
    expect(toolsOf(mergeToolRuns(F7)[0])).toEqual(['A', 'B', 'C'])
  })

  it('F8 空序列 ⇒ 0 区域', () => {
    expect(mergeToolRuns(F8)).toEqual([])
  })

  it('F9 12 区域 / 18 迭代（尾部 6 区域起点 = 迭代 7 —— 区域粒度而非迭代粒度）', () => {
    const want: RegionRunView[] = [
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 2, FirstIteration: 2, LastIteration: 2, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 3, FirstIteration: 3, LastIteration: 3, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 4, FirstIteration: 4, LastIteration: 4, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 5, FirstIteration: 5, LastIteration: 5, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 6, FirstIteration: 6, LastIteration: 6, IterationCount: 1, ToolCount: 0 },
      { HeadIteration: 7, FirstIteration: 7, LastIteration: 8, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 9, FirstIteration: 9, LastIteration: 10, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 11, FirstIteration: 11, LastIteration: 12, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 13, FirstIteration: 13, LastIteration: 14, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 15, FirstIteration: 15, LastIteration: 16, IterationCount: 2, ToolCount: 2 },
      { HeadIteration: 17, FirstIteration: 17, LastIteration: 18, IterationCount: 2, ToolCount: 2 },
    ]
    expect(regionRuns(F9)).toEqual(want)
    // 尾部 k=6 区域的窗口起点 = 区域 7 的 head 迭代号（而不是「尾部 6 个迭代」= 迭代 13）
    expect(want[want.length - 6].HeadIteration).toBe(7)
  })

  it('F10 `k ≥ 区域总数`（与 F2 同输入）⇒ 全量路径区域边界不变', () => {
    expect(regionRuns(F10)).toEqual(regionRuns(F2))
  })

  it('F11 单迭代 turn（text+tools）⇒ 1 区域，文本/工具全保留且原引用透传', () => {
    expect(regionRuns(F11)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1 },
    ])
    const out = mergeToolRuns(F11)
    expect(out[0]).toBe(F11[0])
    expect(out[0].content).toBe('only')
  })

  it('区域数 = 输出块数（`regions_before` / 段边界 / 尾部窗口 K 的同一把尺子）', () => {
    const table: [string, WebIteration[], number][] = [
      ['F1', F1, 1],
      ['F2', F2, 1],
      ['F3', F3, 2],
      ['F4', F4, 3],
      ['F5', F5, 1],
      ['F6', F6, 1],
      ['F7', F7, 1],
      ['F8', F8, 0],
      ['F9', F9, 12],
      ['F10', F10, 1],
      ['F11', F11, 1],
    ]
    for (const [id, input, want] of table) {
      expect(`${id}:${mergeToolRuns(input).length}`).toBe(`${id}:${want}`)
    }
  })

  it('结构不变量：head 号严格递增、纯函数（不改写输入）、合并块的 tools 是新数组', () => {
    const snapshot = JSON.stringify(F4)
    const out = mergeToolRuns(F4)
    for (let i = 1; i < out.length; i++) {
      expect(out[i].iteration).toBeGreaterThan(out[i - 1].iteration)
    }
    expect(JSON.stringify(F4)).toBe(snapshot)
    expect(out[0].tools).not.toBe(F4[0].tools)
    expect(out[0].tools).toEqual([...F4[0].tools, ...F4[1].tools])
  })
})

// ── M 组：判别力自证（mutation 点）─────────────────────────────
//
// 每条 = 「改错哪一行 ⇒ 哪条 F 必红」（已用 6 个变异体实测：全部被 F/M 断言捕获）。
// M 组用独立 fixture 补 F 组覆盖不到的条件（成员带思考、非连续 absorbs、头部号取自成员）。

describe('mergeToolRuns 判别力自证（M 组：改错必红）', () => {
  /**
   * M1 —— 成员前提是 `!content && !reasoning`（纯工具迭代），**不是**「带工具就吸收」。
   *
   * mutation：把 `absorbs` 简化成 `hasTools(it)`（去掉 `!it.content && !it.reasoning`）
   * ⇒ 紧随 head 的「带文本/带思考的带工具迭代」被误吸收 ⇒ 区域数 2 → **1**，且它的文本
   *    被吞进工具组（文本消失 = 无感验收 G2 破裂）⇒ M1 必红。
   * （F2/F4 的吸收成员本身是纯工具迭代 ⇒ 它们杀不掉这个 mutation，故 M1 单独存在。）
   */
  it('M1 带文本/带思考的工具迭代**不被**吸收（区域边界的核心判据）', () => {
    const textMember = [iter(1, { tools: ['A'] }), iter(2, { content: 'sibling text', tools: ['B'] })]
    expect(regionRuns(textMember)).toEqual([
      { HeadIteration: 1, FirstIteration: 1, LastIteration: 1, IterationCount: 1, ToolCount: 1 },
      { HeadIteration: 2, FirstIteration: 2, LastIteration: 2, IterationCount: 1, ToolCount: 1 },
    ])
    const reasoningMember = [iter(1, { tools: ['A'] }), iter(2, { reasoning: 'thinking…', tools: ['B'] })]
    expect(regionRuns(reasoningMember).map((r) => r.HeadIteration)).toEqual([1, 2])
    expect(mergeToolRuns(reasoningMember)[1].reasoning).toBe('thinking…')
  })

  /**
   * M2 —— head **可以带文本**，且带文本的 head 照常折叠其后的纯工具成员。
   *
   * mutation：给折叠附加「head 无文本」的前提（要求 head 为纯工具迭代）⇒ F2 折叠失效
   * ⇒ 区域数 1 → **3** ⇒ F2 必红。
   */
  it('M2 带文本的 head 照常折叠后续纯工具成员（F2 的判别力来源）', () => {
    const out = mergeToolRuns(F2)
    expect(out).toHaveLength(1)
    expect(out[0].content).toBe('assistant text') // 文本与合并后的工具组同属一个区域
  })

  /**
   * M3 —— 折叠块的迭代号取 **head**（不是最后一个成员）。
   *
   * mutation：把 `{ ...head, tools }` 写成 `{ ...iters[j], tools }`（号取成员）⇒ F2 的
   * head 号 1 → **3**、F7 的 1 → 3 ⇒ 窗口 key / 高度缓存漂移（同一迭代换号重新挂载，
   * 滚动位置与高度记忆串味）⇒ F2/F7 必红。
   */
  it('M3 折叠块保留 head 迭代号（号取成员 ⇒ 窗口 key 漂移）', () => {
    expect(mergeToolRuns(F2)[0].iteration).toBe(F2[0].iteration)
    expect(mergeToolRuns(F7)[0].iteration).toBe(F7[0].iteration)
    expect(mergeToolRuns(F7)[0].iteration).not.toBe(F7[2].iteration)
  })

  /**
   * M4 —— 吸收**贪心且必须连续**：遇到不满足 `absorbs` 的迭代即停止。
   *
   * mutation：把 `while (j + 1 < iters.length && absorbs(...))` 改成「吸收所有后续 absorb
   * 成员（跳过不连续者）」⇒ F4 的 it4（纯工具，位于文本 it3 之后）被并进第 1 个区域
   * ⇒ 区域数 3 → **2** ⇒ F4 必红（段边界与后端不符 ⇒ 拼接断号）。
   */
  it('M4 absorbs 必须连续（跳过非吸收迭代去拼远端工具 ⇒ F4 必红）', () => {
    expect(mergeToolRuns(F4)).toHaveLength(3)
    expect(toolsOf(mergeToolRuns(F4)[0])).toEqual(['A', 'B'])
    expect(toolsOf(mergeToolRuns(F4)[2])).toEqual(['C'])
  })

  /**
   * M5 —— head = **首个带工具**迭代，不要求带文本。
   *
   * mutation：把 `if (!hasTools(head)) {...}` 改成「要求 head 带文本（否则不折叠）」
   * ⇒ F3/F7（纯工具 head）每个纯工具迭代各成 1 区域 ⇒ 区域数 F3 2 → **3**、
   *    F7 1 → **3** ⇒ F3/F7 必红。
   */
  it('M5 纯工具 head 也折叠（要求 head 带文本 ⇒ F3/F7 必红）', () => {
    expect(mergeToolRuns(F3)).toHaveLength(2)
    expect(mergeToolRuns(F7)).toHaveLength(1)
    expect(mergeToolRuns(F7)[0].iteration).toBe(1)
  })

  /**
   * M6 —— 无工具迭代各自成**独立区域**（原引用透传，既不吸收也不被吞）。
   *
   * mutation：删掉 `if (!hasTools(head)) { out.push(head); continue }` 分支（跳过无工具
   * 迭代）⇒ 区域数 F1 1 → **0**、F4 3 → **2**，且文本块从默认视图消失 ⇒ F1/F4 必红。
   */
  it('M6 无工具迭代各自成区域（跳过无工具迭代 ⇒ F1/F4 必红）', () => {
    expect(mergeToolRuns(F1)).toHaveLength(1)
    expect(regionRuns(F4).map((r) => r.HeadIteration)).toEqual([1, 3, 4])
  })
})

// ─── B1 块级折叠标记聚合（轮 3 自审修复的防回归） ────────────────
// GenUI-only head（后端豁免瘦身 ⇒ toolsFolded=false）+ 折叠视图成员（toolsFolded=true）：
// IterationGroup 只能看到合并块的标记 —— 只取 head 一份会让成员 pill 的详情 gate 失明
// （点开空详情且不触发按需拉取）。任一成员带标记 ⇒ 合并块带标记。
// mutation：把 mergeToolRuns 的块级聚合（toolsFolded: head || blockFolded）改回只取
// head ⇒ 第一条必红。
describe('mergeToolRuns 块级 toolsFolded 聚合', () => {
  it('★ GenUI-only head + 折叠成员 ⇒ 合并块 toolsFolded=true（成员 pill 的 gate 不失明）', () => {
    const input = [
      iter(1, { genui: ['display_html'] }),
      iter(2, { tools: ['Shell'], folded: true }),
      iter(3, { tools: ['Read'], folded: true }),
    ]
    const out = mergeToolRuns(input)
    expect(out).toHaveLength(1)
    expect(out[0].toolsFolded).toBe(true)
    expect(out[0].tools).toHaveLength(3)
  })

  it('head 与成员全部完整（live/全量）⇒ 合并块 toolsFolded 不为 true（默认视图零请求路径不变）', () => {
    const out = mergeToolRuns([iter(1, { tools: ['A'] }), iter(2, { tools: ['B'] })])
    expect(out).toHaveLength(1)
    expect(out[0].toolsFolded).not.toBe(true)
  })

  it('head 带文本且折叠（REST 常态）⇒ head 自身标记传导进合并块', () => {
    const out = mergeToolRuns([iter(1, { content: 'go', tools: ['A'], folded: true }), iter(2, { tools: ['B'] })])
    expect(out).toHaveLength(1)
    expect(out[0].toolsFolded).toBe(true)
  })
})
