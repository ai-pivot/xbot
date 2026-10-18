/**
 * sessionIdentity —— 「插件 RPC 注入 cwd 的身份该由谁定」的判别力测试。
 *
 * 用户真机 P0（2026-10-17）：原生里点 **web** 版 git 插件 ⇒ 面板显示「当前项目不是 git 目录」，
 * 而**原生** git 面板正确。根因：`useSessionStore` 启动时按缓存的会话树自动选中 `activeSession`
 * （`isCurrent` 或第一个非 synthetic），`usePluginRuntimeHost` 无条件写回
 * `window.__xbot_session__` ⇒ **覆盖宿主注入的身份** ⇒ 插件 RPC 带错会话 ⇒ 后端按那个会话注入
 * cwd（非 repo）。
 *
 * 判据（mutation 自证过）：
 *  · 把 `resolveSessionIdentity` 的宿主优先分支改成「active 优先」⇒ 本文件必红（case 1/2）；
 *  · 把接线里的 `embedded: parseEmbedQuery(...)` 改成 `false` ⇒ 最后一条源码形态断言必红。
 */
import { describe, expect, it } from 'vitest'
import hostSrc from './usePluginRuntimeHost.ts?raw'
import storeSrc from '@/hooks/useSessionStore.ts?raw'
import { initialActiveSession, readHostIdentity, resolveSessionIdentity } from './sessionIdentity'

const HOST = { channel: 'web', chatID: 'chat_HOST' }
const ACTIVE = { channel: 'web', chatID: 'chat_CACHED' }

describe('resolveSessionIdentity', () => {
  it('① 嵌入 + 宿主已注入 ⇒ 宿主权威（web 不得覆盖）', () => {
    const r = resolveSessionIdentity({ hostIdentity: HOST, active: ACTIVE, embedded: true })
    expect(r.hostOwns).toBe(true)
    expect(r.identity).toEqual({ channel: 'web', chatID: 'chat_HOST' })
  })

  it('② 嵌入 + 宿主已注入但 web 没有 activeSession ⇒ 仍用宿主身份（不能因此丢身份）', () => {
    const r = resolveSessionIdentity({ hostIdentity: HOST, active: null, embedded: true })
    expect(r.hostOwns).toBe(true)
    expect(r.identity?.chatID).toBe('chat_HOST')
  })

  it('③ 嵌入 + 宿主没注入 ⇒ web 自己管（浏览器直开/旧宿主兼容）', () => {
    const r = resolveSessionIdentity({ hostIdentity: null, active: ACTIVE, embedded: true })
    expect(r.hostOwns).toBe(false)
    expect(r.identity?.chatID).toBe('chat_CACHED')
  })

  it('④ 嵌入 + 宿主注入为空 chatID ⇒ 视为未注入（不用空身份发 RPC）', () => {
    const r = resolveSessionIdentity({
      hostIdentity: { channel: 'web', chatID: '' },
      active: ACTIVE,
      embedded: true,
    })
    expect(r.hostOwns).toBe(false)
    expect(r.identity?.chatID).toBe('chat_CACHED')
  })

  it('⑤ 非嵌入（浏览器直开）+ 页面里残留 __xbot_session__ ⇒ 行为不变：web 自己管', () => {
    const r = resolveSessionIdentity({ hostIdentity: HOST, active: ACTIVE, embedded: false })
    expect(r.hostOwns).toBe(false)
    expect(r.identity?.chatID).toBe('chat_CACHED')
  })

  it('⑥ 两条来源都没有 ⇒ null（不发射事件、不写全局）', () => {
    const r = resolveSessionIdentity({ hostIdentity: null, active: null, embedded: true })
    expect(r.identity).toBeNull()
    expect(r.hostOwns).toBe(false)
  })

  it('⑦ channel 缺失 ⇒ 回落 web', () => {
    const r = resolveSessionIdentity({
      hostIdentity: { channel: '', chatID: 'chat_HOST' },
      active: null,
      embedded: true,
    })
    expect(r.identity).toEqual({ channel: 'web', chatID: 'chat_HOST' })
  })

  it('⑧ 接线：usePluginRuntimeHost 必须把 parseEmbedQuery 的结果交给判定（防静默退化成 active 优先）', () => {
    expect(hostSrc).toContain('resolveSessionIdentity({')
    expect(hostSrc).toContain('embedded: parseEmbedQuery(window.location.search)')
    // 宿主权威时**不得**覆盖全局
    expect(hostSrc).toContain('if (!verdict.hostOwns) {')
    expect(hostSrc).toContain("w.__xbot_session__ = { channel: effChannel ?? 'web', chatID: effChatID }")
  })
})

/**
 * P0（2026-10-18 审查）：**`activeSession` 必须与插件 RPC 同源**。
 *
 * 症状（未修时）：嵌入态 `EmbedPanelShell` 的 SSE 订阅与所有面板的 REST 快照都取自
 * `session.activeSession`，而它是"缓存会话树自动选中"的 —— 与宿主 `__xbot_session__`
 * 可以不同 ⇒ **同一页面两种会话身份**：插件 RPC 用宿主会话（正确），任务/终端/文件面板
 * 显示另一个会话的数据（错误）。
 *
 * 判据（mutation 自证）：
 *  · `initialActiveSession` 里丢掉宿主优先（直接 `return input.cached`）⇒ ①/② 必红；
 *  · `useSessionStore` 的初始化改回"只按缓存树"⇒ ⑨ 源码形态断言必红；
 *  · 对账（refresh）里丢掉宿主复查 ⇒ ⑩ 必红。
 */
describe('initialActiveSession —— 嵌入态 activeSession 用宿主身份', () => {
  it('① 嵌入 + 宿主已注入 + 缓存树指向另一会话 ⇒ **宿主**（与 RPC 同源）', () => {
    const r = initialActiveSession({ cached: ACTIVE, hostIdentity: HOST, embedded: true })
    expect(r).toEqual({ channel: 'web', chatID: 'chat_HOST' })
  })

  it('② 嵌入 + 宿主已注入 + 缓存树为空 ⇒ 宿主（不能因为没有缓存就丢身份）', () => {
    const r = initialActiveSession({ cached: null, hostIdentity: HOST, embedded: true })
    expect(r).toEqual({ channel: 'web', chatID: 'chat_HOST' })
  })

  it('③ 非嵌入 + 页面残留 __xbot_session__ ⇒ 缓存树（浏览器直开行为逐字节不变）', () => {
    const r = initialActiveSession({ cached: ACTIVE, hostIdentity: HOST, embedded: false })
    expect(r).toEqual({ channel: 'web', chatID: 'chat_CACHED' })
  })

  it('④ 嵌入 + 宿主没注入 ⇒ 缓存树（旧宿主兼容）', () => {
    const r = initialActiveSession({ cached: ACTIVE, hostIdentity: null, embedded: true })
    expect(r).toEqual({ channel: 'web', chatID: 'chat_CACHED' })
  })

  it('⑤ 两条来源都没有 ⇒ null', () => {
    expect(initialActiveSession({ cached: null, hostIdentity: null, embedded: true })).toBeNull()
  })

  it('⑥ readHostIdentity：读全局 / 空 chatID 视为未注入 / 缺失不抛', () => {
    const w = window as unknown as { __xbot_session__?: { channel?: string; chatID?: string } }
    delete w.__xbot_session__
    expect(readHostIdentity()).toBeNull()
    w.__xbot_session__ = { channel: 'cli', chatID: 'chat_HOST' }
    expect(readHostIdentity()).toEqual({ channel: 'cli', chatID: 'chat_HOST' })
    w.__xbot_session__ = { channel: 'cli', chatID: '' }
    expect(readHostIdentity()).toBeNull()
    w.__xbot_session__ = { chatID: 'chat_HOST' }
    expect(readHostIdentity()).toEqual({ channel: 'web', chatID: 'chat_HOST' }) // channel 缺失 ⇒ web
    delete w.__xbot_session__
  })

  it('⑨ 接线：useSessionStore 的 activeSession 初值必须走 initialActiveSession(宿主身份)', () => {
    expect(storeSrc).toContain('initialActiveSession({')
    expect(storeSrc).toContain('hostIdentity: readHostIdentity()')
    expect(storeSrc).toContain('embedded: isEmbeddedHostPage()')
  })

  it('⑩ 接线：会话树对账必须复查宿主身份（原生原地重注入后不重载页面也要生效）', () => {
    expect(storeSrc).toContain('const host = isEmbeddedHostPage() ? readHostIdentity() : null')
    expect(storeSrc).toContain('if (!sameSession(hostSelector, activeSessionRef.current)) setActiveSession(hostSelector)')
  })
})
