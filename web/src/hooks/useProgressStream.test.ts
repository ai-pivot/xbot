import { describe, expect, it, vi } from 'vitest'

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
            { iteration: 1, content: 't', reasoning: '', tools: [], toolCount: 0 },
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

  it('text 事件顶层 content（最终回复）合并进最后迭代 content（v55 架构）', async () => {
    // v55 回归：text 事件带顶层 content（最终回复）+ progress_history（旧格式
    // thinking 字段 / 最后迭代 content 为空）。渲染层 hasIterations=true 时不渲染
    // message.content —— 若不把 finalText 合并进最后迭代 content，最终回复丢失
    // （notification E2E "Done processing notification." 断言失败）。
    const ms = new MessageStore()
    let onMessageCb: ((msg: unknown) => void) | undefined
    let completeArgs: [string, WebIteration[]] | undefined
    const ws = {
      onMessage: (cb: (msg: unknown) => void) => { onMessageCb = cb; return () => {} },
      rpc: vi.fn(),
      send: vi.fn(),
      onConnectionChange: () => () => {},
      connected: false,
    } as unknown as WSConnection & { onMessage: (cb: (msg: unknown) => void) => () => void }
    renderHook(() =>
      useProgressStream({
        chatID: 'chat-1',
        ws,
        messageStore: ms,
        onAssistantComplete: (finalText, iterations) => {
          completeArgs = [finalText, iterations]
        },
      }),
    )
    // 前置状态：turn_started + structured 事件（text 事件处理依赖 turn_id 上下文）
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'notification', content: '⏰ bg task done' }, chat_id: 'web:chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', iteration: 1, seq: 2, turn_id: 1, chat_id: 'web:chat-1', active_tools: [{ name: 'Grep', status: 'running', iteration: 1 }] },
    })
    // text 事件（模拟后端 sendMessage）：顶层 content + progress_history（thinking 旧格式）
    onMessageCb?.({
      type: 'text',
      content: 'Done processing notification.',
      turn_id: 1,
      chat_id: 'chat-1',
      progress_history: JSON.stringify([
        { iteration: 0, thinking: 'Reading file', completed_tools: [{ name: 'Read', status: 'done', iteration: 0 }] },
        { iteration: 1, thinking: 'Searching', completed_tools: [{ name: 'Grep', status: 'done', iteration: 1 }] },
      ]),
    })
    // v55: 最终回复 = 最终 iter 的 content —— text 事件顶层 content 合并进最后迭代
    await waitFor(() => expect(completeArgs).toBeDefined())
    expect(completeArgs![0]).toBe('Done processing notification.')
    expect(completeArgs![1]).toHaveLength(2)
    expect(completeArgs![1][completeArgs![1].length - 1].content).toBe('Done processing notification.')
  })
})
