/**
 * TurnBody tests — thinking char count (merged blocks path).
 *
 * REPRO（2026-09-04 用户报告："committed 之后数字不对，应该永远显示正确的多少 char"）：
 * TurnBody 的 merged blocks 路径（mergeTools=true 默认）reasoning label 用
 * `Math.ceil(block.text.length / 4)` 估算字符数（670 字符显示"思考 167 字"）
 * —— 与 IterationHistory 路径（`iteration.reasoning.length` 真实值）语义分裂。
 * 修复：统一为 i18n `agent.thinkingChars`（真实 block.text.length）。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { renderWithProviders } from '@/test-utils'
import type { WebIteration } from '@/types/shared'

describe('TurnBody thinking char count (merged blocks — real char count, not /4 estimate)', () => {
  it('REPRO: committed reasoning label shows REAL char count（block.text.length），不是 Math.ceil(len/4) 估算', () => {
    // 旧代码：Math.ceil(670/4)=168 → "思考 168 字"（用户 DOM 实测 167）
    // 修复后：真实 670 → "思考了 670 字符"（i18n key agent.thinkingChars）
    const reasoning = 'x'.repeat(670)
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning, tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} level="all" mergeTools={true} turnID={3159} />,
    )
    const text = container.textContent ?? ''
    // 真实字符数 670 必须出现（i18n zh-CN: '思考了 {{count}} 字符'）
    expect(text).toMatch(/670/)
    // /4 估算值（168）不得出现
    expect(text).not.toMatch(/16[0-9]\b/)
  })

  it('短 reasoning（<4 字符）也显示真实值 —— 不四舍五入到 0/1', () => {
    const iterations: WebIteration[] = [
      { iteration: 1, content: '', reasoning: 'ab', tools: [], toolCount: 0 },
    ]
    const { container } = renderWithProviders(
      <TurnBody iterations={iterations} level="all" mergeTools={true} turnID={1} />,
    )
    // 真实 2 字符（旧 /4 估算 Math.ceil(2/4)=1）
    expect(container.textContent).toMatch(/2/)
  })
})
