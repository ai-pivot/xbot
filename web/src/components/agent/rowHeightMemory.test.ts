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

// P0（2026-09-23「正文互相穿插 / 两行压在同一 y」）根因判据：
// **ResizeObserver 的 `entry.borderBoxSize` 是"观察时刻的快照"，不是当前几何** ——
// 乱序/滞后投递时它比真实尺寸**小**；一旦作为尺寸写回虚拟器（resizeItem），该行就比
// 真实高度矮 ⇒ 下一行 `translateY(start)` 偏小 ⇒ 画到它身上；随后 DOM 不再变化 ⇒
// 没有下一次回调 ⇒ 错值**永久固化**（旧实现的主路径正是"直接返回 entry 尺寸"）。
//
// 契约：**行尺寸只能来自它自己的当前几何（批量读真几何）**；RO 快照只允许用来
// **触发**一次真几何读取（`onResize`），绝不允许当作尺寸。
// 变异自证：把实现改回 `readObservedBlockSize(entry)` 直接返回 ⇒ 第 1 个用例必红。
describe('measureElement：RO 快照绝不作为尺寸（只触发真几何读取）', () => {
  const deps = () => {
    const measured: number[] = []
    const onResize: number[] = []
    const m = createRowHeightMemory()
    const fn = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'k', sig: 's' }),
      measure: () => {
        measured.push(1)
        return 999
      },
      memory: m,
      width: () => 800,
      onResize: () => onResize.push(1),
      currentSize: () => 300,
    })
    const el = { dataset: { index: '0' } } as unknown as Element
    return { fn, m, el, measured, onResize }
  }

  it('过期快照（比真实高度小）绝不允许缩行 —— 返回记账尺寸并标脏', () => {
    const { fn, m, el, measured, onResize } = deps()
    m.set('k', 's', 800, 111)
    // 快照说 60，真实几何 300（旧实现返回 60 ⇒ 行矮 240px ⇒ 下一行压上来，永久固化）
    const staleEntry = { borderBoxSize: [{ blockSize: 60, inlineSize: 800 }] } as unknown as ResizeObserverEntry
    expect(fn(el, staleEntry, null)).toBe(300) // 记账尺寸（不变 ⇒ resizeItem 早退）
    expect(onResize).toHaveLength(1) // 标脏 ⇒ 由 flush 读真几何校正
    expect(measured).toHaveLength(0) // 本次不做 DOM 读（批量留给 flush）
    expect(m.get('k', 's', 800)).toBe(111) // 记忆不被快照污染
  })

  it('快照说变大也不直接采纳（唯一真相是真几何，同一批读后写）', () => {
    const { fn, el, onResize } = deps()
    const entry = { borderBoxSize: [{ blockSize: 250, inlineSize: 800 }] } as unknown as ResizeObserverEntry
    expect(fn(el, entry, null)).toBe(300)
    expect(onResize).toHaveLength(1)
  })

  it('无 entry（挂载）⇒ 提示优先（记忆/估算），真实高度由 flush 校正', () => {
    const { fn, m, el } = deps()
    m.set('k', 's', 800, 111)
    const fnWithHint = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'k', sig: 's' }),
      hint: () => 111,
      measure: () => 999,
      memory: m,
      width: () => 800,
      onResize: () => {},
      currentSize: () => 300,
    })
    expect(fnWithHint(el, undefined, null)).toBe(111)
    // 无提示（未记忆）⇒ 真实测量兜底
    expect(fn(el, undefined, null)).toBe(999)
  })
})
