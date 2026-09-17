/**
 * i18n 覆盖守卫（自维护）：**代码里静态使用的每一个 i18n 键，都必须在三种语言里存在**。
 *
 * 事故（2026-09-16 用户截图）：设置面板 Layout 页整片渲染成裸 key
 * （`settings.layout.itemNewChat`、`settings.layout.slotMobileTop`…）——
 * SettingsLayout.tsx 用了 20+ 个 `t('settings.layout.*')`，而 zh-CN/en/ja 里这些键
 * **一个都没有**，`t()` 回落成原样回显 key。
 *
 * 两层扫描（覆盖"字面量调用"与"映射表间接用法"）：
 *   ① `t('a.b.c')`；
 *   ② 任何形如 `'ns.a.b'` 且首段是已知命名空间的字符串字面量
 *      （Layout 页的键来自 `ITEM_LABELS[x] = 'settings.layout.…'`，调用处是
 *       `t(ITEM_LABELS[id])` —— 只扫 ① 会**假绿**，这一层就是为它加的）。
 *
 * ⚠️ 实现约束：**不使用任何 Node API**（`node:fs` / `__dirname`）。
 * 本项目 `src/**` 的 tsconfig 不含 node 类型 ⇒ 用 fs 会让 `tsc -b` 报
 * TS2307/TS2304，直接把 CI 的 Frontend job 弄红。改用 Vite 原生的
 * `import.meta.glob(..., { query: '?raw' })` 读取源码文本（类型由 vite/client 提供）。
 */

import { describe, expect, it } from 'vitest'

import en from './en'
import ja from './ja'
import zhCN from './zh-CN'

/** 以 ?raw 方式把 src 下所有源码/样式文件读成字符串（Vite 原生，无需 Node API）。 */
const RAW_SOURCES = import.meta.glob('../**/*.{ts,tsx}', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>

/** 允许的例外：形似键但**不是** i18n 键的字面量（逐个写明原因）。 */
const ALLOWED_NON_I18N = new Set<string>([
  // 后端/协议用的点分标识，不是语言键。
  'web.ui.layout-overrides',
  // plugin-api/rpc.ts 的 **RPC 方法名**（协议标识，不是文案）。
  'session.get',
  'session.list',
  'agent.cancel',
  // 命令 id（commands.execute('…') / AppShell 的命令表），不是语言键。
  'session.new',
  'settings.open',
])

function at(dict: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((acc, k) => (acc as Record<string, unknown> | undefined)?.[k], dict)
}

/** 顶层命名空间（settings / agent / nav / …）——判定"这个字面量像不像 i18n 键"。 */
function namespaces(dict: unknown): Set<string> {
  return new Set(Object.keys(dict as Record<string, unknown>))
}

const DICTS: Array<[string, unknown]> = [
  ['zh-CN', zhCN],
  ['en', en],
  ['ja', ja],
]

/** 收集源码里静态出现的 i18n 键（两层扫描；跳过测试文件自身）。 */
function usedKeys(): Map<string, string[]> {
  const ns = namespaces(zhCN)
  const keys = new Map<string, string[]>()
  const add = (key: string, file: string) => {
    if (ALLOWED_NON_I18N.has(key)) return
    const list = keys.get(key) ?? []
    list.push(file.replace(/^\.\.\//, ''))
    keys.set(key, list)
  }

  for (const [file, src] of Object.entries(RAW_SOURCES)) {
    if (/\.test\.tsx?$/.test(file)) continue
    // ① t('a.b.c')
    for (const m of src.matchAll(/\bt\(\s*'([A-Za-z0-9_.]+)'\s*\)/g)) add(m[1], file)
    // ② 任何 'ns.a.b' 字面量（首段是已知命名空间）
    for (const m of src.matchAll(/'([A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_-]+)+)'/g)) {
      const key = m[1]
      if (ns.has(key.split('.')[0])) add(key, file)
    }
  }
  return keys
}

describe('i18n 覆盖守卫：静态使用的键必须在三种语言里都存在', () => {
  const used = usedKeys()

  it('至少能扫到一批键（防扫描本身失灵）', () => {
    expect(used.size).toBeGreaterThan(100)
  })

  for (const [locale, dict] of DICTS) {
    it(`${locale}：所有静态键都存在`, () => {
      const missing: string[] = []
      for (const [key, files] of used) {
        if (at(dict, key) === undefined) missing.push(`${key}   ← ${files.slice(0, 2).join(', ')}`)
      }
      expect(missing.sort(), `${locale} 缺失 ${missing.length} 个键：\n${missing.sort().join('\n')}`).toEqual([])
    })
  }
})
