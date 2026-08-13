/**
 * Tests for ShimmerThinking strong-consistency diagnostic.
 *
 * 强一致性保证：思考中… 必须渲染在消息列表的最新位置 —— 它之后（下方）
 * 不能存在任何同级 DOM / turn / iter 内容。若有，console 打印错误 + 状态。
 */
import { describe, expect, it, vi, afterEach } from 'vitest'

import { ShimmerThinking } from '@/components/agent/ShimmerThinking'
import { renderWithProviders } from '@/test-utils'

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ShimmerThinking 强一致性检查', () => {
  it('无后续兄弟：不打印任何诊断', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    renderWithProviders(<ShimmerThinking />)
    expect(errorSpy).not.toHaveBeenCalled()
    expect(warnSpy).not.toHaveBeenCalled()
  })

  it('下方是消息行（data-message-id/turn-id/iter-count）→ console.error 打印错误和状态', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    renderWithProviders(
      <div data-message-list-content>
        {/* LiveIteration 的 turn-N-live 行，内部渲染思考中 */}
        <div data-message-id="turn-5-live" data-turn-id="5" data-iter-count="0" role="row">
          <ShimmerThinking />
        </div>
        {/* 思考中下方的消息行 —— 强一致性违反 */}
        <div data-message-id="seq-100" data-turn-id="6" data-iter-count="2" role="row">
          assistant content
        </div>
      </div>,
    )
    expect(warnSpy).not.toHaveBeenCalled()
    expect(errorSpy).toHaveBeenCalledTimes(1)
    const [message, state] = errorSpy.mock.calls[0]
    expect(String(message)).toContain('THINKING_CONSISTENCY')
    // 状态包含定位信息：turnID / iterCount / sibling 标记
    expect(state).toMatchObject({ isTurnOrIter: true, siblingTurnID: '6', siblingIterCount: '2' })
    expect(state.depth).toBe(1)
    expect(state.listContext).toBeTruthy()
  })

  it('下方是普通同级 DOM（无消息行标记，如 footer）→ console.warn 一次性（不刷屏）', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    renderWithProviders(
      <div data-message-list-content>
        <div data-message-id="seq-1" role="row">prev</div>
        {/* busy placeholder：思考中包裹在容器内，其后是 footer */}
        <div className="px-3 py-2">
          <ShimmerThinking />
        </div>
        <div className="px-3 py-2">footer content</div>
      </div>,
    )
    expect(errorSpy).not.toHaveBeenCalled()
    expect(warnSpy).toHaveBeenCalledTimes(1)
    const [message, state] = warnSpy.mock.calls[0]
    expect(String(message)).toContain('THINKING_CONSISTENCY')
    expect(state).toMatchObject({ isTurnOrIter: false })
  })

  it('下方是空白兄弟（无可见内容）→ 跳过，不打印', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    renderWithProviders(
      <div>
        <div>
          <ShimmerThinking />
        </div>
        <div aria-hidden="true" />
        <div aria-hidden="true" />
      </div>,
    )
    expect(errorSpy).not.toHaveBeenCalled()
    expect(warnSpy).not.toHaveBeenCalled()
  })
})
