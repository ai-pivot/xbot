import { describe, it, expect } from 'vitest'
import {
  PANEL_DEEP_LINK_IDS,
  PANEL_QUERY_KEY,
  mobileViewForPanel,
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
