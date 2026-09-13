/**
 * 守护：`estimateRowByContent` 必须**按 row 对象记忆化**（WeakMap）。
 *
 * 根因（2026-09-13 用户报「加载的历史消息长了就卡」）：TanStack 每次重算 offsets
 * 都会为尚未实测的**每一行**调用 estimateSize → estimateRowByContent；函数体对
 * assistant row 会**遍历该行全部迭代**（tools + iterLen）→ 每帧总代价 =
 * O(已加载的全部迭代数)，历史越长/迭代越多越卡。
 * 记忆化后：同一 row 对象重复调用零重算。
 */
import { describe, expect, it } from 'vitest'

import {
  __estimateRowByContentComputeCount,
  estimateRowByContent,
} from '@/components/agent/MessageList'
import type { ChatMessage } from '@/types/shared'

const rowWithIterations = (n: number): ChatMessage =>
  ({
    id: 'r1',
    role: 'assistant',
    content: 'x'.repeat(200),
    iterations: Array.from({ length: n }, (_, i) => ({
      iteration: i + 1,
      content: 'c'.repeat(500),
      reasoning: 'r'.repeat(200),
      tools: [{ name: 'Shell', status: 'done' }],
    })),
  }) as unknown as ChatMessage

describe('estimateRowByContent 记忆化', () => {
  it('同一 row 对象重复调用只计算一次（TanStack 每帧逐行调用 estimateSize）', () => {
    const row = rowWithIterations(50)
    const before = __estimateRowByContentComputeCount.value
    const first = estimateRowByContent(row)
    for (let i = 0; i < 200; i++) expect(estimateRowByContent(row)).toBe(first)
    expect(__estimateRowByContentComputeCount.value - before).toBe(1)
  })

  it('值仍随内容增长（记忆化不改变语义）', () => {
    const small = estimateRowByContent(rowWithIterations(1))
    const big = estimateRowByContent(rowWithIterations(30))
    expect(big).toBeGreaterThan(small)
  })

  it('不同 row 对象各自计算（WeakMap 不串味）', () => {
    const a = rowWithIterations(2)
    const b = rowWithIterations(2)
    estimateRowByContent(a)
    estimateRowByContent(b)
    expect(estimateRowByContent(a)).toBe(estimateRowByContent(b))
  })
})
