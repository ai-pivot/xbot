import { describe, expect, it, vi } from 'vitest'
import {
  createHeightAwareMeasureElement,
  createRowHeightMemory,
  createWidthTracker,
  rowSignature,
} from './rowHeightMemory'

/** 现场（Trace-20260918T000005）：切会话时 `get offsetHeight` + `getBoundingClientRect`
 *  合计占 CPU 39%，1648/991/988ms 长任务 —— 每挂载一行都强制一次整表 reflow。
 *  这组用例守住修复的核心契约：**内容未变 ⇒ 直接返回记忆高度，完全不碰 DOM**。 */
describe('rowHeightMemory（切会话：零 DOM 读复用实测行高）', () => {
  it('命中记忆 ⇒ 不调用底层 measure（零 offsetHeight / getBoundingClientRect）', () => {
    const memory = createRowHeightMemory()
    memory.set('turn-1-assistant', 'sig-a', 800, 420)
    const measure = vi.fn(() => 999)
    const measureElement = createHeightAwareMeasureElement({
      lookup: (i) => (i === 0 ? { key: 'turn-1-assistant', sig: 'sig-a' } : undefined),
      measure: measure as never,
      memory,
      width: () => 800,
    })

    const el = { dataset: { index: '0' } } as unknown as Element
    expect(measureElement(el, undefined, {})).toBe(420)
    expect(measure).not.toHaveBeenCalled() // ← 修复前：每行都会 read offsetHeight
  })

  it('内容指纹变化（流式 partial 行）⇒ 真实测量并更新记忆（不误用旧高度）', () => {
    const memory = createRowHeightMemory()
    memory.set('turn-1-assistant', 'sig-old', 800, 420)
    const measure = vi.fn(() => 512)
    const measureElement = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'turn-1-assistant', sig: 'sig-new' }),
      measure: measure as never,
      memory,
      width: () => 800,
    })

    expect(measureElement({ dataset: { index: '0' } } as unknown as Element, undefined, {})).toBe(512)
    expect(measure).toHaveBeenCalledTimes(1)
    // 记忆已刷新为新指纹
    expect(memory.get('turn-1-assistant', 'sig-new', 800)).toBe(512)
  })

  it('布局宽度变化 ⇒ 记忆作废（高度不变性的前提），必须重新测量', () => {
    const memory = createRowHeightMemory()
    memory.set('k', 'sig', 800, 420)
    const measure = vi.fn(() => 300)
    const measureElement = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'k', sig: 'sig' }),
      measure: measure as never,
      memory,
      width: () => 420, // 窄屏
    })
    expect(measureElement({ dataset: { index: '0' } } as unknown as Element, undefined, {})).toBe(300)
    expect(measure).toHaveBeenCalledTimes(1)
  })

  it('widthTracker：宽度变化触发一次 onChange（清缓存），未变不触发', () => {
    const t = createWidthTracker()
    const onChange = vi.fn()
    t.onChange(onChange)
    t.observe(800)
    expect(onChange).not.toHaveBeenCalled() // 首次记录不算“变化”
    t.observe(800)
    expect(onChange).not.toHaveBeenCalled()
    t.observe(640)
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(t.current()).toBe(640)
  })

  it('rowSignature：按 row 对象记忆化；内容/迭代变化 ⇒ 指纹变化', () => {
    const row = { role: 'assistant', content: 'abc', iterations: [{ content: 'x' }] }
    const sig1 = rowSignature(row)
    expect(rowSignature(row)).toBe(sig1) // 同对象 ⇒ 稳定（WeakMap 命中）
    expect(rowSignature({ ...row, content: 'abcd' })).not.toBe(sig1)
    expect(rowSignature({ ...row, isPartial: true })).not.toBe(sig1)
    expect(rowSignature({ ...row, iterations: [{ content: 'x' }, { content: 'y' }] })).not.toBe(sig1)
  })

  it('同一元素实例的第二次测量必须真实读取 —— 折叠/图片/mermaid 等异步改高不能被吞', () => {
    const memory = createRowHeightMemory()
    memory.set('k', 'sig', 800, 420)
    const measure = vi.fn(() => 777)
    const measureElement = createHeightAwareMeasureElement({
      lookup: () => ({ key: 'k', sig: 'sig' }),
      measure: measure as never,
      memory,
      width: () => 800,
    })
    const el = { dataset: { index: '0' } } as unknown as Element

    // 首次（挂载爆发期）：走缓存，零 DOM 读
    expect(measureElement(el, undefined, {})).toBe(420)
    expect(measure).not.toHaveBeenCalled()

    // 之后（ResizeObserver 因真实尺寸变化回调）：必须真实读取，并刷新缓存
    expect(measureElement(el, undefined, {})).toBe(777)
    expect(measure).toHaveBeenCalledTimes(1)
    expect(memory.get('k', 'sig', 800)).toBe(777)
  })
})
