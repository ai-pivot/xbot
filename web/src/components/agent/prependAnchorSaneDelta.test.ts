/**
 * `isSanePrependDelta` —— prepend 位移的**合法性闸门**单测。
 *
 * 背景（2026-10-12 用户报告）：「有概率加载完毕后用户视角还是自动变到最上方」。根因：
 * settle 直接施加 `scrollTop += (锚当前 top − 拍摄时 top)` —— 一旦快照是**陈旧**的
 *（挂载期列表还在滚到底部 / 快照后视口被别的东西移动过），delta 会是很大的**负数**⇒
 * `scrollTop` 被夹到 0 = 用户看到「跳到最上方」。
 *
 * 物理约束：段恒在**视口上方**插入（预取带保证）⇒ 锚只可能**向下**移动，且位移不可能
 * 超过本次高度增长。越界 ⇒ 这个锚不能用来判断"该补多少" ⇒ 必须弃用。
 */
import { describe, expect, it } from 'vitest'

import { isSanePrependDelta } from '@/components/agent/MessageList'

describe('isSanePrependDelta（prepend 位移合法性闸门）', () => {
  it('正常 prepend：位移 ≈ 本次高度增长 ⇒ 可信', () => {
    expect(isSanePrependDelta(417, 417)).toBe(true)
    expect(isSanePrependDelta(10, 12)).toBe(true)
    expect(isSanePrependDelta(417.4, 417)).toBe(true) // 亚像素舍入容差
    expect(isSanePrependDelta(0.4, 0)).toBe(true) // 亚像素
  })

  it('⛔ 负位移 ⇒ 不可信（施加它会把 scrollTop 夹到 0 =「跳到最上方」）', () => {
    expect(isSanePrependDelta(-0.1, 417)).toBe(false)
    expect(isSanePrependDelta(-1, 417)).toBe(false)
    expect(isSanePrependDelta(-417, 417)).toBe(false)
    expect(isSanePrependDelta(-5000, 5000)).toBe(false)
  })

  it('⛔ 位移超过本次高度增长 ⇒ 陈旧快照，不可信（绝不能把视口拽走）', () => {
    expect(isSanePrependDelta(418, 417)).toBe(false)
    expect(isSanePrependDelta(5000, 0)).toBe(false)
    expect(isSanePrependDelta(1, 0)).toBe(false) // 没长高却有 1px 位移 = 无关变化
  })

  it('零位移不走本判据（由「收敛」分支处理）', () => {
    expect(isSanePrependDelta(0, 417)).toBe(false)
    expect(isSanePrependDelta(0, 0)).toBe(false)
  })

  it('内容变短（growth < 0）时不放行正位移', () => {
    expect(isSanePrependDelta(3, -5)).toBe(false)
    expect(isSanePrependDelta(1, -1)).toBe(false)
  })
})
