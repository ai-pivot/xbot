import { describe, expect, it } from 'vitest'
import type { DockviewApi } from 'dockview-core'

import { enableCardDrag, hitZone, nearestRectIndex, type Rect } from './cardDrag'

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

describe('nearestRectIndex（drop 目标选择：精确命中优先 + 最近 fallback）', () => {
  const rects: Rect[] = [
    { x: 0, y: 0, w: 200, h: 300 }, // sidebar 列（左）
    { x: 500, y: 0, w: 500, h: 300 }, // 另一候选（右）
  ]

  it('精确命中优先（pointer 在矩形内直接返回，不比距离）', () => {
    expect(nearestRectIndex(100, 150, rects)).toBe(0)
    expect(nearestRectIndex(700, 150, rects)).toBe(1)
  })

  it('rect 外 fallback 取最近 — 主卡片拖动场景（源已过滤，pointer 在源区域）', () => {
    // 源主卡片（右侧 80%）被过滤后候选只剩左侧 sidebar；
    // pointer 在原主卡片区域（rect 外）→ 最近 sidebar 兜底
    const candidates = [rects[0]]
    expect(nearestRectIndex(600, 150, candidates)).toBe(0)
    expect(nearestRectIndex(950, 20, candidates)).toBe(0)
  })

  it('rect 外双候选按距离最近判定', () => {
    // pointer (300, 50)：距 rect0 右边 100，距 rect1 左边 200 → 取 rect0
    expect(nearestRectIndex(300, 50, rects)).toBe(0)
    // pointer (420, 50)：距 rect0 右边 220，距 rect1 左边 80 → 取 rect1
    expect(nearestRectIndex(420, 50, rects)).toBe(1)
  })

  it('空候选返回 -1（单卡片拖不动，无目标可落）', () => {
    expect(nearestRectIndex(10, 10, [])).toBe(-1)
  })
})

describe('Ctrl 光标提示（armed class 切换）', () => {
  const setup = () => {
    const host = document.createElement('div')
    const api = { groups: [] } as unknown as DockviewApi
    const dispose = enableCardDrag(api, host)
    return { host, dispose }
  }
  const fire = (type: string, key: string) =>
    window.dispatchEvent(new KeyboardEvent(type, { key }))

  it('按住 Ctrl 时 host 加 ctrl-drag-armed（CSS 光标 grab），松开移除', () => {
    const { host, dispose } = setup()
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
    fire('keydown', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(true)
    // keydown repeat（按住连发）幂等
    fire('keydown', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(true)
    fire('keyup', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
    dispose()
  })

  it('非 Ctrl 按键不触发 armed class', () => {
    const { host, dispose } = setup()
    fire('keydown', 'a')
    fire('keydown', 'Shift')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
    dispose()
  })

  it('窗口失焦时复位（Ctrl 状态可能已丢失）', () => {
    const { host, dispose } = setup()
    fire('keydown', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(true)
    window.dispatchEvent(new Event('blur'))
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
    dispose()
  })

  it('dispose 后移除监听并清掉 class', () => {
    const { host, dispose } = setup()
    fire('keydown', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(true)
    dispose()
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
    // dispose 后监听已摘除，按键不再加 class
    fire('keydown', 'Control')
    expect(host.classList.contains('ctrl-drag-armed')).toBe(false)
  })
})
