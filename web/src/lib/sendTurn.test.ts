import { describe, expect, it } from 'vitest'

import { sendStartsTurn } from './sendTurn'

/**
 * REPRO（2026-09-17 用户报告）：`!pwd` 输出可见了，但**下方多出一个「思考中」**。
 *
 * 根因：`AgentPanel.onSendSuccess` 对**每一条**发送都乐观置 `running`（busy）。
 * 命令（`!cmd` / slash）后端按设计**没有 turn**（无 turn_started、无 turn_id、
 * 无 session(idle)）⇒ 这个乐观状态**永远清不掉** ⇒ 会话卡 busy；而命令不产生
 * live 行（`liveId === null`）⇒ `MessageList` 的 busy 占位符在列表**最底部**渲染
 * `ShimmerThinking`（「思考中…」）。
 *
 * 契约：只有会开启 turn 的发送才可以乐观置 busy。
 */
describe('sendStartsTurn（乐观 busy 的适用条件）', () => {
  it('命令回复：无 turn_id 且未排队 ⇒ 不得乐观置 busy（否则永久卡 busy → 多出「思考中」）', () => {
    expect(sendStartsTurn({ turnID: undefined, queued: false })).toBe(false)
    expect(sendStartsTurn({ turnID: 0, queued: false })).toBe(false)
    // 后端对命令**省略** turn_id（omitempty）—— 最常见形态
    expect(sendStartsTurn({})).toBe(false)
  })

  it('普通消息：REST 响应带 turn_id ⇒ 可以乐观置 busy（turn 生命周期会清除它）', () => {
    expect(sendStartsTurn({ turnID: 1, queued: false })).toBe(true)
    expect(sendStartsTurn({ turnID: 42 })).toBe(true)
  })

  it('排队消息：无 turn_id 但 queued=true ⇒ 会开启 turn（清空队列后），可以乐观置 busy', () => {
    expect(sendStartsTurn({ turnID: undefined, queued: true })).toBe(true)
  })

  it('没有响应信息（未知）⇒ 保守不置位（SSE session(busy) 仍是主路径，避免无清除路径的状态）', () => {
    expect(sendStartsTurn(undefined)).toBe(false)
  })
})
