/**
 * LongText.markdown.test.tsx — 2026-11-04 小优化回归
 *
 * 结构化消息（报表/提问/决策）的正文必须按 **Markdown** 渲染：
 * 首辅的汇报里带表格、代码块、`###` 标题、`**加粗**`，纯文本渲染出来是
 * 一堆裸露的符号，用户明确要求改成 md。
 */
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { LongText } from './StructuredCards'

describe('LongText markdown rendering', () => {
  it('renders bold / code / list as real markdown elements', () => {
    const { container } = render(
      <LongText text={'**结论**：改 `3` 个文件\n\n- 第一项\n- 第二项'} />,
    )
    expect(container.querySelector('strong')?.textContent).toBe('结论')
    expect(container.querySelector('code')?.textContent).toBe('3')
    expect(container.querySelectorAll('li')).toHaveLength(2)
  })

  it('renders GFM tables (方案汇报里大量使用)', () => {
    const md = '| 文件 | 行数 |\n| --- | --- |\n| a.md | 352 |'
    const { container } = render(<LongText text={md} />)
    expect(container.querySelector('table')).not.toBeNull()
    expect(container.querySelectorAll('td')).toHaveLength(2)
  })
})
