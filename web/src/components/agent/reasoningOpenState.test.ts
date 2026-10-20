import { beforeEach, describe, expect, it } from 'vitest'

import {
  __resetReasoningOpenState,
  getReasoningOpen,
  reasoningKey,
  setReasoningOpen,
} from './reasoningOpenState'

describe('reasoningKey', () => {
  beforeEach(() => __resetReasoningOpenState())

  it('turnID 无效时返回空串，不参与共享（否则所有 turn 的同一 iteration 会共用 key）', () => {
    expect(reasoningKey(null, 1)).toBe('')
    expect(reasoningKey(undefined, 1)).toBe('')
    expect(reasoningKey(0, 1)).toBe('')
    // 负数同样视为无效
    expect(reasoningKey(-1, 3)).toBe('')
  })

  it('turnID 有效时按 turn:iteration 隔离', () => {
    expect(reasoningKey(7, 1)).toBe('7:1')
    expect(reasoningKey(8, 1)).toBe('8:1')
    expect(reasoningKey(7, 2)).toBe('7:2')
  })

  it('展开态不跨 turn 泄漏：点开 turn 7 的思考不会让 turn 8 同号迭代展开', () => {
    const k7 = reasoningKey(7, 1)
    const k8 = reasoningKey(8, 1)
    setReasoningOpen(k7, true)
    expect(getReasoningOpen(k7)).toBe(true)
    expect(getReasoningOpen(k8)).toBeUndefined()
  })

  it('空 key 不会写入共享表（ThinkingLine 退回本地 state，默认折叠）', () => {
    // ThinkingLine 内部对 falsy stateKey 不会调用 setReasoningOpen；
    // 这里直接验证空 key 查不到任何记录。
    expect(getReasoningOpen('')).toBeUndefined()
  })
})
