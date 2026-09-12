/**
 * TurnBody tests — thinking char count（逐迭代渲染路径）。
 *
 * REPRO（2026-09-04 用户报告："committed 之后数字不对，应该永远显示正确的多少 char"）：
 * reasoning label 曾用估算（670 字符显示"思考 167 字"），与 IterationHistory 路径
 * （`iteration.reasoning.length` 真实值）语义分裂。
 * 修复：统一为 i18n `agent.thinkingChars`（真实 iteration.reasoning.length）。
 * 折叠/合并路径已彻底删除（2026-09-12）—— 只剩逐迭代渲染这一条路径。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { renderWithProviders } from '@/test-utils'
import type { WebIteration } from '@/types/shared'

describe('TurnBody thinking char count (真实 char 数，不是 /4 估算)', () => {
  it('REPRO: committed reasoning label shows REAL char count（reasoning.length），不是 Math.ceil(len/4) 估算', () => {
    // 旧代码：Math.ceil(670/4)=168 → "思考 168 字"（用户 DOM 实测 167）
    // 修复后：真实 670 → "思考了 670 字符"（i18n key agent.thinkingChars）
    const reasoning = 'x'.repeat(670)
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning, tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} turnID={3159} />,
    )
    const text = container.textContent ?? ''
    // 完整 i18n 文案必须出现（测试环境默认 en: 'Thought {{count}} characters'）——
    // 只断言数字会漏估算值回归（168 也含 "16"），所以断言整串。
    expect(text).toContain('Thought 670 characters')
    // /4 估算值（168）不得出现
    expect(text).not.toMatch(/16[0-9]\b/)
  })

  it('短 reasoning（<4 字符）也显示真实值 —— 不四舍五入到 0/1', () => {
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning: 'ab', tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} turnID={1} />,
    )
    // 真实 2 字符（旧 /4 估算 Math.ceil(2/4)=1）—— 必须断言完整文案，
    // `toMatch(/2/)` 这种弱断言任何含 "2" 的文本都会通过（假绿）。
    expect(container.textContent).toContain('Thought 2 characters')
  })
})
