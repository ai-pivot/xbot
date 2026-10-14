import { describe, it, expect } from 'vitest'
import {
  PANEL_DEEP_LINK_IDS,
  PANEL_QUERY_KEY,
  mobileViewForPanel,
  parseEmbedQuery,
  parseEmbedTarget,
  parsePanelQuery,
} from './panelRequest'

/**
 * panelRequest —— 宿主「直达内置面板」深链的**判别力**回归。
 *
 * 为什么值得测：这条 URL 是**原生壳唯一的入口**（原生不再为文件/终端/任务各写一套实现）；
 * 判错的后果都是用户可见的：① 坏 id 被当成合法 ⇒ 渲染空壳（"点了没反应"）；
 * ② 5 个 id 有任何一个不在白名单 ⇒ 那个能力在原生侧**彻底打不开**（用户报的
 * "文件管理也没有，终端和任务管理做的很差" 正是这一类）。
 */
describe('parsePanelQuery', () => {
  it('五个内置面板 id 全部可直达（白名单与 MobileAppShell 同源）', () => {
    for (const id of PANEL_DEEP_LINK_IDS) {
      expect(parsePanelQuery(`?${PANEL_QUERY_KEY}=${id}`)).toBe(id)
    }
    expect(PANEL_DEEP_LINK_IDS).toEqual(['files', 'search', 'info', 'tasks', 'terminal'])
  })

  it('与其他参数共存时仍能解析（宿主可能拼上主题/会话等）', () => {
    expect(parsePanelQuery('?theme=dark&xbot_panel=terminal&session=web%3Ac1')).toBe('terminal')
  })

  it('无参数 / 空串 / 只有一个字符 ⇒ null', () => {
    expect(parsePanelQuery('')).toBeNull()
    expect(parsePanelQuery('?')).toBeNull()
    expect(parsePanelQuery('?x=1')).toBeNull()
  })

  it('⛔ 未知 id ⇒ null（fail-closed：不把任意串塞进 panelRegistry 渲染空壳）', () => {
    expect(parsePanelQuery('?xbot_panel=nope')).toBeNull()
    expect(parsePanelQuery('?xbot_panel=')).toBeNull()
    // 大小写敏感（面板 id 是小写；'FILES' 不是合法 id）
    expect(parsePanelQuery('?xbot_panel=FILES')).toBeNull()
    // 插件视图 id 不是面板 id（两条协议互不替代）
    expect(parsePanelQuery('?xbot_panel=xbot.git-fancy.panel')).toBeNull()
  })

  it('超长/怪输入不抛（宿主可能把整条 URL 塞进来）', () => {
    expect(() => parsePanelQuery('?xbot_panel=' + 'a'.repeat(5000))).not.toThrow()
    expect(parsePanelQuery('?xbot_panel=' + 'a'.repeat(5000))).toBeNull()
  })

  it('面板 ⇒ 移动端外壳视图固定为 detail（终端列表也是 detail，不是终端会话视图）', () => {
    for (const id of PANEL_DEEP_LINK_IDS) {
      expect(mobileViewForPanel(id)).toBe('detail')
    }
  })

  it('⛔ 非法 id ⇒ null（不猜视图，宁可留在默认视图也不渲染空壳）', () => {
    expect(mobileViewForPanel('nope')).toBeNull()
    expect(mobileViewForPanel('')).toBeNull()
  })
})

/**
 * parseEmbedQuery —— 宿主**嵌入模式**开关（`?xbot_embed=1`）。
 *
 * 为什么值得测：它决定原生壳里要不要渲染 web 自己的 chrome（用户原话「去掉多余的 ui」
 * 「不要做多余的渲染」）。判错两个方向的代价都不对称：
 *   · 假阴（该 embed 却判否）⇒ 真机又出现"两层 chrome"套娃（用户已报的观感问题）；
 *   · 假阳（不该 embed 却判真）⇒ 浏览器直接访问 `/` 时只剩一个面板，**整个应用"消失"**
 *     （比套娃严重得多）⇒ 所以只认字面量 `'1'`，`0`/`false`/空/任意值一律否（fail-safe）。
 */
describe('parseEmbedQuery', () => {
  it("只认字面量 '1'（与 xbot_panel 配对使用）", () => {
    expect(parseEmbedQuery('?xbot_panel=files&xbot_embed=1')).toBe(true)
    expect(parseEmbedQuery('?xbot_embed=1')).toBe(true)
  })

  it("⛔ '0'/空/任意值/false 一律否（否则浏览器访问 `/` 会只剩一个面板）", () => {
    expect(parseEmbedQuery('?xbot_panel=files&xbot_embed=0')).toBe(false)
    expect(parseEmbedQuery('?xbot_embed=')).toBe(false)
    expect(parseEmbedQuery('?xbot_embed=false')).toBe(false)
    expect(parseEmbedQuery('?xbot_embed=true')).toBe(false)
    expect(parseEmbedQuery('?xbot_embed')).toBe(false)
  })

  it('无查询串 / 空串 / 单字符 / 怪输入 ⇒ 否且不抛', () => {
    expect(parseEmbedQuery('')).toBe(false)
    expect(parseEmbedQuery('?')).toBe(false)
    expect(() => parseEmbedQuery('?xbot_embed=' + 'a'.repeat(5000))).not.toThrow()
    expect(parseEmbedQuery('?xbot_embed=' + 'a'.repeat(5000))).toBe(false)
  })
})

/**
 * parseEmbedTarget —— 嵌入态要挂**什么**（内置面板 **或** 插件视图）。
 *
 * 为什么值得测（真机实证 2026-10-15）：此前嵌入判定是 `parseEmbedQuery ? parsePanelQuery : null`
 * ⇒ `?xbot_view=<viewId>&xbot_embed=1`（原生「能力面板」点插件时发的深链，**没有** `xbot_panel`）
 * 落到 `null` ⇒ 渲染 `<AppShell />`（整个 SPA）⇒ 原生 `‹ 插件 ⟳` 之下又出现 web 自己的
 * `← 插件 ＋ 🔧 ⚙️` 头 + 底部 `本机 ●` 栏 = 用户看到的"套娃"，且"为一个视图渲了整个 app"。
 */
describe('parseEmbedTarget', () => {
  it('xbot_panel（白名单内）⇒ panel 目标', () => {
    expect(parseEmbedTarget('?xbot_panel=tasks&xbot_embed=1')).toEqual({
      kind: 'panel',
      panelId: 'tasks',
    })
  })

  it('⭐ xbot_view（无 xbot_panel）+ xbot_embed=1 ⇒ view 目标（本轮修复的核心判据）', () => {
    expect(
      parseEmbedTarget('?xbot_view=xbot.plugin-manager.panel&xbot_view_title=%E6%8F%92%E4%BB%B6&xbot_embed=1'),
    ).toEqual({ kind: 'view', viewId: 'xbot.plugin-manager.panel' })
  })

  it('两条都在 ⇒ panel 优先（内置面板 id 是白名单，语义更窄）', () => {
    expect(parseEmbedTarget('?xbot_panel=files&xbot_view=x.y.panel&xbot_embed=1')).toEqual({
      kind: 'panel',
      panelId: 'files',
    })
  })

  it('⛔ 非嵌入态（无 xbot_embed=1）⇒ null，即使带 xbot_view（浏览器访问 `/` 必须仍是完整应用）', () => {
    expect(parseEmbedTarget('?xbot_view=x.y.panel')).toBeNull()
    expect(parseEmbedTarget('?xbot_panel=files')).toBeNull()
  })

  it('⛔ 嵌入态但两条都不可用 ⇒ null（fail-safe：回落完整应用，不给宿主空白壳）', () => {
    expect(parseEmbedTarget('?xbot_embed=1')).toBeNull()
    expect(parseEmbedTarget('?xbot_embed=1&xbot_panel=nope')).toBeNull()
    expect(parseEmbedTarget('?xbot_embed=1&xbot_view=')).toBeNull()
  })

  it('怪输入不抛', () => {
    expect(() => parseEmbedTarget('?xbot_embed=1&xbot_view=' + 'a'.repeat(5000))).not.toThrow()
    expect(() => parseEmbedTarget('?xbot_embed=' + 'a'.repeat(5000))).not.toThrow()
  })
})
