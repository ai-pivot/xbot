/**
 * i18n 一致性守卫：**语义相反的键不得使用同一个值**。
 *
 * 事故（用户报告："为什么没完成的（蓝色）你显示已完成"）：
 * zh-CN 的 `agent.goal.inProgress` 被复制成了 `'已完成'`（与 `agent.goal.completed`
 * 同值）。GoalBanner 的映射逻辑本身是对的（completed ? completed : inProgress），
 * 所以「进行中」的目标渲染出：accent 蓝边框 + target 图标 + 脉动动画（都是
 * 未完成分支）配上一个写着「已完成」的徽章 —— 自相矛盾。
 *
 * 这类错误是纯复制粘贴造成的，在 i18n 目录此前没有任何测试守护。这里用一份
 * 极小、语义明确的"必须区分"键清单拦住它。
 */
import { describe, expect, it } from 'vitest'

import en from './en'
import ja from './ja'
import zhCN from './zh-CN'

/** [keyA, keyB] —— 这两个键表达相反/互斥的状态，值必须不同。 */
const MUST_DIFFER: Array<[string, string]> = [
  ['agent.goal.inProgress', 'agent.goal.completed'],
  ['agent.goalModeOn', 'agent.goalModeOff'],
  ['agent.switchToQueueMode', 'agent.switchToInterjectMode'],
  ['agent.interjectModeHint', 'agent.queueModeHint'],
]

function at(dict: unknown, path: string): unknown {
  return path
    .split('.')
    .reduce<unknown>((acc, k) => (acc as Record<string, unknown> | undefined)?.[k], dict)
}

const DICTS: Array<[string, unknown]> = [
  ['zh-CN', zhCN],
  ['en', en],
  ['ja', ja],
]

describe('i18n 一致性守卫', () => {
  for (const [locale, dict] of DICTS) {
    it(`${locale}: 语义相斥的键不得同值`, () => {
      for (const [a, b] of MUST_DIFFER) {
        const va = at(dict, a)
        const vb = at(dict, b)
        if (typeof va !== 'string' || typeof vb !== 'string') continue // 该语言未定义则跳过
        expect(va, `${locale}: ${a} 与 ${b} 不能同为 ${JSON.stringify(va)}`).not.toBe(vb)
      }
    })
  }

  it('zh-CN: Goal 状态文案必须可区分（回归）', () => {
    expect(at(zhCN, 'agent.goal.inProgress')).toBe('进行中')
    expect(at(zhCN, 'agent.goal.completed')).toBe('已完成')
  })
})
