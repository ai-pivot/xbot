import { describe, expect, it } from 'vitest'
import { createHeightAwareMeasureElement, createRowHeightMemory, rowSignature } from './rowHeightMemory'

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

// P0（2026-09-18）根因判据：measureElement **绝不用"猜"的高度定位已渲染的行**。
// 变异自证：恢复"首次测量返回记忆值" ⇒ 第二个用例必红（返回 111 而非真实 999）。
describe('measureElement：只信浏览器实测（记忆仅作 estimate 提示）', () => {
  const deps = () => {
    const measured: number[] = []
    const m = createRowHeightMemory()
    const fn = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'k', sig: 's' }),
      measure: () => {
        measured.push(1)
        return 999
      },
      memory: m,
      width: () => 800,
    })
    const el = { dataset: { index: '0' } } as unknown as Element
    return { fn, m, el, measured }
  }

  it('有 RO entry ⇒ 直接用 entry 的真实尺寸（零 DOM 读，且把记忆刷新为真值）', () => {
    const { fn, m, el, measured } = deps()
    m.set('k', 's', 800, 111)
    const entry = { borderBoxSize: [{ blockSize: 250, inlineSize: 800 }] } as unknown as ResizeObserverEntry
    expect(fn(el, entry, null)).toBe(250)
    expect(measured).toHaveLength(0)
    expect(m.get('k', 's', 800)).toBe(250)
  })

  it('无 entry（挂载）⇒ 真实测量，绝不返回记忆值', () => {
    const { fn, m, el } = deps()
    m.set('k', 's', 800, 111)
    expect(fn(el, undefined, null)).toBe(999)
  })

  it('contentRect 兜底也能取到真实高度', () => {
    const { fn, el } = deps()
    const entry = { contentRect: { height: 321 } } as unknown as ResizeObserverEntry
    expect(fn(el, entry, null)).toBe(321)
  })
})
