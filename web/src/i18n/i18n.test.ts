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
  // 会话统计面板：水位标签 vs 模型名。zh-CN 曾把两者都写成「当前模型」，
  // 中文界面把 prompt token 水位显示成模型名（用户可见的错标）。
  ['plugins.sessionStats.contextLevel', 'plugins.sessionStats.currentModel'],
]

function at(dict: unknown, path: string): unknown {
  return path
    .split('.')
    .reduce<unknown>((acc, k) => (acc as Record<string, unknown> | undefined)?.[k], dict)
}

/** 递归展平字典为 "a.b.c" 路径集合（只走 plain object，叶子含 string）。 */
function keyPaths(obj: unknown, prefix = ''): string[] {
  if (obj === null || typeof obj !== 'object') return prefix ? [prefix] : []
  const out: string[] = []
  for (const [k, v] of Object.entries(obj as Record<string, unknown>)) {
    out.push(...keyPaths(v, prefix ? `${prefix}.${k}` : k))
  }
  return out
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

  it('zh-CN: 会话统计的水位标签不得与模型名同值（回归）', () => {
    // 曾把 contextLevel 误写成 '当前模型'（与 currentModel 同值）。
    expect(at(zhCN, 'plugins.sessionStats.contextLevel')).toBe('上下文使用量')
  })

  it('三个语言文件的 key 集合必须完全一致', () => {
    // 删/加 i18n 键时必须三个语言同步 —— 纯手工操作极易漏改（PR #350 删除
    // 折叠相关键时就差点漏掉 4 个死键）。
    const base = keyPaths(zhCN).sort()
    for (const [locale, dict] of DICTS) {
      expect(keyPaths(dict).sort(), `${locale} 的 key 集合与 zh-CN 不一致`).toEqual(base)
    }
  })
})
