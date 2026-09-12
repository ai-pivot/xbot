/**
 * 守护：**气泡之外的格式已被彻底删除，不得回归**（用户要求，2026-09-12：
 * "这个过时样式必须彻底，完全删光相关代码，现在必须都是这种样式"）。
 *
 * 被删除的形态：
 *   - 折叠级别 CollapseLevel（'all' / 'minimal' / 'none'）+ 整 turn 摘要折叠行
 *     （`已处理 N 次迭代 · 调用 M 个工具` / `Processed N iterations · M tools`）；
 *   - 跨迭代合并工具 mergeTools（flattenIterations 的 blocks 合并路径）；
 *   - 落地实现：useCollapseLevel.ts / defaultOpenForLevel、types 常量、
 *     SettingsInteraction 的「合并工具调用」开关、i18n 的 processed /
 *     mergeTools* / collapseLevel* key。
 *
 * 唯一允许的形态：每个迭代独立渲染（TurnBody → IterationGroup），迭代内每个
 * 工具一个独立 pill。
 *
 * 两层守护：
 *   1. 源码层 —— 上述标识符/存储 key 不得在任何非测试源文件里出现；
 *   2. 渲染层 —— AssistantMessage 逐迭代渲染（data-iter-id 每迭代一个），
 *      且不出现整 turn 摘要行（removed i18n key 会渲染成原始 key，可断言）。
 */
// @ts-expect-error node builtin — test-only file; the web tsconfig has no node types
import { readdirSync, readFileSync } from 'node:fs'
// @ts-expect-error node builtin — test-only file
import { dirname, join, resolve } from 'node:path'
// @ts-expect-error node builtin — test-only file
import { fileURLToPath } from 'node:url'

import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { AssistantMessage } from '@/components/agent/AssistantMessage'
import { I18nProvider } from '@/providers/i18n'
import type { ChatMessage, WebIteration } from '@/types/shared'

const SRC = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

/** 代码形态的禁用标识（不匹配纯注释里的中文说明）。 */
const FORBIDDEN_CODE = [
  'useCollapseLevel',
  'defaultOpenForLevel',
  'xbot-collapse-level',
  'xbot-merge-tools',
  'collapseLevel=',
  'collapseLevel:',
  'collapseLevel?:',
  'mergeTools=',
  'mergeTools:',
  'mergeTools?:',
  'agent.processed',
]

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(join(SRC, dir), { withFileTypes: true })) {
    const rel = dir === '.' ? entry.name : `${dir}/${entry.name}`
    if (entry.isDirectory()) {
      sourceFiles(rel, out)
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.(ts|tsx)$/.test(entry.name)) {
      out.push(rel)
    }
  }
  return out
}

describe('旧折叠/合并格式必须彻底不存在（防回归）', () => {
  const files = sourceFiles('.')

  it('扫描到的源文件集合非空（守护本身有效）', () => {
    expect(files.length).toBeGreaterThan(50)
  })

  it.each(FORBIDDEN_CODE)('源码中不再出现 %s', (needle) => {
    const hits = files.filter((f) => readFileSync(join(SRC, f), 'utf8').includes(needle))
    expect(hits, `${needle} 残留于: ${hits.join(', ')}`).toEqual([])
  })

  it('i18n 三个语言包都没有 collapse/merge 相关 key', () => {
    for (const lang of ['zh-CN', 'en', 'ja']) {
      const text = readFileSync(join(SRC, `i18n/${lang}.ts`), 'utf8')
      expect(text).not.toMatch(/\b(collapseLevel|mergeTools|processed)\s*:/)
    }
  })
})

describe('渲染层：只有"逐迭代 + 每工具一 pill"这一种形态', () => {
  const iterations: WebIteration[] = [1, 2].map((n) => ({
    iteration: n,
    content: `out-${n}`,
    reasoning: `reason-${n}`,
    tools: [{ name: 'Read', label: `f${n}.ts`, status: 'done' }],
    toolCount: 1,
  })) as unknown as WebIteration[]

  const message: ChatMessage = {
    id: 'a1',
    role: 'assistant',
    content: '',
    iterations,
    timestamp: '2026-09-12T00:00:00Z',
    isPartial: false,
    turnID: 1,
  }

  it('每个迭代独立渲染且不产生整 turn 摘要行', () => {
    const { container } = render(<AssistantMessage message={message} />, {
      wrapper: ({ children }) => <I18nProvider>{children}</I18nProvider>,
    })

    const blocks = Array.from(container.querySelectorAll('[data-iter-id]'))
    expect(blocks.map((b) => b.getAttribute('data-iter-id'))).toEqual(['1', '2'])

    // 摘要行 key（agent.processed）已删除 —— 若回归会渲染成原始 key
    expect(container.textContent ?? '').not.toContain('agent.processed')
    // 删除后的 i18n key 兜底：不能出现"已处理 … 次迭代"这类摘要文案
    expect(container.textContent ?? '').not.toMatch(/已处理\s*\d+\s*次迭代/)
    expect(container.textContent ?? '').not.toMatch(/Processed\s+\d+\s+iterations/)

    // 每个工具一个 pill（每迭代 1 个）
    expect(container.querySelectorAll('[data-testid="tool-pill"]').length).toBe(2)
  })
})
