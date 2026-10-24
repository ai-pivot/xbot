import '@testing-library/jest-dom/vitest'
/**
 * 后端原始报错 → 人话。
 *
 * 用户实测（2026-10-22）：聊天气泡里直接是
 * `dial tcp 127.0.0.1:9999: connect: connection refused #1 #2 #3 #4 #5`。
 * 要求：只显示「模型服务暂不可用（已重试 5 次）」+ 重试按钮 + 默认折叠的技术详情。
 */
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { HumanErrorNotice, classifyError, retryCountOf } from './HumanError'
import { RetryLastTurnContext } from './retryContext'

const RAW = 'dial tcp 127.0.0.1:9999: connect: connection refused #1 #2 #3 #4 #5'

describe('classifyError', () => {
  it('连接被拒 ⇒ 「模型服务暂不可用（已重试 5 次）」，原始串留在 raw 里', () => {
    const e = classifyError(RAW)
    expect(e).not.toBeNull()
    expect(e!.title).toBe('模型服务暂不可用（已重试 5 次）')
    expect(e!.attempts).toBe(5)
    expect(e!.raw).toBe(RAW)
    // 人话里不得出现技术串
    expect(e!.title).not.toMatch(/dial tcp|127\.0\.0\.1/)
    expect(e!.hint).not.toMatch(/dial tcp|connection refused/)
  })

  it('超时 / 限流 / 鉴权 各自有不同的人话标题', () => {
    expect(classifyError('context deadline exceeded')!.title).toMatch(/超时/)
    expect(classifyError('429 too many requests')!.title).toMatch(/限流/)
    expect(classifyError('401 Unauthorized: invalid api key')!.title).toMatch(/鉴权/)
    expect(classifyError('502 Bad Gateway')!.title).toMatch(/暂不可用/)
  })

  it('没有重试记录时标题不带「已重试」', () => {
    const e = classifyError('dial tcp 10.0.0.1:8080: connect: connection refused')
    expect(e!.attempts).toBe(0)
    expect(e!.title).toBe('模型服务暂不可用')
  })

  it('正常回复里只是提到报错 ⇒ 不拦截（不能把正文变成错误卡）', () => {
    expect(classifyError('我把 connection refused 加进了重试规则，现在连续失败会退避重试。')).toBeNull()
    expect(classifyError('# 排查记录\n\ndial tcp 127.0.0.1:9999 是服务没起来。\n\n- 先看端口\n- 再看日志')).toBeNull()
    expect(classifyError('这是一段很长的正常回复。'.repeat(80) + ' connection refused')).toBeNull()
  })

  it('空白文本不拦截', () => {
    expect(classifyError('   ')).toBeNull()
  })
})

describe('retryCountOf', () => {
  it('识别 #1…#5 / 已重试 5 次 / retry 3', () => {
    expect(retryCountOf(RAW)).toBe(5)
    expect(retryCountOf('调用失败，已重试 4 次')).toBe(4)
    expect(retryCountOf('failed after retry 3')).toBe(3)
    expect(retryCountOf('没有数字')).toBe(0)
  })
})

describe('HumanErrorNotice', () => {
  it('渲染人话标题 + 重试按钮；技术详情默认折叠', () => {
    const e = classifyError(RAW)!
    const { container } = render(
      <RetryLastTurnContext.Provider value={() => {}}>
        <HumanErrorNotice error={e} />
      </RetryLastTurnContext.Provider>,
    )
    expect(screen.getByTestId('human-error')).toHaveTextContent('模型服务暂不可用（已重试 5 次）')
    expect(screen.getByTestId('human-error-retry')).toBeInTheDocument()
    // <details> 默认关闭：技术详情不可见
    const details = container.querySelector('details')
    expect(details).not.toBeNull()
    expect(details!.open).toBe(false)
    expect(details).toHaveTextContent('dial tcp 127.0.0.1:9999')
  })

  it('没有重试上下文 ⇒ 不渲染点了没反应的按钮', () => {
    const e = classifyError(RAW)!
    render(<HumanErrorNotice error={e} />)
    expect(screen.queryByTestId('human-error-retry')).toBeNull()
  })
})
