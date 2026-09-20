/**
 * viewRegistrySync —— view 贡献点 → 登记表（panelRegistry / layoutRegistry）同步。
 *
 * 回归背景（2026-09-19 用户实测：「改了语言插件没动态变化」）：
 * 面板/tab 标题是「**按当前语言解析后**的文本」被登记进登记表（view.title 允许是
 * 插件清单 `web.i18n` 里的 key），而同步只在 mount + view 集合变化时跑 ⇒ 宿主切语言后
 * 侧栏/rail/tab 标题全部定格在旧语言。
 *
 * 本文件守护：
 *  ① 宿主语言变化 ⇒ panelRegistry + layoutRegistry 两处标题都按新语言重算（修复前必红）；
 *  ② 重算是**幂等**的，且**不重置用户布局**（moveItem 的归属/顺序保留，只换文本）；
 *  ③ view 卸载 / 同步器 dispose 时登记项被清理（不留幽灵 tab）；
 *  ④ 内置清单（无 web.i18n 表）的标题原样透传（其文本由 manifest getter 惰性给出）。
 */
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import i18n, { changeLocale } from '@/i18n'
import type { ViewContribution } from '@/plugin-api'

import { layoutRegistry } from './layoutRegistry'
import { panelRegistry } from './panelRegistry'
import { usePluginViewRegistrySync, type ViewSourceRuntime } from './viewRegistrySync'

// ── fixture：一个「标题是清单 i18n key」的第三方插件 + 一个内置插件 ──────────
const viewsFixture: Array<{ pluginId: string; view: ViewContribution }> = []
const manifests: Record<string, { i18n?: Record<string, Record<string, string>> }> = {}
// 稳定引用：hook 的 useEffect deps 含 runtime，每次渲染新对象会反复重挂载。
const runtime: ViewSourceRuntime = {
  listAllViews: () => viewsFixture,
  registry: { manifestOf: (id: string) => manifests[id] },
  subscribeViews: () => () => {},
}

function view(id: string, title: string, container: ViewContribution['container'] = 'right_sidebar'): ViewContribution {
  return { kind: 'view', id, container, title, icon: 'blocks' }
}

function panelTitle(id: string): string | undefined {
  return panelRegistry.getPanel(id)?.title
}

function layoutTitle(id: string): string | undefined {
  return layoutRegistry.itemsFor('desktop.activity_bar').find((i) => i.id === id)?.title
}

beforeEach(async () => {
  viewsFixture.splice(0, viewsFixture.length)
  for (const k of Object.keys(manifests)) delete manifests[k]
  for (const p of panelRegistry.listPanels()) panelRegistry.unregisterPanel(p.id)
  layoutRegistry.resetAll()
  // 语言显式固定在 zh-CN（jsdom 的 navigator.language 是 en-US ⇒ 初始语言不确定）。
  await i18n.changeLanguage('zh-CN')
  manifests['xbot.git-fancy'] = {
    i18n: {
      'zh-CN': { 'view.panel.title': 'Git 面板' },
      en: { 'view.panel.title': 'Git panel' },
      ja: { 'view.panel.title': 'Git パネル' },
    },
  }
})

afterEach(async () => {
  vi.restoreAllMocks()
  await i18n.changeLanguage('zh-CN')
})

describe('usePluginViewRegistrySync：语言变化 ⇒ 标题重算', () => {
  it('宿主切语言后，panelRegistry 与 layoutRegistry 的标题都按新语言刷新（无需刷新页面）', async () => {
    viewsFixture.push({ pluginId: 'xbot.git-fancy', view: view('xbot.git-fancy.panel', 'view.panel.title') })

    renderHook(() => usePluginViewRegistrySync(runtime))

    // 初始（zh-CN）：清单 key 被解析成中文
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git 面板')
    expect(layoutTitle('xbot.git-fancy.panel')).toBe('Git 面板')

    // 宿主切语言（真实入口 changeLocale —— 设置面板走的就是它）
    await act(async () => {
      changeLocale('en')
      await i18n.changeLanguage('en')
    })

    // 修复前：两处仍是 'Git 面板'（同步只在 mount 跑）⇒ 本断言必红
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git panel')
    expect(layoutTitle('xbot.git-fancy.panel')).toBe('Git panel')

    await act(async () => {
      changeLocale('ja')
      await i18n.changeLanguage('ja')
    })
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git パネル')
    expect(layoutTitle('xbot.git-fancy.panel')).toBe('Git パネル')
  })

  it('重算是幂等的：不重复登记、也不重置用户的布局归属与顺序（只换文本）', async () => {
    viewsFixture.push({ pluginId: 'xbot.git-fancy', view: view('xbot.git-fancy.panel', 'view.panel.title') })
    const { result } = renderHook(() => usePluginViewRegistrySync(runtime))

    // 用户把该插件面板拖到「桌面侧栏」并排在首位（布局意图存在 layoutRegistry 内部状态）
    act(() => {
      layoutRegistry.moveItemTo('xbot.git-fancy.panel', 'desktop.sidebar', { beforeId: null })
    })
    expect(layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id)).toContain('xbot.git-fancy.panel')
    const orderBefore = layoutRegistry.getSlotOrder('desktop.sidebar')
    const panelsBefore = panelRegistry.listPanels().length

    await act(async () => {
      await i18n.changeLanguage('en')
    })

    // 位置/顺序/登记数量不变，仅文本更新
    expect(layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id)).toContain('xbot.git-fancy.panel')
    expect(layoutRegistry.itemsFor('desktop.activity_bar').map((i) => i.id)).not.toContain('xbot.git-fancy.panel')
    expect(layoutRegistry.getSlotOrder('desktop.sidebar')).toEqual(orderBefore)
    expect(panelRegistry.listPanels().length).toBe(panelsBefore)
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git panel')
    expect(layoutTitle('xbot.git-fancy.panel')).toBeUndefined() // 已不在 activity_bar
    // 不产生重复项
    expect(layoutRegistry.allItems().filter((i) => i.id === 'xbot.git-fancy.panel')).toHaveLength(1)
    void result
  })

  it('内置清单（无 web.i18n 表）标题原样透传（文本由 manifest getter 惰性给出）', async () => {
    viewsFixture.push({ pluginId: 'xbot.plugin-manager', view: view('xbot.plugin-manager.panel', '插件') })
    renderHook(() => usePluginViewRegistrySync(runtime))
    expect(panelTitle('xbot.plugin-manager.panel')).toBe('插件')

    await act(async () => {
      await i18n.changeLanguage('en')
    })
    // 没有表 ⇒ 同步器不翻译；题目（宿主 i18n）由 manifest getter 在**读取时**给新语言
    // —— 见 src/plugins/manager/builtinManifestI18n.test.ts 的守护。
    expect(panelTitle('xbot.plugin-manager.panel')).toBe('插件')
  })

  it('view 消失 / dispose 时登记项被清理（不留幽灵面板）', async () => {
    viewsFixture.push({ pluginId: 'xbot.git-fancy', view: view('xbot.git-fancy.panel', 'view.panel.title') })
    const { unmount } = renderHook(() => usePluginViewRegistrySync(runtime))
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git 面板')

    // 插件被卸载（view 集合变化 → subscribeViews 回调；此处直接改 fixture 后手动触发）
    viewsFixture.splice(0, viewsFixture.length)
    await act(async () => {
      await i18n.changeLanguage('en') // 语言变化 = 重算触发器之一
    })
    expect(panelTitle('xbot.git-fancy.panel')).toBeUndefined()
    expect(layoutTitle('xbot.git-fancy.panel')).toBeUndefined()

    // 重新挂载一个 view 后 unmount 同步器：登记项全部清理
    viewsFixture.push({ pluginId: 'xbot.git-fancy', view: view('xbot.git-fancy.panel', 'view.panel.title') })
    const second = renderHook(() => usePluginViewRegistrySync(runtime))
    await act(async () => {
      await i18n.changeLanguage('zh-CN')
    })
    expect(panelTitle('xbot.git-fancy.panel')).toBe('Git 面板')
    unmount()
    second.unmount()
    expect(panelTitle('xbot.git-fancy.panel')).toBeUndefined()
    expect(layoutTitle('xbot.git-fancy.panel')).toBeUndefined()
  })
})
