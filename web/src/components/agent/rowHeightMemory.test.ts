import { describe, expect, it } from 'vitest'
import { createRowHeightMemory, rowSignature } from './rowHeightMemory'

// P0（2026-09-18 字符重合 / 行重叠）判别性用例。
// 根因：rowSignature 旧实现 ① 只看长度、② 按 row 对象记忆化（derive 的 WeakMap 恒等
// memo 保证对象标识稳定而内容会变）⇒ 指纹冻结在首帧 ⇒ 记忆高度偏小 ⇒ resizeItem 早退
// 不校正 ⇒ 虚拟行 translateY 偏小 ⇒ 下一行画到上一行身上。
// 变异自证：恢复「按对象记忆化（不做输入校验）」⇒ 前两个用例必红。
describe('rowSignature：指纹必须随渲染内容变化', () => {
  it('对象标识稳定、内容变化 ⇒ 指纹必须变化（旧实现冻结在首帧）', () => {
    const row = { role: 'assistant', content: 'aaa', iterations: [], isPartial: true } as {
      role: string
      content: string
      iterations: { content?: string; reasoning?: string; tools?: unknown[] }[]
      isPartial?: boolean
    }
    const s1 = rowSignature(row)
    row.content = 'aaaaa'
    expect(rowSignature(row)).not.toBe(s1)
  })

  it('长度相同但文本不同 ⇒ 指纹必须不同（必须带内容哈希）', () => {
    const row = { role: 'assistant', content: 'abcd', iterations: [] } as {
      role: string
      content: string
      iterations: { content?: string; reasoning?: string; tools?: unknown[] }[]
    }
    const s1 = rowSignature(row)
    row.content = 'wxyz'
    expect(rowSignature(row)).not.toBe(s1)
  })

  it('iterations 内容变化（数组引用不变、逐项长度不变）⇒ 指纹必须变化', () => {
    const row = { role: 'assistant', content: '', iterations: [{ content: 'aa', reasoning: '' }] }
    const s1 = rowSignature(row)
    row.iterations[0].content = 'bb'
    expect(rowSignature(row)).not.toBe(s1)
  })

  it('完全未变 ⇒ 指纹稳定（记忆化仍生效）', () => {
    const row = { role: 'assistant', content: 'stable', iterations: [] }
    expect(rowSignature(row)).toBe(rowSignature(row))
  })
})

describe('行高记忆：指纹 / 宽度不匹配一律不命中', () => {
  it('指纹变化或宽度变化 ⇒ 返回 undefined（绝不返回陈旧高度）', () => {
    const m = createRowHeightMemory()
    m.set('k', 'sig1', 800, 500)
    expect(m.get('k', 'sig1', 800)).toBe(500)
    expect(m.get('k', 'sig2', 800)).toBeUndefined()
    expect(m.get('k', 'sig1', 900)).toBeUndefined()
  })
})
