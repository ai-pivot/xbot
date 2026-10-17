import { describe, it, expect, afterEach } from 'vitest'
import { hasNativeHost, isEmbedMode, postHostMessage, postHostNav } from './hostBridge'

/**
 * hostBridge —— 宿主桥的**判别力**回归。
 *
 * 为什么值得测：这条桥的失败方向都是"用户操作静默无效"（本仓最恨的形态）——
 *   ① 消息发不出去 / 格式与原生对不上 ⇒ 原生壳永远收不到"面板已就绪"，标题不改、
 *      原生 UI 不被触发，而 web 侧一切正常（无报错）⇒ 只能靠真机才发现；
 *   ② 桥不存在时**必须**静默（纯浏览器打开同一份产物）：抛异常 = 面板白屏。
 */
afterEach(() => {
  delete (window as unknown as { xbotNative?: unknown }).xbotNative
})

describe('postHostMessage', () => {
  it('有宿主 ⇒ 把 {v,id,type,payload} 序列化后交给宿主 postMessage', () => {
    const sent: string[] = []
    ;(window as unknown as { xbotNative: unknown }).xbotNative = {
      postMessage: (s: string) => {
        sent.push(s)
        return ''
      },
    }
    expect(hasNativeHost()).toBe(true)
    expect(postHostMessage('nav', { kind: 'ready', text: 'files' })).toBe(true)
    expect(sent).toHaveLength(1)
    expect(JSON.parse(sent[0])).toEqual({
      v: 1,
      id: 'nav',
      type: 'nav',
      payload: { kind: 'ready', text: 'files' },
    })
  })

  it('无宿主（纯浏览器）⇒ 返回 false 且不抛', () => {
    expect(hasNativeHost()).toBe(false)
    expect(postHostMessage('nav', { kind: 'ready', text: 'files' })).toBe(false)
  })

  it('宿主 postMessage 抛错 ⇒ 吞掉并返回 false（⛔ 桥坏不能把面板打崩）', () => {
    ;(window as unknown as { xbotNative: unknown }).xbotNative = {
      postMessage: () => {
        throw new Error('bridge not ready')
      },
    }
    expect(hasNativeHost()).toBe(true)
    expect(() => postHostMessage('nav', { kind: 'title', text: 'x' })).not.toThrow()
    expect(postHostMessage('nav', { kind: 'title', text: 'x' })).toBe(false)
  })

  it('宿主存在但没有 postMessage（注入半途失败）⇒ 视为无宿主', () => {
    ;(window as unknown as { xbotNative: unknown }).xbotNative = {}
    expect(hasNativeHost()).toBe(false)
    expect(postHostMessage('nav', { kind: 'ready', text: 'files' })).toBe(false)
  })
})

describe('postHostNav', () => {
  it('固定走 type=nav（原生侧 webbridge 的白名单据此分派）', () => {
    const seen: Array<Record<string, unknown>> = []
    ;(window as unknown as { xbotNative: unknown }).xbotNative = {
      postMessage: (s: string) => {
        seen.push(JSON.parse(s) as Record<string, unknown>)
        return ''
      },
    }
    postHostNav({ kind: 'openFile', text: '/tmp/a.ts' })
    expect(seen[0]).toEqual({ v: 1, id: 'nav', type: 'nav', payload: { kind: 'openFile', text: '/tmp/a.ts' } })
  })
})

describe('isEmbedMode', () => {
  it('读 window.location.search 的判定与 parseEmbedQuery 同源', () => {
    window.history.replaceState({}, '', '/?xbot_panel=files&xbot_embed=1')
    expect(isEmbedMode()).toBe(true)
    window.history.replaceState({}, '', '/?xbot_panel=files')
    expect(isEmbedMode()).toBe(false)
    window.history.replaceState({}, '', '/')
    expect(isEmbedMode()).toBe(false)
  })
})
