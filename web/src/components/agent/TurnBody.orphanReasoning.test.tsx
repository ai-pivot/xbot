import { describe, expect, it } from 'vitest'
import { mergeIterationsForRender, mergeOrphanReasoning } from './TurnBody'
import type { WebIteration } from '@/types/shared'

/**
 * 2026-11-04 P0（用户报「content 后面接 reasoning，刷新都不能恢复，究极无敌严重」）回归。
 *
 * 真实数据（turn 3164 → 3165，中间没有用户消息，是后台任务通知自动续跑）：
 *   turn 3164 iter=3  content=''    reasoning=629字
 *   turn 3164 iter=4  content=552字 reasoning=''
 *   turn 3165 iter=1  content=''    reasoning=214字   ← 被渲染到上一轮正文之后
 *   turn 3165 iter=2  content=67字  reasoning=267字
 *
 * 语义：思考必须在它所属的输出之前。孤儿 reasoning 应并入下一个有内容的迭代。
 */
function mk(partial: Partial<WebIteration> & { iteration: number }): WebIteration {
  return {
    content: '',
    reasoning: '',
    tools: [],
    tokens: 0,
    ttftMs: 0,
    tokensPerSec: 0,
    toolMs: 0,
    ...partial,
  } as WebIteration
}

describe('mergeIterationsForRender (孤儿 reasoning 接入点)', () => {
  it('孤儿 reasoning 并入下一个有 content 的迭代（不再渲染在上一轮正文之后）', () => {
    const input = [
      mk({ iteration: 1, content: '上一轮正文', reasoning: '' }),
      mk({ iteration: 2, reasoning: '这一轮的思考' }), // 孤儿
      mk({ iteration: 3, content: '这一轮的正文', reasoning: '后续思考' }),
    ]
    const out = mergeIterationsForRender(input)
    expect(out).toHaveLength(2)
    expect(out[0].iteration).toBe(1)
    expect(out[0].content).toBe('上一轮正文')
    // 孤儿 reasoning 成了下一块的前缀
    expect(out[1].iteration).toBe(3)
    expect(out[1].reasoning).toBe('这一轮的思考后续思考')
    expect(out[1].content).toBe('这一轮的正文')
  })

  it('连续多个孤儿 reasoning 按顺序拼接', () => {
    const input = [
      mk({ iteration: 1, reasoning: 'A' }),
      mk({ iteration: 2, reasoning: 'B' }),
      mk({ iteration: 3, content: '正文', reasoning: 'C' }),
    ]
    const out = mergeIterationsForRender(input)
    expect(out).toHaveLength(1)
    expect(out[0].reasoning).toBe('ABC')
  })

  it('尾部孤儿保留独立块（正在进行中的思考，不是错位）', () => {
    const input = [
      mk({ iteration: 1, content: '正文' }),
      mk({ iteration: 2, reasoning: '正在想' }),
    ]
    const out = mergeIterationsForRender(input)
    expect(out).toHaveLength(2)
    expect(out[1].iteration).toBe(2)
    expect(out[1].reasoning).toBe('正在想')
  })

  it('孤儿 reasoning 后面跟纯工具迭代时不吞工具（工具也算 payload）', () => {
    const tool = { name: 'Shell', label: 'Shell', status: 'done', iteration: 2 } as never
    const input = [
      mk({ iteration: 1, content: '正文' }),
      mk({ iteration: 2, reasoning: '想调用工具' }),
      mk({ iteration: 3, tools: [tool] }),
    ]
    const out = mergeIterationsForRender(input)
    expect(out).toHaveLength(2)
    expect(out[1].iteration).toBe(3)
    expect(out[1].reasoning).toBe('想调用工具')
    expect(out[1].tools).toHaveLength(1)
  })

  it('正常的「reasoning + content 同块」不受影响', () => {
    const input = [
      mk({ iteration: 1, content: '正文', reasoning: '思考' }),
      mk({ iteration: 2, content: '正文2', reasoning: '思考2' }),
    ]
    const out = mergeIterationsForRender(input)
    expect(out).toHaveLength(2)
    expect(out[0].reasoning).toBe('思考')
    expect(out[1].reasoning).toBe('思考2')
  })

  it('空输入不炸', () => {
    expect(mergeOrphanReasoning([])).toEqual([])
  })
})
