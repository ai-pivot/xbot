/**
 * PanelLayout v5.1「Focus + Drawer」停靠引擎测试。
 *
 * 覆盖规格验收（每类至少 1 例）+ 拖拽协议 v5 修的三个 bug：
 *  - v1→v2 迁移幂等（migrateV1Layout 纯函数）+ v2→v5.1 迁移（side 非 sessions
 *    → chip，幂等，端到端 localStorage 渲染迁移）
 *  - 默认分配：core.sessions → side 置顶 h 420；其余内置 → chip；插件
 *    contribution（def.location）尊重
 *  - 钉选/取消钉选（chips 📌 → side h 220 append 尾；side ✕ → chip；
 *    PINNED_DEFAULTS 面板无 ✕）
 *  - 底边调高（move 零持久化 + body height 跟随；up 一次落盘 clamp 140–640；
 *    零挤压结构断言）
 *  - SideChips（渲染 / 单击 float / 拖入收纳）
 *  - BadgeSlot 宽度锁定（只增不减 + tabular-nums）
 *  - ＋N 收纳（TopRail 依次测量、尾部收纳、菜单项 = 徽章 popover、⤢ 升浮窗）
 *  - zone 判定（move 中 elementFromPoint 判 activeZone + 宿主 ring + ghost 形态）
 *  - 修 bug 1：拖动/缩放 move 中零持久化（localStorage 不变），up 一次写入
 *  - 修 bug 2：dropHint 真实写入（插入线渲染），up 清空
 *  - 修 bug 3：重排基于渲染序 sideIds（钉选起点），落盘完整 order
 *  - 取消路径：落点无 zone / Esc / 4px 阈值内松手 → 零状态变更
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { panelRegistry } from '@/plugin-runtime/panelRegistry'
import type { PanelDefinition } from '@/plugin-api'
import type { TabManager } from '@/hooks/useTabManager'
import {
  FloatingLayer,
  PanelDock,
  PanelDockProvider,
  defaultPanelLayout,
  migrateV1Layout,
  migrateV2Layout,
  parsePanelLayoutV2,
} from './PanelLayout'
import { TopRail } from './rails'

// radix Popover（@floating-ui 定位）在 jsdom 里需要 ResizeObserver。
class ROStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(window as unknown as { ResizeObserver: unknown }).ResizeObserver = ROStub
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ROStub

const V2_KEY = 'xbot:panel-layout-v2'
const V1_KEY = 'xbot:panel-layout'

const fakeTabManager = { openTab: vi.fn() } as unknown as TabManager

const registeredIds: string[] = []
function registerPanel(def: PanelDefinition): void {
  panelRegistry.registerPanel(def)
  registeredIds.push(def.id)
}

function makeDef(id: string, title: string, overrides: Partial<PanelDefinition> = {}): PanelDefinition {
  return {
    id,
    title,
    icon: 'blocks',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: () => <div data-testid={`body-${id}`}>{title}</div>,
    ...overrides,
  }
}

// ── jsdom 环境 stub ─────────────────────────────────────────────────────────

let elementFromPointImpl: (x: number, y: number) => Element | null = () => null

beforeEach(() => {
  localStorage.clear()
  // jsdom 未实现 elementFromPoint——按测试用例注入映射。
  elementFromPointImpl = () => null
  Object.defineProperty(document, 'elementFromPoint', {
    configurable: true,
    writable: true,
    value: (x: number, y: number) => elementFromPointImpl(x, y),
  })
})

afterEach(() => {
  delete (document as unknown as { elementFromPoint?: unknown }).elementFromPoint
  for (const id of registeredIds.splice(0)) panelRegistry.unregisterPanel(id)
  vi.restoreAllMocks()
})

function renderShell(): ReturnType<typeof renderWithProviders> {
  return renderWithProviders(
    <PanelDockProvider tabManager={fakeTabManager}>
      <div style={{ position: 'relative', width: 1000, height: 800 }}>
        <PanelDock />
        <TopRail className="max-w-[300px]" />
        <FloatingLayer />
      </div>
    </PanelDockProvider>,
  )
}

function gripOf(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-panel-id="${id}"] [aria-label="拖拽重排面板"]`)
  if (!el) throw new Error(`grip of ${id} not found`)
  return el
}

/** side 钉选堆叠当前渲染顺序（渲染序 = 重排基准的观察窗口）。 */
function sideRenderOrder(): string[] {
  const stack = document.querySelector<HTMLElement>('[data-testid="panel-dock-stack"]')
  if (!stack) throw new Error('side stack not found')
  return [...stack.querySelectorAll('[data-panel-id]')].map((el) => el.getAttribute('data-panel-id')!)
}

/** chips 启动器当前渲染顺序。 */
function chipOrder(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[data-panel-chip]')].map((el) => el.getAttribute('data-panel-chip')!)
}

// ── v1→v2 迁移 ──────────────────────────────────────────────────────────────

describe('migrateV1Layout（v1→v2 迁移）', () => {
  const known = new Set(['p.a', 'p.b', 'p.f'])
  const V1 = JSON.stringify({
    panels: {
      'p.a': { mode: 'docked', x: 0, y: 0, w: 0, h: 0, collapsed: false },
      'p.b': { mode: 'docked', x: 0, y: 0, w: 0, h: 0, collapsed: true },
      'p.f': { mode: 'floating', x: 120, y: 80, w: 360, h: 240, collapsed: false },
      'p.gone': { mode: 'docked', x: 0, y: 0, w: 0, h: 0, collapsed: false },
    },
    dockOrder: ['p.b', 'p.a'],
  })

  it('迁移幂等：重复执行结果一致，输出可作为 v2 再解析不丢数据', () => {
    const first = migrateV1Layout(V1, known)
    const second = migrateV1Layout(V1, known)
    expect(first).not.toBeNull()
    expect(first).toEqual(second)
    // 幂等的第二形态：迁移输出作为 v2 数据再解析，结果不变。
    expect(parsePanelLayoutV2(JSON.stringify(first), known)).toEqual(first)
  })

  it('docked 保持 dockOrder 序（v2 形状 side）；floating 保留 xywh；未知 id 丢弃', () => {
    const migrated = migrateV1Layout(V1, known)!
    expect(migrated['p.b'].loc).toEqual({ zone: 'side', order: 0 })
    expect(migrated['p.a'].loc).toEqual({ zone: 'side', order: 1 })
    expect(migrated['p.f'].loc).toEqual({ zone: 'floating', order: 0, x: 120, y: 80, w: 360, h: 240 })
    expect(migrated['p.gone']).toBeUndefined()
  })

  it('读失败/坏数据回退 null（默认布局）；坏 entry / 未知 id 丢弃', () => {
    const knownOne = new Set(['p.a'])
    expect(migrateV1Layout('not json', knownOne)).toBeNull()
    expect(migrateV1Layout(JSON.stringify({ panels: 'x', dockOrder: [] }), knownOne)).toBeNull()
    expect(parsePanelLayoutV2('not json', knownOne)).toBeNull()
    // zone 非法 → entry 丢弃（回退该面板默认）。
    const badEntry = JSON.stringify({ 'p.a': { loc: { zone: 'nowhere', order: 0 }, collapsed: true } })
    expect(parsePanelLayoutV2(badEntry, knownOne)).toEqual({})
    // h 非有限数字 → entry 丢弃。
    const badH = JSON.stringify({ 'p.a': { loc: { zone: 'side', order: 0, h: 'x' }, collapsed: true } })
    expect(parsePanelLayoutV2(badH, knownOne)).toEqual({})
  })

  it('端到端：localStorage v1 → 渲染即迁移（非 sessions 面板收入 chips），首次交互才写 v2', () => {
    registerPanel(makeDef('p.a', 'A'))
    registerPanel(makeDef('p.b', 'B'))
    localStorage.setItem(V1_KEY, JSON.stringify({
      panels: {
        'p.a': { mode: 'docked', x: 0, y: 0, w: 0, h: 0, collapsed: false },
        'p.b': { mode: 'docked', x: 0, y: 0, w: 0, h: 0, collapsed: true },
      },
      dockOrder: ['p.b', 'p.a'],
    }))
    renderShell()
    // v5.1：docked 迁移产物再过 migrateV2Layout——side 非 sessions 全收 chips
    // （chips 渲染序 = order 序）。
    expect(chipOrder()).toEqual(['p.b', 'p.a'])
    expect(sideRenderOrder()).toEqual([])
    // 迁移不写盘（v2 key 仍空——写盘推迟到首次交互）。
    expect(localStorage.getItem(V2_KEY)).toBeNull()
    // 首次交互（📌 钉选）→ v2 全量落盘。
    fireEvent.click(screen.getByLabelText('钉选 A'))
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc).toMatchObject({ zone: 'side', order: 0, h: 220 })
    expect(saved['p.b'].loc).toEqual({ zone: 'chip', order: 0 })
  })
})

// ── v2→v5.1 迁移（side → chip）──────────────────────────────────────────────

describe('migrateV2Layout（v2→v5.1 迁移：side 非 sessions → chip）', () => {
  const state: Record<string, { loc: Record<string, unknown>; collapsed: boolean }> = {
    'p.a': { loc: { zone: 'side', order: 0 }, collapsed: true },
    'p.b': { loc: { zone: 'side', order: 1, h: 200 }, collapsed: false },
    'core.sessions': { loc: { zone: 'side', order: 2, h: 420 }, collapsed: false },
    'p.f': { loc: { zone: 'floating', order: 0, x: 10, y: 10, w: 320, h: 240 }, collapsed: false },
  }

  it('side 的非 sessions 面板 → chip（丢弃无意义高度）；sessions 保持 side', () => {
    const next = migrateV2Layout(state as never)
    expect(next['p.a'].loc).toEqual({ zone: 'chip', order: 0 })
    expect(next['p.b'].loc).toEqual({ zone: 'chip', order: 1 })
    expect(next['core.sessions'].loc).toEqual({ zone: 'side', order: 2, h: 420 })
    expect(next['p.f'].loc.zone).toBe('floating')
  })

  it('幂等：重复执行结果一致', () => {
    const first = migrateV2Layout(state as never)
    expect(migrateV2Layout(first)).toEqual(first)
  })

  it('sessions 的 side h 规范化到拖拽 clamp 边界（140–640）', () => {
    const over = migrateV2Layout({
      'core.sessions': { loc: { zone: 'side', order: 0, h: 9999 }, collapsed: false },
    } as never)
    expect(over['core.sessions'].loc.h).toBe(640)
    const under = migrateV2Layout({
      'core.sessions': { loc: { zone: 'side', order: 0, h: 20 }, collapsed: false },
    } as never)
    expect(under['core.sessions'].loc.h).toBe(140)
  })

  it('端到端：localStorage v2（zone side 非 sessions）→ 渲染即收入 chips', () => {
    registerPanel(makeDef('p.a', 'A'))
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.a': { loc: { zone: 'side', order: 3 }, collapsed: false },
    }))
    renderShell()
    expect(chipOrder()).toEqual(['p.a'])
    expect(sideRenderOrder()).toEqual([])
  })
})

// ── 默认分配（v5.1）─────────────────────────────────────────────────────────

describe('defaultPanelLayout（v5.1 默认分配）', () => {
  it('core.sessions → side 置顶（h 420 展开）；其余内置 → chip；插件 contribution 尊重；未知兜底 chip', () => {
    const defs: PanelDefinition[] = [
      makeDef('core.sessions', '会话', { source: 'core' }),
      makeDef('core.files', '文件', { source: 'core' }),
      makeDef('git.panel', 'Git', { source: 'xbot.git', location: { zone: 'side', h: 220, order: 0 } }),
      makeDef('p.x', 'X'),
    ]
    const d = defaultPanelLayout(defs)
    expect(d['core.sessions']).toEqual({ loc: { zone: 'side', order: 0, h: 420 }, collapsed: false })
    expect(d['core.files']).toEqual({ loc: { zone: 'chip', order: 1 }, collapsed: true })
    // 插件声明侧栏容器 → 默认钉选 side（默认 h 220）。
    expect(d['git.panel']).toEqual({ loc: { zone: 'side', h: 220, order: 0 }, collapsed: true })
    // 无 location 的插件面板兜底 chip。
    expect(d['p.x']).toEqual({ loc: { zone: 'chip', order: 3 }, collapsed: true })
  })
})

// ── v5.1 Focus + Drawer（钉选/取消钉选/chips/调高）──────────────────────────

describe('v5.1 Focus + Drawer', () => {
  beforeEach(() => {
    registerPanel(makeDef('p.a', 'A'))
    registerPanel(makeDef('p.b', 'B'))
  })

  it('默认分配：未知插件面板兜底 chips（side 无残留，零挤压结构就位）', () => {
    renderShell()
    expect(chipOrder()).toEqual(['p.a', 'p.b'])
    expect(sideRenderOrder()).toEqual([])
    // 零挤压：堆叠区 overflow-y-auto（超高整栏滚动），chips 条 shrink-0 固定底部。
    const stack = document.querySelector('[data-testid="panel-dock-stack"]')!
    expect(stack.className).toContain('overflow-y-auto')
    expect(stack.className).toContain('flex-1')
    const chips = document.querySelector('[data-testid="panel-chip-dock"]')!
    expect(chips.className).toContain('shrink-0')
  })

  it('chips 📌 钉选：→ side append 堆叠尾 + 默认 h 220 + 展开落盘', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    fireEvent.click(screen.getByLabelText('钉选 B'))
    expect(sideRenderOrder()).toEqual(['p.a', 'p.b'])
    expect(chipOrder()).toEqual([])
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc).toMatchObject({ zone: 'side', order: 0, h: 220 })
    expect(saved['p.a'].collapsed).toBe(false)
    expect(saved['p.b'].loc).toMatchObject({ zone: 'side', order: 1, h: 220 })
  })

  it('side ✕ 取消钉选 → chip；PINNED_DEFAULTS（sessions）无 ✕ 且可调高', () => {
    registerPanel(makeDef('core.sessions', '会话', { source: 'core', icon: 'message' }))
    renderShell()
    // sessions 默认 side 置顶、h 420 展开（entryOf 合成，未交互不落盘）。
    expect(sideRenderOrder()).toEqual(['core.sessions'])
    expect(chipOrder()).toEqual(['p.a', 'p.b'])
    expect(localStorage.getItem(V2_KEY)).toBeNull()
    const sessionsBody = document.querySelector<HTMLElement>('[data-panel-id="core.sessions"] > div')!
    expect(sessionsBody.style.height).toBe('420px')
    expect(sessionsPanel(sessionsBody).querySelector('[aria-label="取消钉选（收入底部启动器）"]')).toBeNull()
    expect(sessionsPanel(sessionsBody).querySelector('[aria-label="调整面板高度"]')).not.toBeNull()

    // p.a 钉选 → 有 ✕ → 取消钉选回 chip。
    fireEvent.click(screen.getByLabelText('钉选 A'))
    const panel = document.querySelector<HTMLElement>('[data-panel-id="p.a"]')!
    fireEvent.click(within(panel).getByLabelText('取消钉选（收入底部启动器）'))
    expect(chipOrder()).toEqual(['p.b', 'p.a'])
    expect(sideRenderOrder()).toEqual(['core.sessions'])
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.zone).toBe('chip')
  })

  it('chip 单击 → floating（复用 floatPanel 主区中上落位），FloatingLayer 渲染', () => {
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.a': { loc: { zone: 'chip', order: 0 }, collapsed: true },
    }))
    renderShell()
    const layer = document.querySelector<HTMLElement>('[data-panel-zone="floating"]')!
    vi.spyOn(layer, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 1000, height: 800 } as DOMRect)
    fireEvent.click(document.querySelector('[data-panel-chip="p.a"]')!)
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.zone).toBe('floating')
    expect(saved['p.a'].collapsed).toBe(false)
    expect(document.querySelector('[data-panel-id="p.a"]')).toBeInTheDocument()
  })

  it('floating 收回（关闭按钮）→ chip（v5.1：浮动退出一律回收纳态）', () => {
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.a': { loc: { zone: 'floating', order: 0, x: 100, y: 100, w: 320, h: 280 }, collapsed: false },
    }))
    renderShell()
    const panel = document.querySelector<HTMLElement>('[data-panel-id="p.a"]')!
    fireEvent.click(within(panel).getByLabelText('关闭浮窗（收入启动器）'))
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.zone).toBe('chip')
    // order 保留（floating 0 / chip 0 并列）→ 稳定排序按注册序 p.a 在前。
    expect(chipOrder()).toEqual(['p.a', 'p.b'])
  })

  it('底边调高：move 零持久化 + body height 跟随，up 一次落盘', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    const before = localStorage.getItem(V2_KEY)
    const handle = document.querySelector<HTMLElement>('[aria-label="调整面板高度"]')!
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(handle, { clientX: 100, clientY: 150 })
    // move 中本地跟随（220 + 50 = 270），零持久化。
    const body = document.querySelector<HTMLElement>('[data-panel-id="p.a"] > div')!
    expect(body.style.height).toBe('270px')
    expect(localStorage.getItem(V2_KEY)).toBe(before)
    fireEvent.pointerUp(handle)
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.h).toBe(270)
  })

  it('底边调高 clamp：拖超上界 640 / 拖过下界 140', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    const handle = document.querySelector<HTMLElement>('[aria-label="调整面板高度"]')!
    // 上界：从 220 起 +2000 → clamp 640。
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(handle, { clientX: 100, clientY: 2100 })
    fireEvent.pointerUp(handle)
    let saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.h).toBe(640)
    // 下界：从 640 起 -2000 → clamp 140。
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(handle, { clientX: 100, clientY: -1900 })
    fireEvent.pointerUp(handle)
    saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.h).toBe(140)
  })

  it('插件 contribution 尊重：def.location side h 220 → 默认钉选（折叠态标题栏，展开后 body 220）', () => {
    registerPanel(makeDef('git.panel', 'Git', { source: 'xbot.git', location: { zone: 'side', h: 220, order: 0 } }))
    renderShell()
    expect(sideRenderOrder()).toEqual(['git.panel'])
    expect(chipOrder()).toEqual(['p.a', 'p.b'])
    const panel = document.querySelector<HTMLElement>('[data-panel-id="git.panel"]')!
    fireEvent.click(within(panel).getByLabelText('展开'))
    const body = panel.querySelector<HTMLElement>(':scope > div')!
    expect(body.style.height).toBe('220px')
  })

  it('拖 side 面板到底部 chips 条 → zone chip（跨 zone 放置）', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    fireEvent.click(screen.getByLabelText('钉选 B'))
    expect(sideRenderOrder()).toEqual(['p.a', 'p.b'])
    const chipsHost = document.querySelector<HTMLElement>('[data-testid="panel-chip-dock"]')!
    elementFromPointImpl = (x, y) => (x === 100 && y === 700 ? chipsHost : null)
    const grip = gripOf('p.a')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 100, clientY: 700 })
    fireEvent.pointerUp(grip, { clientX: 100, clientY: 700 })
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.zone).toBe('chip')
    expect(sideRenderOrder()).toEqual(['p.b'])
  })

  it('openPanel request：chip 面板 → 升浮窗（打开即见，不强制钉选）', () => {
    renderShell()
    fireEvent(window, new CustomEvent('xbot:panel-request', { detail: { id: 'p.a' } }))
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.zone).toBe('floating')
  })
})

/** 由 body 元素向上找面板 section（✕/handle 断言用）。 */
function sessionsPanel(body: HTMLElement): HTMLElement {
  return body.closest<HTMLElement>('[data-panel-id]')!
}

// ── 拖拽协议 v5 ─────────────────────────────────────────────────────────────

describe('拖拽协议 v5', () => {
  beforeEach(() => {
    registerPanel(makeDef('p.a', 'A'))
    registerPanel(makeDef('p.b', 'B'))
  })

  it('修 bug 3：重排基于渲染序 sideIds——钉选起点拖 B 到 A 上半部 → 完整 order 落盘', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    fireEvent.click(screen.getByLabelText('钉选 B'))
    expect(sideRenderOrder()).toEqual(['p.a', 'p.b'])
    const elA = document.querySelector<HTMLElement>('[data-dock-item="p.a"]')!
    vi.spyOn(elA, 'getBoundingClientRect').mockReturnValue({ top: 0, height: 100, left: 0, width: 200 } as DOMRect)
    elementFromPointImpl = (x, y) => (x === 100 && y === 40 ? elA : null)
    const grip = gripOf('p.b')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 100, clientY: 40 })
    fireEvent.pointerUp(grip, { clientX: 100, clientY: 40 })
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.b'].loc).toMatchObject({ zone: 'side', order: 0 })
    expect(saved['p.a'].loc).toMatchObject({ zone: 'side', order: 1 })
    expect(sideRenderOrder()).toEqual(['p.b', 'p.a'])
  })

  it('修 bug 1：move 中零持久化（拖动 + resize 均只改本地），up 后一次写入', () => {
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.a': { loc: { zone: 'floating', order: 0, x: 100, y: 100, w: 320, h: 280 }, collapsed: false },
      'p.b': { loc: { zone: 'chip', order: 0 }, collapsed: true },
    }))
    renderShell()
    const layer = document.querySelector<HTMLElement>('[data-panel-zone="floating"]')!
    vi.spyOn(layer, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 1000, height: 800 } as DOMRect)
    const before = localStorage.getItem(V2_KEY)
    // resize：move 中尺寸跟随（width 370px）但 localStorage 不写。
    const handle = document.querySelector<HTMLElement>('[aria-label="调整面板大小"]')!
    fireEvent.pointerDown(handle, { button: 0, clientX: 420, clientY: 380 })
    fireEvent.pointerMove(handle, { clientX: 470, clientY: 430 })
    const panel = document.querySelector<HTMLElement>('[data-panel-id="p.a"]')!
    expect(panel.style.width).toBe('370px')
    expect(localStorage.getItem(V2_KEY)).toBe(before)
    fireEvent.pointerUp(handle)
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc).toMatchObject({ w: 370, h: 330 })
  })

  it('修 bug 2：dropHint 真实写入——move 中插入线渲染，up 后清空', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 A'))
    fireEvent.click(screen.getByLabelText('钉选 B'))
    const elA = document.querySelector<HTMLElement>('[data-dock-item="p.a"]')!
    vi.spyOn(elA, 'getBoundingClientRect').mockReturnValue({ top: 0, height: 100, left: 0, width: 200 } as DOMRect)
    elementFromPointImpl = (x, y) => (x === 100 && y === 40 ? elA : null)
    const grip = gripOf('p.b')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 100, clientY: 40 })
    expect(document.querySelector('[data-panel-id="p.a"] [data-drop-indicator="before"]')).toBeInTheDocument()
    fireEvent.pointerUp(grip, { clientX: 100, clientY: 40 })
    expect(document.querySelector('[data-drop-indicator]')).toBeNull()
  })

  it('zone 判定：拖到 top rail → 宿主 ring 高亮 + ghost 徽章形态；up 落 top segment 按落点左右半', () => {
    registerPanel(makeDef('p.t', 'T'))
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.t': { loc: { zone: 'top', order: 0 }, collapsed: true },
    }))
    renderShell()
    // p.a 无持久化 → chips；钉选后进 side（拖拽源需要 grip）。
    fireEvent.click(screen.getByLabelText('钉选 A'))
    const rail = document.querySelector<HTMLElement>('[data-panel-zone="top"]')!
    vi.spyOn(rail, 'getBoundingClientRect').mockReturnValue({ left: 0, top: 0, width: 400, height: 32 } as DOMRect)
    elementFromPointImpl = (x, y) => (x === 100 && y === 20 ? rail : null)
    const grip = gripOf('p.a')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 100, clientY: 20 })
    // zone 高亮（宿主根元素）+ ghost 形态预告（非 floating → 徽章）。
    expect(rail.getAttribute('data-zone-active')).toBe('true')
    expect(document.querySelector('[data-testid="panel-drag-ghost"]')).toHaveAttribute('data-ghost-mode', 'badge')
    fireEvent.pointerUp(grip, { clientX: 100, clientY: 20 })
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    // 落点 x=100 < rail 中线 200 → left 半区。
    expect(saved['p.a'].loc).toMatchObject({ zone: 'top', segment: 'left' })
    expect(rail.getAttribute('data-zone-active')).toBeNull()
    expect(document.querySelector('[data-testid="panel-drag-ghost"]')).toBeNull()
  })

  it('取消路径：落点无 zone / Esc / 4px 阈值内松手 → 零状态变更', () => {
    renderShell()
    fireEvent.click(screen.getByLabelText('钉选 B'))
    const pinned = localStorage.getItem(V2_KEY)
    // 落点无 zone：up 在任何 [data-panel-zone] 之外 → 不写。
    elementFromPointImpl = () => null
    let grip = gripOf('p.b')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 100, clientY: 40 })
    fireEvent.pointerUp(grip, { clientX: 100, clientY: 40 })
    expect(localStorage.getItem(V2_KEY)).toBe(pinned)
    // Esc：move 中按 Esc → 零变更 + 拖拽态清空。
    elementFromPointImpl = () => document.querySelector<HTMLElement>('[data-panel-zone="side"]')
    grip = gripOf('p.b')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 200, clientY: 40 })
    expect(document.querySelector('[data-zone-active]')).not.toBeNull()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(localStorage.getItem(V2_KEY)).toBe(pinned)
    expect(document.querySelector('[data-zone-active]')).toBeNull()
    expect(document.querySelector('[data-testid="panel-drag-ghost"]')).toBeNull()
    // 4px 阈值：位移 ≤ 4px 松手 = 点击误触，零变更。
    grip = gripOf('p.b')
    fireEvent.pointerDown(grip, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(grip, { clientX: 102, clientY: 100 })
    fireEvent.pointerUp(grip, { clientX: 102, clientY: 100 })
    expect(localStorage.getItem(V2_KEY)).toBe(pinned)
  })
})

// ── TopRail ＋N 收纳 ────────────────────────────────────────────────────────

describe('TopRail ＋N 收纳', () => {
  it('徽章依次测量，放不下从尾部收进 ＋N 菜单；菜单项 = 徽章 popover（⤢ 升为浮窗）', () => {
    const items: Array<[string, string, number]> = [['p.a', 'A', 0], ['p.b', 'B', 1], ['p.c', 'C', 2]]
    for (const [id, title, order] of items) {
      registerPanel(makeDef(id, title))
      localStorage.setItem(V2_KEY, JSON.stringify({
        ...(JSON.parse(localStorage.getItem(V2_KEY) ?? '{}')),
        [id]: { loc: { zone: 'top', order }, collapsed: true },
      }))
    }
    // 容器宽 200、徽章宽 100：第 2 个起放不下（＋N 预留）→ 收进 ＋N 菜单。
    const origGBCR = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.dataset?.railBadge) return { left: 0, top: 0, width: 100, height: 24, right: 100, bottom: 24 } as DOMRect
      if (this.dataset?.panelZone === 'top') return { left: 0, top: 0, width: 200, height: 32, right: 200, bottom: 32 } as DOMRect
      return origGBCR.call(this)
    })
    const clientWidthSpy = vi.spyOn(Element.prototype, 'clientWidth', 'get')
      .mockImplementation(function (this: Element) {
        return (this as HTMLElement).dataset?.panelZone === 'top' ? 200 : 0
      })
    try {
      renderShell()
      const rail = document.querySelector<HTMLElement>('[data-panel-zone="top"]')!
      // 绝不溢出：收纳后只渲染可见徽章；容器本身 overflow-hidden + 消费方 max-width 透传。
      expect(rail.className).toContain('overflow-hidden')
      expect(rail.className).toContain('max-w-[300px]')
      expect(rail.querySelectorAll('[data-rail-badge]')).toHaveLength(1)
      const plus = screen.getByTestId('rail-overflow-button')
      expect(plus).toHaveTextContent('2')
      // ＋N 菜单列出被收纳徽章。
      fireEvent.click(plus)
      const item = document.querySelector<HTMLElement>('[data-rail-overflow-item="p.c"]')!
      expect(item).toBeInTheDocument()
      // 点击项 = 徽章 popover（紧凑详情）。
      fireEvent.click(item)
      expect(document.querySelector('[data-rail-detail="p.c"]')).toBeInTheDocument()
      // ⤢ 升为浮窗 → 面板 zone 变 floating（FloatingLayer 渲染）。
      fireEvent.click(screen.getByTestId('rail-detail-float'))
      const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
      expect(saved['p.c'].loc.zone).toBe('floating')
    } finally {
      clientWidthSpy.mockRestore()
    }
  })
})

// ── BadgeSlot 宽度锁定（v5.1 硬性要求）──────────────────────────────────────

describe('BadgeSlot 徽章宽度锁定', () => {
  it('宽度只增不减 + tabular-nums：内容 847→1042→1 不抖动', () => {
    let badgeText = '847'
    let slotW = 30
    const origGBCR = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      // 徽章按钮固定 50px（保证收纳算法让徽章内联渲染——BadgeSlot 才存在）。
      if (this.dataset?.railBadge) return { left: 0, top: 0, width: 50, height: 24, right: 50, bottom: 24 } as DOMRect
      if (this.dataset?.badgeSlot !== undefined) {
        return { left: 0, top: 0, width: slotW, height: 20, right: slotW, bottom: 20 } as DOMRect
      }
      return origGBCR.call(this)
    })
    const clientWidthSpy = vi.spyOn(Element.prototype, 'clientWidth', 'get')
      .mockImplementation(function (this: Element) {
        return (this as HTMLElement).dataset?.panelZone === 'top' ? 200 : 0
      })
    try {
      registerPanel(makeDef('p.b1', 'B1', { badges: () => ({ text: badgeText, color: '#f59e0b' }) }))
      localStorage.setItem(V2_KEY, JSON.stringify({
        'p.b1': { loc: { zone: 'top', order: 0 }, collapsed: true },
      }))
      const view = renderWithProviders(
        <PanelDockProvider tabManager={fakeTabManager}>
          <TopRail className="max-w-[300px]" />
        </PanelDockProvider>,
      )
      // 注意：Popover 宿主重渲染会重建徽章 DOM——断言前必须重新查询（旧引用 detached）。
      const slotOf = () => {
        const el = document.querySelector<HTMLElement>('[data-badge-slot]')
        if (!el) throw new Error('badge slot not found')
        return el
      }
      expect(slotOf().style.minWidth).toBe('30px')
      expect(slotOf().style.fontVariantNumeric).toBe('tabular-nums')
      // 内容变宽（847→1042）→ minWidth 增至新内容宽。
      badgeText = '1042'
      slotW = 42
      view.rerender(
        <PanelDockProvider tabManager={fakeTabManager}>
          <TopRail className="max-w-[300px]" />
        </PanelDockProvider>,
      )
      expect(slotOf().style.minWidth).toBe('42px')
      // 内容变窄（1042→1）→ minWidth 保持锁定（只增不减，锁定值由组件 ref 持有）。
      badgeText = '1'
      slotW = 10
      view.rerender(
        <PanelDockProvider tabManager={fakeTabManager}>
          <TopRail className="max-w-[300px]" />
        </PanelDockProvider>,
      )
      expect(slotOf().style.minWidth).toBe('42px')
    } finally {
      clientWidthSpy.mockRestore()
    }
  })
})
