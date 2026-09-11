/**
 * TurnBody tests — per-iteration rendering.
 *
 * 每个 iteration 独立渲染（无 turn 级折叠、无跨迭代工具合并）。
 * reasoning 的字符数必须是**真实值**（`iteration.reasoning.length`），
 * 不允许 /4 之类的估算（用户要求"永远显示正确的多少 char"）。
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { TurnBody } from '@/components/agent/TurnBody'
import { renderWithProviders } from '@/test-utils'
import type { WebIteration } from '@/types/shared'

describe('TurnBody per-iteration rendering (real char count, not a /4 estimate)', () => {
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
      <TurnBody iterations={iterations} turnID={1} />,
    )
    // 真实 2 字符（旧 /4 估算 Math.ceil(2/4)=1）
    expect(container.textContent).toMatch(/2/)
  })
})
