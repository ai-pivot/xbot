import { describe, expect, it } from 'vitest'

import { hitZone, type Rect } from './cardDrag'

const R: Rect = { x: 0, y: 0, w: 400, h: 300 }
// 边缘带：宽 400×0.25=100，高 300×0.25=75

describe('hitZone（drop 方位判定，25% 边缘带）', () => {
  it('中心区 → center（并入目标卡片为 tab）', () => {
    expect(hitZone(R, 200, 150)).toBe('center')
    expect(hitZone(R, 150, 120)).toBe('center')
    expect(hitZone(R, 250, 180)).toBe('center')
  })

  it('左边缘带 → left', () => {
    expect(hitZone(R, 30, 150)).toBe('left')
    expect(hitZone(R, 0, 150)).toBe('left')
  })

  it('右边缘带 → right', () => {
    expect(hitZone(R, 380, 150)).toBe('right')
    expect(hitZone(R, 400, 150)).toBe('right')
  })

  it('上/下边缘带 → top/bottom', () => {
    expect(hitZone(R, 200, 30)).toBe('top')
    expect(hitZone(R, 200, 290)).toBe('bottom')
  })

  it('角部 → 深度比例更近的边（左上角落左/上取决于比例）', () => {
    // 左上角 (20, 15)：距左 20/100=0.2，距上 15/75=0.2 → 相等归水平（left）
    expect(hitZone(R, 20, 15)).toBe('left')
    // 更靠上：(20, 5)：距左 20/100=0.2，距上 5/75=0.067 → top
    expect(hitZone(R, 20, 5)).toBe('top')
    // 更靠左：(5, 40)：距左 5/100=0.05 < 距上 40/75=0.53 → left
    expect(hitZone(R, 5, 40)).toBe('left')
    // 右下角 (390, 280)：距右 10/100=0.1，距下 20/75=0.27 → right
    expect(hitZone(R, 390, 280)).toBe('right')
  })

  it('非原点矩形同样成立', () => {
    const r: Rect = { x: 1000, y: 600, w: 400, h: 300 }
    expect(hitZone(r, 1020, 750)).toBe('left')
    expect(hitZone(r, 1200, 800)).toBe('center')
    expect(hitZone(r, 1200, 890)).toBe('bottom')
  })
})
