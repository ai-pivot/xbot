/**
 * pluginViewRequest 单测 —— 宿主可触发的「打开任意插件视图」入口。
 *
 * 为什么值得钉（2026-10-13 真缺陷的回归防线）：web 侧插件 view 没有静态 URL，
 * 原生壳曾经只能落到**通用根页**（用户原话「插件都打不开」）。本模块是唯一入口，
 * 三条不变量任一破掉，宿主就再也打不开插件视图：
 *   ① URL 深链必须解析出 viewId（含 key/params/title），坏 JSON **不得**让视图打不开；
 *   ② 打开器未注册时请求必须**排队**（宿主挂载前先到的请求不能丢）；
 *   ③ 就绪信号到达后 `flush` 必须**恰好兑现一次**，且由**外部**（宿主）触发。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  flushPluginViewRequest,
  parsePluginViewQuery,
  requestOpenPluginView,
  takePendingPluginView,
} from './pluginViewRequest'
import { hasEditorTabOpener, registerEditorTabOpener } from './editorTabs'

type OpenerInput = { type: string; title: string; data?: Record<string, unknown> }

describe('parsePluginViewQuery（URL 深链）', () => {
  it('无参数 ⇒ null', () => {
    expect(parsePluginViewQuery('')).toBeNull()
    expect(parsePluginViewQuery('?foo=1')).toBeNull()
    expect(parsePluginViewQuery('?xbot_view=')).toBeNull()
  })

  it('viewId + 默认 title=viewId', () => {
    expect(parsePluginViewQuery('?xbot_view=xbot.git-fancy.panel')).toEqual({
      viewId: 'xbot.git-fancy.panel',
      title: 'xbot.git-fancy.panel',
      key: undefined,
      params: undefined,
    })
  })

  it('key / params(JSON) / title 全部解析', () => {
    const q = '?xbot_view=v.panel&xbot_view_key=k1&xbot_view_title=Git&' +
      'xbot_view_params=' + encodeURIComponent('{"path":"a.ts","commit":"abc"}')
    expect(parsePluginViewQuery(q)).toEqual({
      viewId: 'v.panel',
      title: 'Git',
      key: 'k1',
      params: { path: 'a.ts', commit: 'abc' },
    })
  })

  it('params 是坏 JSON ⇒ 仍打开（params 缺省），⛔ 不能因为一个坏参数就打不开', () => {
    const r = parsePluginViewQuery('?xbot_view=v.panel&xbot_view_params=%7Bnot-json')
    expect(r?.viewId).toBe('v.panel')
    expect(r?.params).toBeUndefined()
  })
})

describe('request/flush（排队 → 兑现）', () => {
  beforeEach(() => {
    registerEditorTabOpener(null)
    takePendingPluginView()
  })

  it('打开器未注册 ⇒ 排队（不丢请求），flush 返回 false', () => {
    requestOpenPluginView({ viewId: 'v.panel', title: 'V' })
    expect(flushPluginViewRequest()).toBe(false)
    expect(takePendingPluginView()?.viewId).toBe('v.panel')
  })

  it('注册打开器后 flush ⇒ 恰好兑现一次并清空待办', () => {
    const calls: OpenerInput[] = []
    registerEditorTabOpener((input) => {
      calls.push(input as unknown as OpenerInput)
      return 'tab-1'
    })
    requestOpenPluginView({ viewId: 'v.panel', title: 'V', key: 'k', params: { a: 1 } })
    expect(flushPluginViewRequest()).toBe(true)
    expect(calls).toHaveLength(1)
    expect(calls[0].type).toBe('plugin')
    expect(calls[0].data).toMatchObject({ viewId: 'v.panel', viewKey: 'k', viewParams: { a: 1 } })
    expect(flushPluginViewRequest()).toBe(false) // 幂等：没有待办了
    expect(calls).toHaveLength(1)
  })

  it('后到覆盖先到（宿主语义 = 切到某个视图，不排队多个）', () => {
    registerEditorTabOpener(() => 'tab')
    requestOpenPluginView({ viewId: 'a', title: 'A' })
    requestOpenPluginView({ viewId: 'b', title: 'B' })
    expect(takePendingPluginView()?.viewId).toBe('b')
    expect(hasEditorTabOpener()).toBe(true)
  })

  it('注销打开器后 ⇒ 回到排队（宿主卸载不丢请求）', () => {
    registerEditorTabOpener(() => 'tab')
    registerEditorTabOpener(null)
    requestOpenPluginView({ viewId: 'v', title: 'V' })
    expect(flushPluginViewRequest()).toBe(false)
    expect(takePendingPluginView()?.viewId).toBe('v')
  })
})

describe('订阅（宿主挂载后立刻尝试兑现）', () => {
  beforeEach(() => {
    registerEditorTabOpener(null)
    takePendingPluginView()
  })

  it('新请求到达 ⇒ 通知订阅者（带该请求）', async () => {
    const { onPluginViewRequest } = await import('./pluginViewRequest')
    const seen: string[] = []
    const off = onPluginViewRequest((o) => seen.push(o.viewId))
    requestOpenPluginView({ viewId: 'v1', title: 'V1' })
    requestOpenPluginView({ viewId: 'v2', title: 'V2' })
    expect(seen).toEqual(['v1', 'v2'])
    off()
    requestOpenPluginView({ viewId: 'v3', title: 'V3' })
    expect(seen).toEqual(['v1', 'v2']) // 退订后不再收到
  })
})
