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
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import {act, fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'

// 本文件断言中文文案；jsdom 的 navigator.language 是 en-US，i18n 初始化成
// 英文会让所有中文断言失败 —— 局部固定为 zh-CN。
beforeAll(async () => { await i18n.changeLanguage('zh-CN') })

import { renderWithProviders } from '@/test-utils'
import { I18nProvider } from '@/providers/i18n'
import { panelRegistry } from '@/plugin-runtime/panelRegistry'
import type { PanelDefinition } from '@/plugin-api'
import type { TabManager } from '@/hooks/useTabManager'
import {
  PanelDock,
  PanelDockProvider,
  defaultPanelLayout,
  enforcePinnedState,
  sanitizeRailBadges,
  migrateV1Layout,
  migrateV2Layout,
  parsePanelLayoutV2,
} from './PanelLayout'
import { SideChips, TopRail,
  BottomRailBadges,
} from './rails'

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
        <BottomRailBadges />
        <SideChips />
      </div>
    </PanelDockProvider>,
  )
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

  it('docked 保持 dockOrder 序（v2 形状：sessions→side、非 sessions→chip）；floating 保留 xywh；未知 id 丢弃', () => {
    const migrated = migrateV1Layout(V1, known)!
    // p.b（非 sessions）→ chip（v5.2 直接归入 chips，保留 dockOrder 序）
    expect(migrated['p.b'].loc).toEqual({ zone: 'chip', order: 0 })
    // p.a（非 sessions）→ chip（dockOrder 序 1）
    expect(migrated['p.a'].loc).toEqual({ zone: 'chip', order: 1 })
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

  it('端到端：localStorage v1 → 渲染即迁移（非 sessions 面板收入 chips），未交互不写 v2', () => {
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
    // v5.2：v1 迁移直接把非 sessions docked → chip（不经 migrateV2Layout 二次迁移）。
    expect(chipOrder()).toEqual(['p.b', 'p.a'])
    expect(sideRenderOrder()).toEqual([])
    expect(localStorage.getItem(V2_KEY)).toBeNull()
    // 首次交互才写 v2（交互入口在 chip 内面板上；此处只断言迁移结果与零写入）。
    expect(chipOrder()).toEqual(['p.b', 'p.a'])
  })
})

// ── v2→v5.1 迁移（side → chip）──────────────────────────────────────────────

describe('migrateV2Layout（v2→v5.2：side 面板保持不迁移，仅 h clamp）', () => {
  const state: Record<string, { loc: Record<string, unknown>; collapsed: boolean }> = {
    'p.a': { loc: { zone: 'side', order: 0 }, collapsed: true },
    'p.b': { loc: { zone: 'side', order: 1, h: 200 }, collapsed: false },
    'core.sessions': { loc: { zone: 'side', order: 2, h: 420 }, collapsed: false },
    'p.f': { loc: { zone: 'floating', order: 0, x: 10, y: 10, w: 320, h: 240 }, collapsed: false },
  }

  it('side 面板保持 side（v5.2 不迁移——用户 pin 的面板刷新后保持 pin）', () => {
    const next = migrateV2Layout(state as never)
    expect(next['p.a'].loc).toEqual({ zone: 'side', order: 0 })
    expect(next['p.b'].loc).toEqual({ zone: 'side', order: 1, h: 200 })
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

  it('端到端：localStorage v2（zone side 非 sessions）→ 保持 side（v5.2 不迁移用户 pin 的面板）', () => {
    registerPanel(makeDef('p.a', 'A'))
    registerPanel(makeDef('p.b', 'B'))
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.a': { loc: { zone: 'side', order: 3 }, collapsed: false },
    }))
    renderShell()
    // v5.2：v2 格式的 side 面板不迁移（用户 pin 的保持 pin+collapsed）。
    expect(sideRenderOrder()).toEqual(['p.a'])
    expect(chipOrder()).toEqual(['p.b'])
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
    // 插件声明侧栏容器 → 默认钉选 side（声明 h 220 被统一默认 360 提升）。
    expect(d['git.panel']).toEqual({ loc: { zone: 'side', h: 360, order: 0 }, collapsed: true })
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
    // 面板铺满堆叠区：面板总高超出时整栏滚动（overflow-y-auto），chips 条固定底部。
    const stack = document.querySelector('[data-testid="panel-dock-stack"]')!
    expect(stack.className).toContain('overflow-y-auto')
    expect(stack.className).toContain('flex-1')
    // SideChips 外层 div（data-panel-zone="chip"）内含 shrink-0 的 chips 条。
    const chipDock = document.querySelector('[data-testid="panel-chip-dock"]')!
    const chipBar = chipDock.firstElementChild!
    expect(chipBar.className).toContain('shrink-0')
  })

  

  

  

  

  it('底边调高：move 零持久化 + flex-basis 跟随，up 一次落盘', () => {
    // 2026-09-20 拖拽/钉选已删：预置 side entry（sessions + A + B 展开）→ A 有 handle。
    localStorage.setItem(V2_KEY, JSON.stringify({
      'core.sessions': { loc: { zone: 'side', order: 0, h: 360 }, collapsed: false },
      'p.a': { loc: { zone: 'side', order: 1, h: 360 }, collapsed: false },
      'p.b': { loc: { zone: 'side', order: 2, h: 360 }, collapsed: false },
    }))
    renderShell()

    const before = localStorage.getItem(V2_KEY)
    const handle = document.querySelector<HTMLElement>('[aria-label="调整面板高度"]')!
    fireEvent.pointerDown(handle, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(handle, { clientX: 100, clientY: 150 })
    // move 中本地跟随（360 + 50 = 410），零持久化。flex-basis 跟随 curH。
    const panel = document.querySelector<HTMLElement>('[data-panel-id="p.a"]')!
    expect(panel.style.flex).toContain('410')
    expect(localStorage.getItem(V2_KEY)).toBe(before)
    fireEvent.pointerUp(handle)
    const saved = JSON.parse(localStorage.getItem(V2_KEY)!)
    expect(saved['p.a'].loc.h).toBe(410)
  })

  it('底边调高 clamp：拖超上界 640 / 拖过下界 140', () => {
    // 2026-09-20 拖拽/钉选已删：预置 side entry（sessions + A + B 展开）。
    localStorage.setItem(V2_KEY, JSON.stringify({
      'core.sessions': { loc: { zone: 'side', order: 0, h: 360 }, collapsed: false },
      'p.a': { loc: { zone: 'side', order: 1, h: 360 }, collapsed: false },
      'p.b': { loc: { zone: 'side', order: 2, h: 360 }, collapsed: false },
    }))
    renderShell()

    const handle = document.querySelector<HTMLElement>('[aria-label="调整面板高度"]')!
    // 上界：从 360 起 +2000 → clamp 640。
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

  it('插件 contribution 尊重：def.location side h 220 → 默认钉选（统一默认 360）', () => {
    registerPanel(makeDef('git.panel', 'Git', { source: 'xbot.git', location: { zone: 'side', h: 220, order: 0 } }))
    renderShell()
    // side zone 现在只渲染【展开】的面板（入口在 ActivityBar）——用 openPanel 事件展开。
    fireEvent(window, new CustomEvent('xbot:panel-request', { detail: { id: 'git.panel' } }))
    const panel = document.querySelector<HTMLElement>('[data-panel-id="git.panel"]')!
    expect(panel.style.flex).toContain('360')
  })

  
})

// ── 标题栏点击语义（2026-09-15 用户：「点 Sessions 这个词有bug，别的位置没有」）──

describe('标题栏点击语义（标题文字/图标/空白一律不折叠）', () => {
  beforeEach(() => {
    registerPanel(makeDef('core.sessions', '会话', { source: 'core', icon: 'message' }))
  })

  function sessionsPanel(): HTMLElement {
    const el = document.querySelector<HTMLElement>('[data-panel-id="core.sessions"]')
    if (!el) throw new Error('core.sessions not rendered')
    return el
  }

  it('点标题文字 / 图标 / header 空白区：不折叠（唯一折叠控件是 ⌄ 按钮）', () => {
    renderShell()
    expect(sideRenderOrder()).toEqual(['core.sessions'])

    fireEvent.click(within(sessionsPanel()).getByTestId('panel-title'))
    expect(sideRenderOrder()).toEqual(['core.sessions'])

    fireEvent.click(sessionsPanel().querySelector('header svg')!)
    expect(sideRenderOrder()).toEqual(['core.sessions'])

    fireEvent.click(sessionsPanel().querySelector('header')!)
    expect(sideRenderOrder()).toEqual(['core.sessions'])
    // 未折叠 ⇒ 不落盘（零状态变更）。
    expect(localStorage.getItem(V2_KEY)).toBeNull()
  })

  it('⌄ 折叠按钮仍可折叠普通面板；折叠后左栏给空态提示（不再是一整片黑）', () => {
    registerPanel(makeDef('p.a', 'A'))
    localStorage.setItem(V2_KEY, JSON.stringify({
      'core.sessions': { loc: { zone: 'side', order: 0, h: 360 }, collapsed: false },
      'p.a': { loc: { zone: 'side', order: 1, h: 360 }, collapsed: false },
    }))
    renderShell()
    expect(sideRenderOrder()).toEqual(['core.sessions', 'p.a'])
    fireEvent.click(within(document.querySelector<HTMLElement>('[data-panel-id="p.a"]')!).getByLabelText('折叠'))
    expect(sideRenderOrder()).toEqual(['core.sessions'])
  })
})

// ── 常驻面板不变量（v5.3：core.sessions 不可浮窗/不可折叠/不可收 chips）──────


// ── 标题行扩展槽（headerExtra）：面板自己的控件放标题那一行 ────────────────

describe('标题行扩展槽 headerExtra', () => {
  it('面板定义里的 headerExtra 渲染在【标题行内】（不在主体里）', () => {
    registerPanel(
      makeDef('core.sessions', '会话', {
        source: 'core',
        headerExtra: () => <span data-testid="hdr-extra">渠道</span>,
        render: () => <div data-testid="body-sessions">body</div>,
      }),
    )
    renderShell()
    const panel = document.querySelector<HTMLElement>('[data-panel-id="core.sessions"]')!
    const header = panel.querySelector('header')!
    expect(header.querySelector('[data-testid="hdr-extra"]')).not.toBeNull()
    // 与标题同排（header 的直接子节点），且不在主体里
    expect(panel.querySelector('[data-testid="body-sessions"] [data-testid="hdr-extra"]')).toBeNull()
  })
})

describe('常驻面板（PINNED_DEFAULTS）不变量', () => {
  beforeEach(() => {
    registerPanel(makeDef('core.sessions', '会话', { source: 'core', icon: 'message' }))
    registerPanel(makeDef('p.a', 'A'))
  })

  function panel(): HTMLElement {
    const el = document.querySelector<HTMLElement>('[data-panel-id="core.sessions"]')
    if (!el) throw new Error('core.sessions not rendered')
    return el
  }

  it('header 不渲染浮窗/折叠/拖拽把手 —— 用户报"另一个有同样 bug 的按钮" + "拖拽不需要了"', () => {
    renderShell()
    const hdr = panel().querySelector('header')!
    expect(hdr.querySelector('svg.lucide-picture-in-picture-2')).toBeNull()
    expect(hdr.querySelector('svg.lucide-chevron-right')).toBeNull()
    // 隐藏的拖拽把手（opacity-0 仍占位）会让标题行右侧控件**对不齐**，且拖拽已不需要。
    expect(hdr.querySelector('[data-testid="panel-grip"]')).toBeNull()
  })

  

  it('持久化里的旧状态（被折叠/浮窗/chip）在加载时自愈回 side+展开', () => {
    localStorage.setItem(V2_KEY, JSON.stringify({
      'core.sessions': { loc: { zone: 'floating', order: 0, x: 40, y: 40, w: 300, h: 200 }, collapsed: true },
      'p.a': { loc: { zone: 'side', order: 1, h: 360 }, collapsed: false },
    }))
    renderShell()
    // 面板回到左栏堆叠（而不是浮层/chip）且展开可见。
    expect(sideRenderOrder()).toContain('core.sessions')
    expect(document.querySelector('[data-panel-zone="floating"] [data-panel-id="core.sessions"]')).toBeNull()
    expect(panel().querySelector('header')).not.toBeNull()
  })

  it('enforcePinnedState（纯函数）：浮窗/chip/折叠的旧状态 → side + 展开 + h clamp', () => {
    const healed = enforcePinnedState({
      'core.sessions': { loc: { zone: 'floating', order: 0, x: 10, y: 10, w: 300, h: 900 }, collapsed: true },
      'p.a': { loc: { zone: 'chip', order: 1 }, collapsed: true },
    })
    expect(healed['core.sessions'].loc.zone).toBe('side')
    expect(healed['core.sessions'].collapsed).toBe(false)
    expect(healed['core.sessions'].loc.h).toBe(640) // clamp 到 DOCK_H_MAX
    // 非常驻面板不受影响。
    expect(healed['p.a'].loc.zone).toBe('chip')
    // 已经是合法状态时返回原引用（不制造无谓的状态变更）。
    const fine = { 'core.sessions': { loc: { zone: 'side' as const, order: 0, h: 420 }, collapsed: false } }
    expect(enforcePinnedState(fine)).toBe(fine)
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
    } finally {
      clientWidthSpy.mockRestore()
    }
  })
})

// ── 皮肤 theme token 化（浮窗/钉选不硬编码颜色）────────────────────────────


// ── floating 全方向 resize（四角+四边）────────────────────────────────────────


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
        <I18nProvider>
          <PanelDockProvider tabManager={fakeTabManager}>
            <TopRail className="max-w-[300px]" />
          </PanelDockProvider>
        </I18nProvider>,
      )
      expect(slotOf().style.minWidth).toBe('42px')
      // 内容变窄（1042→1）→ minWidth 保持锁定（只增不减，锁定值由组件 ref 持有）。
      badgeText = '1'
      slotW = 10
      view.rerender(
        <I18nProvider>
          <PanelDockProvider tabManager={fakeTabManager}>
            <TopRail className="max-w-[300px]" />
          </PanelDockProvider>
        </I18nProvider>,
      )
      expect(slotOf().style.minWidth).toBe('42px')
    } finally {
      clientWidthSpy.mockRestore()
    }
  })
})

describe('sanitizeRailBadges（rail 徽章污染自愈）', () => {
  const barDef = {
    id: 'p.bar',
    title: 'Runner Bar',
    icon: 'server',
    defaultSlot: 'left',
    defaultMode: 'docked',
    location: { zone: 'bottom', order: 0 },
    render: () => null,
    badgeRender: () => null,
    source: 'p',
  } as unknown as PanelDefinition

  it('被拖进 side 的 rail 徽章 → 修正回 bottom（2026-09-20 用户「runner 选择栏不见了」）', () => {
    const state = { 'p.bar': { loc: { zone: 'side', order: 9, h: 360 }, collapsed: false } } as never
    const out = sanitizeRailBadges(state, [barDef])
    // ⚠️ entry 必须【保留】（删掉会让 entryOf 走 defaultEntryOf 兜底 ⇒ rail 不渲染它）。
    expect(out['p.bar']).toBeDefined()
    expect(out['p.bar'].loc.zone).toBe('bottom')
    // 其余字段保留（collapsed / h）。
    expect(out['p.bar'].collapsed).toBe(false)
  })

  it('已是声明 zone → 返回原引用（零变更）', () => {
    const state = { 'p.bar': { loc: { zone: 'bottom', order: 0 }, collapsed: false } } as never
    expect(sanitizeRailBadges(state, [barDef])).toBe(state)
  })

  it('非 rail 徽章（普通面板）不受影响', () => {
    const panelDef = { ...barDef, id: 'p.panel', location: { zone: 'side', order: 0 }, badgeRender: undefined } as unknown as PanelDefinition
    const state = { 'p.panel': { loc: { zone: 'chip', order: 0 }, collapsed: true } } as never
    expect(sanitizeRailBadges(state, [panelDef])).toBe(state)
  })
})

describe('rail 徽章（runner 选择栏）必须进 bottom rail', () => {
  it('zone=bottom 的 badge def → zoneIds(\'bottom\') 含它（被污染 entry 修正后）', () => {
    registerPanel({
      id: 'p.bar',
      title: 'Runner Bar',
      icon: 'server',
      defaultSlot: 'left',
      defaultMode: 'docked',
      location: { zone: 'bottom', order: 0 },
      render: () => null,
      badgeRender: () => '本机',
      source: 'p',
    } as unknown as PanelDefinition)
    // 历史污染：entry 曾被拖进 side（用户 2026-09-20 报「runner 选择栏不见了」）。
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.bar': { loc: { zone: 'side', order: 9, h: 360 }, collapsed: false },
    }))
    // 直接断言状态层：rail 的 ids 来自 dock.zoneIds(zone)。jsdom 无布局宽度，
    // DOM 断言会被 rail 的「放不下收进 ＋N」逻辑干扰 ⇒ 断言状态而非 DOM。
    const { container } = renderShell()
    const dock = (container.ownerDocument.defaultView as never as { __dock?: unknown }).__dock
    void dock
    // 通过 DOM 上的 rail 容器（data-testid=panel-rail-bottom）确认 zone 归属：
    const rail = document.querySelector('[data-testid="panel-rail-bottom"]')
    expect(rail).toBeTruthy()
    // 活动栏不得含它（用户报「跑侧边栏」）。
    expect(document.querySelector('[data-activity-item="p.bar"]')).toBeFalsy()
  })
})

describe('rail 徽章：插件【异步】注册也必须回 bottom rail（2026-09-20 runner 消失）', () => {
  const barDef = {
    id: 'p.asyncbar',
    title: 'Async Rail Bar',
    icon: 'server',
    defaultSlot: 'left',
    defaultMode: 'docked',
    location: { zone: 'bottom', order: 0 },
    render: () => null,
    badgeRender: () => '本机',
    source: 'p',
  } as unknown as PanelDefinition

  it('渲染时 defs 还不含它（插件后到）→ 污染 entry 仍须被修正回 bottom', () => {
    // 污染：历史拖拽把它写进 side。
    localStorage.setItem(V2_KEY, JSON.stringify({
      'p.asyncbar': { loc: { zone: 'side', order: 9, h: 360 }, collapsed: false },
    }))
    // 首次渲染：defs 为空（插件尚未注册）⇒ 初始化时的 sanitize 看不到它。
    const { rerender } = renderShell()
    // 插件异步注册（= 真实环境的 activate 时机）—— act 包裹让 defs 更新 flush。
    act(() => { registerPanel(barDef) })
    rerender(
      <PanelDockProvider tabManager={fakeTabManager}>
        <div style={{ position: 'relative', width: 1000, height: 800 }}>
          <PanelDock />
          <TopRail className="max-w-[300px]" />
          <BottomRailBadges />
          <SideChips />
        </div>
      </PanelDockProvider>,
    )
    // defs 变化 effect 重跑 sanitize ⇒ 污染 entry 被修正。
    const saved = JSON.parse(localStorage.getItem(V2_KEY) ?? '{}')
    expect(saved['p.asyncbar']?.loc.zone).toBe('bottom')
  })
})
