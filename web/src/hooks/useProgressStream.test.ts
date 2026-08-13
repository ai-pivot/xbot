import { describe, expect, it } from 'vitest'

import { hasVisibleProgress } from '@/hooks/useProgressStream'
import type { ProgressSnapshot } from '@/types/shared'

function snap(over: Partial<ProgressSnapshot>): ProgressSnapshot {
  return {
    eventSeq: 0,
    phase: '',
    iteration: 0,
    streamContent: '',
    reasoningStreamContent: '',
    content: '',
    streaming: true,
    activeTools: [],
    completedTools: [],
    iterationHistory: [],
    streamingTools: [],
    genuiContent: '',
    lastIter: 0,
    lastReasoning: '',
    todos: [],
    subAgents: [],
    tokenUsage: null,
    turnID: 0,
    ...over,
  }
}

describe('hasVisibleProgress', () => {
  it('returns true at the iteration boundary — all visible fields cleared, lastIter > 0', () => {
    // User report: "agent turn 消失然后又出现" — a new iteration started, the
    // previous iteration's active/completed tools were JUST cleared (the
    // clearing event is a phase:undefined stream delta carrying NO
    // iteration_history), and the new iteration's iterationHistory delta has
    // not arrived yet. Every visible field is momentarily empty. Without the
    // lastIter>0 guard the live row VANISHES for a frame until the next
    // structured event appends the iteration history.
    expect(hasVisibleProgress(snap({ lastIter: 3 }))).toBe(true)
  })

  it('returns false for a fresh pre-iteration thinking phase (lastIter=0, nothing visible)', () => {
    // Turn just started, nothing rendered yet — the busy placeholder shows.
    expect(hasVisibleProgress(snap({}))).toBe(false)
  })

  it('returns false after the turn ended (store reset → lastIter=0)', () => {
    // text event → resetProgress → store.reset() clears lastIter; no live row.
    expect(hasVisibleProgress(snap({ streaming: false, phase: 'done' }))).toBe(false)
  })

  it('returns true with only iterationHistory present', () => {
    expect(
      hasVisibleProgress(
        snap({
          iterationHistory: [
            { iteration: 1, thinking: 't', reasoning: '', tools: [], toolCount: 0 },
          ],
        }),
      ),
    ).toBe(true)
  })
})

// ── 切换会话 hydration 还原 live iter 状态 ──
// 用户需求：切换会话必须还原 live iter（已完成迭代 + 进行中工具），否则
// SSE 在执行 tool 很久没有新事件时会一直卡着（无进度显示）。
// hydration effect 从 active_progress 恢复 ProgressStore + MessageStore。
import { renderHook, waitFor } from '@testing-library/react'
import { MessageStore } from '@/components/agent/messageStore'
import { useProgressStream } from '@/hooks/useProgressStream'
import type { WSConnection } from '@/types/ws'

describe('切换会话 hydration 还原 live iter', () => {
  it('从 active_progress 恢复进行中 turn 的 live（已完成迭代 + 进行中工具）', async () => {
    const ms = new MessageStore()
    const ws = {
      onMessage: () => () => {},
      rpc: vi.fn(),
      send: vi.fn(),
      onConnectionChange: () => () => {},
      connected: false,
    } as unknown as WSConnection
    const initialProgress = {
      phase: 'tool_exec',
      turn_id: 360,
      iteration: 2,
      active_tools: [{ name: 'Shell', label: 'Shell: 长时间任务', status: 'running', iteration: 2 }],
      completed_tools: [{ name: 'WebSearch', label: 'WebSearch', status: 'done', iteration: 1 }],
      iteration_history: [
        { iteration: 1, content: '第一步完成', reasoning: '', tools: [], tool_count: 0 },
      ],
      content: '',
    }
    renderHook(() =>
      useProgressStream({
        chatID: 'chat-1',
        initialProgress,
        ws,
        messageStore: ms,
      }),
    )
    // hydration 恢复：MessageStore 的 live 含已完成迭代 + 进行中工具
    await waitFor(() => expect(ms.getLive(360)).toBeDefined())
    const live = ms.getLive(360)
    expect(live?.iterations?.map((i) => i.iteration)).toEqual([1])
    expect(live?.activeTools?.map((t) => t.name)).toEqual(['Shell'])
    expect(live?.phase).toBe('tool_exec')
  })
})
