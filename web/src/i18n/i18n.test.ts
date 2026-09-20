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

/**
 * 「按钮文案 = 状态文案」守卫（2026-09-12 用户报告："设置 → 开发者，所有按钮都
 * 显示为导出中，即使我还没点击"）。
 *
 * 根因：zh-CN / ja 的 4 个导出按钮文案（exportMulticaBtn / exportBenchmarkBtn /
 * exportOpenAIBtn / exportCodexBtn）被复制成了 `exporting` 的值 —— 未点击时按钮
 * 渲染的正是它自己的文案，于是永远显示「导出中…」（en 是对的）。
 *
 * 这类"复制粘贴串味"在 i18n 里没有编译期保护，只能靠守卫测试拦住。
 */
describe('i18n 守卫：按钮文案不得等于状态文案', () => {
  const EXPORT_BTNS = [
    'settings.developer.exportTurnIterBtn',
    'settings.developer.exportMulticaBtn',
    'settings.developer.exportBenchmarkBtn',
    'settings.developer.exportOpenAIBtn',
    'settings.developer.exportCodexBtn',
  ]

  for (const [locale, dict] of DICTS) {
    it(`${locale}: 导出按钮文案必须存在且不等于 exporting`, () => {
      const exporting = at(dict, 'settings.developer.exporting')
      expect(typeof exporting, `${locale}: 缺少 settings.developer.exporting`).toBe('string')
      for (const key of EXPORT_BTNS) {
        const label = at(dict, key)
        expect(typeof label, `${locale}: 缺少 ${key}`).toBe('string')
        expect((label as string).trim(), `${locale}: ${key} 不能为空`).not.toBe('')
        expect(
          label,
          `${locale}: ${key} 与 exporting 同值 → 未点击就显示「导出中…」`,
        ).not.toBe(exporting)
      }
    })
  }

  it('ja: 不得残留简体中文（导出 / 无 / 败 等专用字形）', () => {
    const SIMPLIFIED_ONLY = /[导无败]/
    for (const key of [
      'settings.developer.exporting',
      'settings.developer.noActiveSession',
      'settings.developer.exportFailed',
      'settings.developer.exportSection',
    ]) {
      const v = at(ja, key) as string
      expect(SIMPLIFIED_ONLY.test(v), `ja: ${key} 残留简体中文: ${v}`).toBe(false)
    }
  })
})

/**
 * 占位符一致性守卫（2026-09-17 用户报告："删除会话的时候弹窗内容有问题，看上去是
 * 占位符没有实际被替换掉"）。
 *
 * 事故：`session.deleteConfirm` 文案写的是 `{{username}}`，而唯一调用点传的是
 * `{ name }` —— i18next 取不到 username，把模板**原样渲染**成
 * `Delete session "{{username}}"?`。这类错误不报错、不告警，只能靠肉眼在界面上发现。
 *
 * 两条不变量（同一轮审计还发现 30+ 处同族缺陷：某种语言丢了占位符、或调用点传值
 * 而文案里没有对应占位符 → 值永不显示）：
 *   1. `t('key', {...})` 传入的参数必须覆盖文案里的**每个**占位符；
 *   2. 三种语言的同一 key 必须使用**完全相同**的占位符集合。
 * 两条都直接对应「用户看到 {{xxx}} 或看不到值」这一症状。
 */
function flatten(node: unknown, prefix = '', out: Record<string, string> = {}): Record<string, string> {
  if (typeof node === 'string') {
    if (prefix) out[prefix] = node
    return out
  }
  if (node && typeof node === 'object') {
    for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
      flatten(v, prefix ? `${prefix}.${k}` : k, out)
    }
  }
  return out
}

const placeholders = (s: string): string[] => [...s.matchAll(/\{\{\s*([\w.]+)\s*\}\}/g)].map((m) => m[1])

/** i18next 的保留选项键 —— 出现在 `t(key, {...})` 里但不是插值参数。 */
const I18N_OPTION_KEYS = new Set([
  'count', 'defaultValue', 'ns', 'lng', 'context', 'replace', 'interpolation',
  'keySeparator', 'nsSeparator', 'postProcess', 'returnObjects', 'joinArrays',
])

// 源码文本（Vite raw glob；无需 node:fs —— web tsconfig 未含 node 类型）。
const SOURCES = import.meta.glob('../**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

describe('i18n 占位符守卫', () => {
  it('t() 调用点传入的参数必须覆盖文案里的每个占位符', () => {
    const zh = flatten(zhCN)
    const problems: string[] = []
    for (const [file, src] of Object.entries(SOURCES)) {
      if (/\.(test|spec)\./.test(file) || file.includes('/i18n/')) continue
      for (const m of src.matchAll(/\bt\(\s*'([\w.]+)'\s*,\s*\{([^}]*)\}/g)) {
        const [, key, body] = m
        const value = zh[key]
        if (value === undefined) {
          problems.push(`${file}: key 不存在于 zh-CN: ${key}`)
          continue
        }
        if (body.includes('...')) continue // 展开无法静态判定
        const params = new Set(
          body.split(',').map((p) => p.trim().split(':')[0].trim()).filter((p) => /^[\w$]+$/.test(p)),
        )
        const paramsForThisKey = new Set([...params].filter((p) => !I18N_OPTION_KEYS.has(p)))
        for (const need of placeholders(value)) {
          if (!params.has(need)) {
            problems.push(
              `${file}: t('${key}') 缺参数 {{${need}}} → 会渲染出字面量模板` +
                `（文案=${JSON.stringify(value)}，实际传=[${[...paramsForThisKey].join(', ')}]）`,
            )
          }
        }
      }
    }
    expect(problems, `调用点与文案占位符不一致：\n${problems.join('\n')}`).toEqual([])
  })

  it('三种语言的同一 key 必须使用完全相同的占位符集合', () => {
    const dicts: Array<[string, Record<string, string>]> = [
      ['zh-CN', flatten(zhCN)],
      ['en', flatten(en)],
      ['ja', flatten(ja)],
    ]
    const problems: string[] = []
    const keys = new Set(dicts.flatMap(([, d]) => Object.keys(d)))
    for (const key of [...keys].sort()) {
      const sets = dicts.map(
        ([locale, d]) => [locale, placeholders(d[key] ?? '').sort().join(',')] as const,
      )
      const [baseLocale, base] = sets[0]
      for (const [locale, s] of sets) {
        if (s !== base) {
          problems.push(`${key}: ${baseLocale}=[${base}] 但 ${locale}=[${s}]`)
        }
      }
    }
    expect(problems, `三语言占位符漂移（某种语言会丢值）：\n${problems.join('\n')}`).toEqual([])
  })
})
