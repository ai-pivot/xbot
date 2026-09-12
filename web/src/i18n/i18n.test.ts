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

/**
 * 插值语法守卫：i18next 26 的默认插值是 `{{ }}`（双括号），单括号 `{name}`
 * **不会**被替换 —— 会原样渲染给用户（用户报告："当前生效：{mode} 这个显示的
 * 模板参数没有生效"）。zh-CN 里曾有 4 处存量单括号、新增 1 处，全部统一为
 * 双括号；此测试确保不再回流。
 */
const SINGLE_BRACE = /(^|[^{])\{[a-zA-Z_][a-zA-Z0-9_.]*\}([^}]|$)/

function collectStrings(node: unknown, prefix = ''): Array<[string, string]> {
  if (typeof node === 'string') return [[prefix, node]]
  if (node && typeof node === 'object') {
    return Object.entries(node as Record<string, unknown>).flatMap(([k, v]) =>
      collectStrings(v, prefix ? `${prefix}.${k}` : k),
    )
  }
  return []
}

describe('i18n 插值语法守卫', () => {
  for (const [locale, dict] of DICTS) {
    it(`${locale}: 不得使用单括号占位符（i18next 默认 {{ }}，单括号原样渲染）`, () => {
      const offenders = collectStrings(dict).filter(([, value]) => SINGLE_BRACE.test(value))
      expect(
        offenders,
        `以下键使用单括号插值，不会生效：${JSON.stringify(offenders)}`,
      ).toEqual([])
    })
  }
})
