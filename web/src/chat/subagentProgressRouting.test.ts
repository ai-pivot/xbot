/**
 * subagentProgressRouting.test.ts — 2026-11-04 P0
 *
 * 用户实测：「子代理窗口只有 user 消息，看不到 streaming 进度；主代理窗口正常」。
 *
 * 根因：子代理的进度事件里 `payload.ChatID` 是**逻辑地址**
 * `agent:web:web-4/court.shoufu:main`（后端 `agentProgressKey := "agent:" + key`），
 * 而面板传入的 progressChatID 是**物理 key** `web:web-4/court.shoufu:main`。
 * 旧 `stripChannel` 只剥一层前缀 ⇒ 两边归一出不同结果 ⇒ `normalizeEvent` 在
 * chat 过滤处直接 `return null`，把子代理**全部**进度事件静默丢弃。
 *
 * 这类 bug 不报错、不崩溃，只表现为"前端很安静"，所以必须有判别力测试。
 */
import { describe, expect, it } from 'vitest'

import { normalizeEvent } from './normalize'

function progressEvent(chatID: string) {
  return {
    type: 'progress_structured',
    chat_id: chatID,
    progress: {
      chat_id: chatID,
      phase: 'thinking',
      iteration: 3,
      content: '正在读代码',
      turn_id: 42,
    },
  }
}

describe('subagent progress routing', () => {
  it('accepts logical agent chat_id when panel subscribes to the physical key', () => {
    // 后端实际发出的形态 → 前端实际订阅的形态
    const evs = normalizeEvent(
      progressEvent('agent:web:web-4/court.shoufu:main'),
      'web:web-4/court.shoufu:main',
    )
    expect(evs, '子代理的进度事件被 chat 过滤丢掉了（用户现象：子代理窗口没有 streaming 进度）').not.toBeNull()
    expect(evs!.length).toBeGreaterThan(0)
  })

  it('accepts physical chat_id when panel holds the logical key (反向亦然)', () => {
    const evs = normalizeEvent(
      progressEvent('web:web-4/court.shoufu:main'),
      'agent:web:web-4/court.shoufu:main',
    )
    expect(evs).not.toBeNull()
  })

  it('still keeps main-session matching intact', () => {
    const evs = normalizeEvent(progressEvent('web-4'), 'web-4')
    expect(evs).not.toBeNull()
    const withPrefix = normalizeEvent(progressEvent('web:web-4'), 'web-4')
    expect(withPrefix).not.toBeNull()
  })

  it('still drops events from a different session (判别力)', () => {
    const evs = normalizeEvent(
      progressEvent('agent:web:web-4/court.gongbu:main'),
      'web:web-4/court.shoufu:main',
    )
    expect(evs, '别的子代理的进度不能串进本面板').toBeNull()
  })
})
