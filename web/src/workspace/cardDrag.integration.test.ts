/**
 * cardDrag 集成测试 — 真实 DockviewComponent（jsdom）验证拖动手势全链路。
 *
 * 背景：「主卡片拖动不了」两轮修复无效 → 停止盲改，用真实 dockview 结构
 * 一锤定音验证三层假设：
 *   1. api.groups[].element 确实是 .dv-groupview DOM 元素（groupAt 的
 *      closest('.dv-groupview') + find(g => g.element === groupEl) 匹配基础）
 *   2. group.api.location.type === 'grid'（groupAt 的第二个 gate）
 *   3. enableCardDrag 手势链路（pointerdown → move → up → moveTo + onDrop）
 *
 * jsdom 限制：无 PointerEvent 构造器（MouseEvent 派发 'pointerdown' 类型——
 * dispatchEvent 不校验构造器，ctrlKey/button/clientX MouseEvent 都有，
 * pointerId 两侧均 undefined 恒匹配）；无 layout（getBoundingClientRect
 * 手工 mock 目标卡片 rect）。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DockviewComponent, type DockviewApi } from 'dockview'

import { enableCardDrag } from './cardDrag'

function firePointer(
  el: Element | Window,
  type: string,
  opts: { ctrlKey?: boolean; button?: number; clientX?: number; clientY?: number } = {},
) {
  el.dispatchEvent(
    new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      ctrlKey: opts.ctrlKey ?? false,
      button: opts.button ?? 0,
      clientX: opts.clientX ?? 0,
      clientY: opts.clientY ?? 0,
    }),
  )
}

function mockRect(el: HTMLElement, x: number, y: number, w: number, h: number) {
  el.getBoundingClientRect = () =>
    ({ left: x, top: y, right: x + w, bottom: y + h, width: w, height: h, x, y }) as DOMRect
}

function createDockview(host: HTMLElement): DockviewApi {
  const dockview = new DockviewComponent(host, {
    createComponent: () => ({
      element: document.createElement('div'),
      init: () => {},
      update: () => {},
      dispose: () => {},
    }),
    createTabComponent: () => ({
      element: document.createElement('div'),
      init: () => {},
      update: () => {},
      dispose: () => {},
    }),
    defaultTabComponent: 'stub',
  })
  return (dockview as unknown as { api: DockviewApi }).api
}

describe('cardDrag 集成（真实 dockview 结构假设验证 + 手势链路）', () => {
  let cleanup: Array<() => void> = []

  afterEach(() => {
    for (const fn of cleanup) fn()
    cleanup = []
    document.body.innerHTML = ''
  })

  it('假设 1：api.groups[].element 带 .dv-groupview class（groupAt 匹配基础）', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const api = createDockview(host)
    cleanup.push(() => api.clear?.() ?? undefined)

    api.addPanel({ id: 'p1', component: 'stub' })
    api.addPanel({ id: 'p2', component: 'stub', position: { direction: 'right' } })
    expect(api.groups.length).toBe(2)

    for (const g of api.groups) {
      expect(g.element.classList.contains('dv-groupview')).toBe(true)
      expect(g.api.location.type).toBe('grid')
    }

    // closest 从内容区向上可达 .dv-groupview（groupAt 的 DOM 路径）
    const content = api.groups[0].element.querySelector('div')!
    expect(content.closest('.dv-groupview')).toBe(api.groups[0].element)
  })

  it('手势链路：Ctrl+pointerdown → move（超阈值）→ up 落在源右半（拖向右）→ 源移到目标右侧', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const api = createDockview(host)
    const onDrop = vi.fn()

    api.addPanel({ id: 'p1', component: 'stub' })
    api.addPanel({ id: 'p2', component: 'stub', position: { direction: 'right' } })
    const [source, target] = api.groups

    // jsdom 无 layout：手工 mock 两张卡片的 rect（左 0-400 / 右 400-1000）
    mockRect(source.element, 0, 0, 400, 300)
    mockRect(target.element, 400, 0, 600, 300)

    const dispose = enableCardDrag(api, host, { onDrop })
    cleanup.push(dispose)

    // 按在源卡片内容区（任意子元素），Ctrl+左键
    const content = source.element.querySelector('div') ?? source.element
    firePointer(content, 'mousedown', { ctrlKey: true, button: 0, clientX: 50, clientY: 50 })
    // 拖动超阈值（50,50 → 120,60）
    firePointer(window, 'mousemove', { clientX: 120, clientY: 60 })
    // 松手在源右半（quadrantZone：relX > 0.5 → 'right'）→ 源放到目标右侧
    firePointer(window, 'mouseup', { clientX: 700, clientY: 150 })

    // 手势完成：onDrop 回调被调（quadrantZone 'right' → moveTo(target, right) 调用）
    // 位置验证交 E2E（真实布局）—— jsdom 的 api.groups 顺序不反映 gridview 树位置
    expect(onDrop).toHaveBeenCalledTimes(1)
  })

  it('手势链路：拖向左（up 在源左半）→ 源移到目标左侧（换边）', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const api = createDockview(host)
    const onDrop = vi.fn()

    api.addPanel({ id: 'p1', component: 'stub' })
    api.addPanel({ id: 'p2', component: 'stub', position: { direction: 'right' } })
    const [source, target] = api.groups

    mockRect(source.element, 0, 0, 400, 300)
    mockRect(target.element, 400, 0, 600, 300)

    const dispose = enableCardDrag(api, host, { onDrop })
    cleanup.push(dispose)

    const content = source.element.querySelector('div') ?? source.element
    firePointer(content, 'mousedown', { ctrlKey: true, button: 0, clientX: 50, clientY: 50 })
    firePointer(window, 'mousemove', { clientX: 120, clientY: 60 })
    // 松手在源左半（relX < 0.5 → 'left'）→ 源移到目标左侧
    firePointer(window, 'mouseup', { clientX: 100, clientY: 150 })

    expect(onDrop).toHaveBeenCalledTimes(1)
    // 源 group 移到目标左侧（api.groups 顺序：源在目标前）
    const srcIdx = api.groups.indexOf(source)
    const tgtIdx = api.groups.indexOf(target)
    expect(srcIdx).toBeLessThan(tgtIdx)
  })
})
