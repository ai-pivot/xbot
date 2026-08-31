import { describe, expect, it } from 'vitest'
import type { DockviewApi } from 'dockview-core'

import { enableCardDrag, nearestRectIndex, quadrantZone, type Rect } from './cardDrag'

const R: Rect = { x: 0, y: 0, w: 400, h: 300 }
// 中央 no-op 容差 ±0.15：x ∈ (140, 260) 且 y ∈ (105, 195) → null

describe('quadrantZone（拖动落点方位：相对源卡片四分，拖向哪边放哪边）', () => {
  it('源中央小区 → null（拖动意图不明确，不落子）', () => {
    expect(quadrantZone(R, 200, 150)).toBeNull()
    // 中央容差 ±0.1：x∈(160,240) 且 y∈(120,180)
    expect(quadrantZone(R, 230, 155)).toBeNull()
    expect(quadrantZone(R, 180, 150)).toBeNull()
  })

  it('源左半（含源外更左）→ left（换边：源放到最近邻居左边）', () => {
    expect(quadrantZone(R, 100, 150)).toBe('left')
    expect(quadrantZone(R, 0, 150)).toBe('left')
    // pointer 拖到源外远处（左侧 sidebar 区域）→ 仍 left
    expect(quadrantZone(R, -200, 150)).toBe('left')
  })

  it('源右半（含源外更右）→ right', () => {
    expect(quadrantZone(R, 300, 150)).toBe('right')
    expect(quadrantZone(R, 400, 150)).toBe('right')
    expect(quadrantZone(R, 600, 150)).toBe('right')
  })

  it('源上/下半 → top/bottom（垂直分屏）', () => {
    expect(quadrantZone(R, 200, 50)).toBe('top')
    expect(quadrantZone(R, 200, 0)).toBe('top')
    expect(quadrantZone(R, 200, 250)).toBe('bottom')
  })

  it('斜向落点 → 归一化距离更远的轴（dx vs dy）', () => {
    // (50, 50)：relX=0.125, relY=0.167 → dx=0.375 > dy=0.333 → 水平主导 → left
    expect(quadrantZone(R, 50, 50)).toBe('left')
    // (200, 20)：relY=0.067 → dy=0.433 > dx=0 → top
    expect(quadrantZone(R, 200, 20)).toBe('top')
  })

  it('master/stack 场景：主卡片（右侧 80%）拖向左半屏 → left（换边核心路径）', () => {
    // viewport 1400：sidebar 左 280 宽，主卡 {x:280, w:1120}
    const main: Rect = { x: 280, y: 0, w: 1120, h: 900 }
    // pointer 拖到左侧（sidebar 区域上方，relX 为负）→ left
    expect(quadrantZone(main, 100, 450)).toBe('left')
    // pointer 在主卡左半内 → left
    expect(quadrantZone(main, 700, 450)).toBe('left')
    // pointer 在主卡右半内 → right（原位方向）
    expect(quadrantZone(main, 1200, 450)).toBe('right')
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
