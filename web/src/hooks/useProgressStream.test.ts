import { describe, expect, it, vi } from 'vitest'

import { hasVisibleProgress } from '@/hooks/useProgressStream'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

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

  it('AskUser WaitingUser: committed iterations KEEP the iteration content/reasoning', async () => {
    // 回归：AskUser WaitingUser 时 iterationHistory 为空（没有下一次迭代触发
    // attachIterationDelta，delta 从未到前端）——迭代的 content/reasoning 只在
    // snap.content / snap.lastReasoning。turn_started(2) commit 时必须 fold 进
    // 迭代，否则 v55 渲染（hasIterations=true 不渲染顶层 content）下 content 消失
    // （用户报告："askuser 渲染后迭代的 content 和 reasoning 消失"）。
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
    // Turn 1: agent 思考（reasoning + content）→ 调用 AskUser → WaitingUser。
    // iteration_history 为空（AskUser 迭代 delta 未 attach）；content/reasoning 在
    // 结构化字段。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: '帮我确认一下' }, chat_id: 'chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: {
        phase: 'tool_exec',
        turn_id: 1,
        iteration: 1,
        seq: 2,
        chat_id: 'chat-1',
        content: '我需要确认一下你的选择',
        reasoning: '用户需要做决定',
        completed_tools: [{ name: 'AskUser', label: 'AskUser', status: 'done', iteration: 1 }],
        // 无 iteration_history —— AskUser WaitingUser 场景
      },
    })
    // getSnapshot() 是 RAF-throttled：等 mutation flush 到 snapshot，否则
    // commitLiveProgressAndReset 读到空快照 → hasVisibleProgress=false → 不 commit。
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 用户回答 → 新 turn 开始 → commitLiveProgressAndReset 提交 turn 1。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user', content: '选 A' }, chat_id: 'chat-1' },
    })
    await waitFor(() => expect(completeArgs).toBeDefined())
    // 顶层 content 被 fold 进迭代（commitText 清空）；迭代保留 content + reasoning。
    expect(completeArgs![0]).toBe('')
    expect(completeArgs![1]).toHaveLength(1)
    expect(completeArgs![1][0].content).toBe('我需要确认一下你的选择')
    expect(completeArgs![1][0].reasoning).toBe('用户需要做决定')
  })

  it('cancel ack 后新 turn 的 stream_content 不被 finalized/phaseDone guard 卡死（SSE 更新但前端卡死回归）', async () => {
    // 用户报告："sse在dev tool里显示一直更新，但是web前端进度卡死"。
    // 根因：cancel ack（text cancelled）无条件设置 finalizedRef/phaseDoneRef=true。
    // 若 turn_started 丢失（SSE gap），新 turn 的 stream_content 到达时：
    //   - stream_content 分支 line 853/859 在 turn_id 检查之前 `if (finalizedRef?
    //     .current) return` 无条件拦截 → 新 turn 流内容永不渲染。
    //   - progress_structured 分支 line 1169 `if (phaseDoneRef?.current)` 只保留
    //     todos → 新 turn 迭代/工具也永不渲染。
    // 修复：finalized/phaseDone guard 必须带 turn_id 条件 —— 只拦截旧 turn
    // （turn_id <= finalizedTurnID），新 turn（turn_id 更大）放行。
    const ms = new MessageStore()
    let onMessageCb: ((msg: unknown) => void) | undefined
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
      }),
    )
    // Turn 1: turn_started + 一个结构化事件。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'hi' }, chat_id: 'chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', turn_id: 1, iteration: 1, seq: 2, chat_id: 'chat-1', active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }] },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // cancel ack（turn 1）。
    onMessageCb?.({ type: 'text', cancelled: true, turn_id: 1, chat_id: 'chat-1' })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // turn_started(2) 丢失（SSE gap）—— 新 turn 的第一个 stream_content 直接到达。
    onMessageCb?.({
      type: 'stream_content',
      progress: { turn_id: 2, iteration: 1, stream_content: '新 turn 流式内容' },
    })
    // 修复后：新 turn 的流内容必须写入 MessageStore live（turn 2）。
    await waitFor(() => {
      const live = ms.getLive(2)
      expect(live).toBeDefined()
      expect(live?.content).toContain('新 turn 流式内容')
    })
  })

  it('cancel ack 后新 turn 的 progress_structured 不被 phaseDoneRef 卡死（SSE 更新但前端卡死回归）', async () => {
    const ms = new MessageStore()
    let onMessageCb: ((msg: unknown) => void) | undefined
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
      }),
    )
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'hi' }, chat_id: 'chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', turn_id: 1, iteration: 1, seq: 2, chat_id: 'chat-1', active_tools: [{ name: 'Shell', status: 'running', iteration: 1 }] },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // cancel ack（turn 1）。
    onMessageCb?.({ type: 'text', cancelled: true, turn_id: 1, chat_id: 'chat-1' })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // turn_started(2) 丢失 —— 新 turn 的第一个 progress_structured 直接到达。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', turn_id: 2, iteration: 1, seq: 5, chat_id: 'chat-1', active_tools: [{ name: 'Grep', status: 'running', iteration: 1 }] },
    })
    // 修复后：新 turn 的工具必须写入 MessageStore live（turn 2）。
    await waitFor(() => {
      const live = ms.getLive(2)
      expect(live).toBeDefined()
      expect(live?.activeTools?.map((t) => t.name)).toContain('Grep')
    })
  })
})
