import { describe, expect, it } from 'vitest'

import { emptyReasonFor, statusLabel, statusTitle } from './connection'

/**
 * 2026-10-22 用户报障「通信视图永远离线」回归：
 * 「在线但没有数据」必须与「请求失败（离线）」区分开。
 */
describe('court connection semantics', () => {
  it('无群组 / 无消息属于在线空态，不是错误', () => {
    // 文案不再暴露「群组」这个内部概念（用户 2026-10-22 反馈）
    expect(emptyReasonFor(0, 0)).toContain('还没有可见的会话或消息')
    expect(emptyReasonFor(0, 0)).not.toContain('群组')
    expect(emptyReasonFor(1, 0)).toContain('还没有消息')
    expect(emptyReasonFor(1, 3)).toBeNull()
  })

  it('只有 offline 才显示「离线」；在线空数据仍显示「已连接」', () => {
    expect(statusLabel({ conn: 'loading' })).toBe('加载中…')
    expect(statusLabel({ conn: 'offline' })).toBe('离线')
    expect(statusLabel({ conn: 'online' })).toBe('已连接')
    expect(statusLabel({ conn: 'online' }, true)).toBe('预览')
  })

  it('在线空数据的 hover 提示给原因，断连的提示给真实错误', () => {
    expect(statusTitle({ conn: 'offline', error: 'HTTP 500', emptyReason: null })).toBe('HTTP 500')
    expect(statusTitle({ conn: 'online', error: null, emptyReason: '当前账号下还没有可见的会话或消息' }))
      .toBe('当前账号下还没有可见的会话或消息')
    expect(statusTitle({ conn: 'online', error: null, emptyReason: null })).toBe('已连接服务端')
  })
})
