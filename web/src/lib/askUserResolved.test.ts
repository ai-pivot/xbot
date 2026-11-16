/**
 * ask_user_resolved 收回提示（G-B20261030-01）。
 *
 * 判据（对应任务书 b）：
 *   同一条 ask_user_resolved 事件，`cancel_reason='ttl_expired'` 的文案含「超时」，
 *   缺该键（主动取消）的文案不含「超时」，两者 `!==`。
 * 另外锁三语同步：zh-CN / en / ja 两条文案都在、都不相同、都不回落到裸 key。
 */
import { afterEach, describe, expect, it } from 'vitest'

import i18n from '@/i18n'
import {
  askUserResolvedTip,
  askUserResolvedTipKey,
  shouldTipAskUserResolved,
  ASK_USER_RESOLVED_CANCELLED_KEY,
  ASK_USER_RESOLVED_TTL_KEY,
} from './askUserResolved'

const ORIGINAL_LNG = i18n.language || 'zh-CN'

afterEach(async () => {
  await i18n.changeLanguage(ORIGINAL_LNG)
})

describe('ask_user_resolved 收回文案：超时收回 vs 主动取消', () => {
  it('zh-CN：ttl_expired 含「超时」，缺键不含「超时」，两文案不相等', async () => {
    await i18n.changeLanguage('zh-CN')

    const ttl = askUserResolvedTip('ttl_expired')
    const active = askUserResolvedTip(undefined)

    expect(ttl).toContain('超时')
    expect(active).not.toContain('超时')
    expect(ttl).not.toBe(active)
    // 缺失 / null / 空串 / 未知值一律归主动取消 —— 宁可少说，不把"我取消的"说成"超时"
    expect(askUserResolvedTip(null)).toBe(active)
    expect(askUserResolvedTip('')).toBe(active)
    expect(askUserResolvedTip('some_future_reason')).toBe(active)
  })

  it('reason → 文案键：只有 ttl_expired 走超时键', () => {
    expect(askUserResolvedTipKey('ttl_expired')).toBe(ASK_USER_RESOLVED_TTL_KEY)
    expect(askUserResolvedTipKey(undefined)).toBe(ASK_USER_RESOLVED_CANCELLED_KEY)
    expect(askUserResolvedTipKey('some_future_reason')).toBe(ASK_USER_RESOLVED_CANCELLED_KEY)
    expect(askUserResolvedTipKey('ttl_expired')).not.toBe(askUserResolvedTipKey(undefined))
  })

  it('三语（zh-CN / en / ja）都解析出两条不同文案，只有超时那条提到超时', async () => {
    const cases = [
      ['zh-CN', '超时'],
      ['en', 'timed out'],
      ['ja', 'タイムアウト'],
    ] as const

    for (const [locale, timeoutWord] of cases) {
      await i18n.changeLanguage(locale)
      const ttl = askUserResolvedTip('ttl_expired')
      const active = askUserResolvedTip(undefined)

      // 不回落到裸 key（= 该语言缺键）
      expect(ttl, `${locale}: 缺 ${ASK_USER_RESOLVED_TTL_KEY}`).not.toBe(ASK_USER_RESOLVED_TTL_KEY)
      expect(active, `${locale}: 缺 ${ASK_USER_RESOLVED_CANCELLED_KEY}`).not.toBe(
        ASK_USER_RESOLVED_CANCELLED_KEY,
      )
      expect(ttl).toContain(timeoutWord)
      expect(active).not.toContain(timeoutWord)
      expect(active.length).toBeGreaterThan(0)
      expect(ttl).not.toBe(active)
    }
  })

  it('shouldTip：只有 reason="cancelled"（提问被收回）才提示', () => {
    expect(shouldTipAskUserResolved({ type: 'ask_user_resolved', reason: 'cancelled' })).toBe(true)
    for (const reason of ['answered', 'rewound', 'cleared', '']) {
      expect(shouldTipAskUserResolved({ type: 'ask_user_resolved', reason })).toBe(false)
    }
    // 非 resolved 事件一律不提示（同一个 reason 值也不提示）
    expect(shouldTipAskUserResolved({ type: 'ask_user', reason: 'cancelled' })).toBe(false)
  })
})
