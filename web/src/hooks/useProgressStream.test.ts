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

  it('切换会话后 finalizedTurnIDRef 必须重置 —— 否则新会话所有事件被旧会话 turn_id 拦截（SSE 更新但前端卡死回归）', async () => {
    // 用户报告："侧边栏切换会话后，dev tool 里 SSE 一直在更新进度，但是前端
    // 卡死在特定 stream 进度（可能思考内容输出了一半，然后卡死）。gap 检测
    // 追赶逻辑为何不生效？"
    // 根因：turn_id 是【每会话独立】计数器（agent.go ss.turnIDSeq，DB 恢复）。
    // 会话 A 的 turn_id 可能已到 50（finalizedTurnIDRef=50）。切换会话 B 后
    // useProgressStream 的 chatID-change effect 只重置 finalizedRef/
    // phaseDoneRef/turnCommittedRef，【漏了 finalizedTurnIDRef】→ 残留 50。
    // 会话 B 的 turn_id 从 1 开始 → 所有事件 streamTurnID=1,2,...<=50 被
    // stream_content/progress_structured 分支的 finalizedTurnID 检查无条件
    // 丢弃 → SSE 持续到达（dev tool 可见）但前端 store 永不更新（卡死）。
    // gap 检测不生效：事件根本没进 store，迭代号从不前进，gap 永不触发。
    const ms = new MessageStore()
    let onMessageCb: ((msg: unknown) => void) | undefined
    const ws = {
      onMessage: (cb: (msg: unknown) => void) => { onMessageCb = cb; return () => {} },
      rpc: vi.fn(),
      send: vi.fn(),
      onConnectionChange: () => () => {},
      connected: false,
    } as unknown as WSConnection & { onMessage: (cb: (msg: unknown) => void) => () => void }
    const { rerender } = renderHook(
      ({ chatID }: { chatID: string }) =>
        useProgressStream({ chatID, ws, messageStore: ms }),
      { initialProps: { chatID: 'chat-A' } },
    )
    // 会话 A：turn_id 推到 50（PhaseDone 设置 finalizedTurnIDRef=50）。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 50, turn_start: { trigger: 'user', content: 'hi' }, chat_id: 'chat-A' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'done', turn_id: 50, iteration: 1, seq: 2, chat_id: 'chat-A' },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 切换会话 B（turn_id 从 1 开始）。
    rerender({ chatID: 'chat-B' })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 会话 B：turn_started + stream_content。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'hello' }, chat_id: 'chat-B' },
    })
    onMessageCb?.({
      type: 'stream_content',
      progress: { turn_id: 1, iteration: 1, stream_content: '新会话的思考内容' },
    })
    // 修复后：会话 B 的 stream_content 必须写入 store（live content 更新）。
    await waitFor(() => {
      const live = ms.getLive(1)
      expect(live).toBeDefined()
      expect(live?.content).toContain('新会话的思考内容')
    })
  })

  it('发送 user msg 后，上一个 turn 最后迭代 content 不丢失（迟到 text 权威 finalizer 不被 finalizedTurnID 拦截）', async () => {
    // P0 回归：用户报告"发送 user msg 之后，上一个 agent turn 最后一个迭代的
    // content 消失，刷新后正常"。
    // 根因链：
    //  1. turn 1 正常结束，text 事件（权威 finalizer，携带最终回复 content +
    //     progress_history）在 SSE 上【迟到】（尚未到达前端）。
    //  2. 用户发送新 user msg → turn_started(2) 到达 → beginTurn →
    //     commitStaleLives 用不完整 live（text 未到，最后迭代 content 为空）提前
    //     commit turn 1；turn_started 分支设 finalizedTurnIDRef = 1。
    //  3. 迟到的 text(turn_id=1) 到达 → line 1495 `effTextTurnID(1) <=
    //     finalizedTurnID(1)` → return 丢弃 —— 权威 content 永久丢失。
    //  4. 刷新后 DB 权威数据恢复 → "刷新后正常"。
    // 修复：finalizedTurnID 拦截只对【严格更早】turn 的迟到 text（`<`）生效；
    //  当前/未 finalize turn 的迟到 text 放行（commitAssistant 按 turnID 幂等
    //  覆盖 slot，不产生第二行 —— 见 AGENTS.md "旧 turn 的迟到 text 由
    //  MessageStore 的 slot 唯一性保证幂等覆盖"）。
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
        onAssistantComplete: (content, iterations, _eventSeq, turnID) => {
          ms.commitAssistant(turnID ?? 0, content, iterations)
        },
      }),
    )
    // turn 1：turn_started + 结构化事件（最后迭代 content 尚未由 text 填充）。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'hi' }, chat_id: 'chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', turn_id: 1, iteration: 1, seq: 2, chat_id: 'chat-1', active_tools: [{ name: 'Shell', status: 'done', iteration: 1 }] },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 用户发送新消息 → turn_started(2)（turn 1 的 text 尚未到达）。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 2, turn_start: { trigger: 'user', content: '继续' }, chat_id: 'chat-1' },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // turn 1 的 text（权威 finalizer）迟到到达：携带最终回复 content + progress_history。
    onMessageCb?.({
      type: 'text',
      content: '这是最终回复的完整内容',
      progress_history: JSON.stringify([
        {
          iteration: 1,
          phase: 'done',
          content: '这是最终回复的完整内容',
          reasoning: '',
          tools: [{ name: 'Shell', label: 'Shell', status: 'done', elapsed_ms: 10, iteration: 1 }],
        },
      ]),
      turn_id: 1,
      chat_id: 'chat-1',
    })
    // 修复后：turn 1 的 committed assistant 最后迭代 content 必须保留最终回复
    // （迟到 text 未被 finalizedTurnID 拦截）。
    await waitFor(() => {
      const rows = ms.toRows()
      const turn1 = rows.find((r) => r.turnID === 1 && r.role === 'assistant')
      expect(turn1).toBeDefined()
      const iters = turn1?.iterations ?? []
      expect(iters.length).toBeGreaterThan(0)
      expect(iters[iters.length - 1].content).toContain('这是最终回复的完整内容')
    })
  })

  it('PhaseDone 必须把后端附带的 iteration_history（最后迭代）写入 store —— 否则最后 iter 消失再出现（闪烁回归）', async () => {
    // 用户报告："最后一个 iter 结束之后，最后一个 iter 会消失再出现造成闪烁。
    // iter 产生了就不要消失"。
    // 根因链：
    //  1. 最后一个迭代完成后，后端 attachIterationDelta 只在【推进到下一迭代】
    //     时记录前一个迭代；最后一个迭代没有"下一迭代"，所以它从不通过普通
    //     结构化事件进入 IterationHistory。
    //  2. 后端在 PhaseDone 时 recordFinalIteration 补记，并把它 attach 到
    //     PhaseDone 事件的 iteration_history（engine_wire.go:1908-1918）。
    //  3. 前端 PhaseDone 分支只调 store.stopStreaming() + 处理 todos，
    //     【从不把 p.iteration_history 传给 setStructuredTools】→ 最后一个
    //     迭代从未进入 store.iterationHistory → live 渲染从 UI 消失。
    //  4. text 事件到达 → completeRef 用 parseWebIterations(progress_history)
    //     重建 → 最后一个迭代重新出现 → 闪烁。
    // 修复：PhaseDone 分支把 p.iteration_history 传给 store.setStructuredTools。
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
    // turn 1：单个迭代（最后迭代）完成。
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'turn_started', turn_id: 1, turn_start: { trigger: 'user', content: 'hi' }, chat_id: 'chat-1' },
    })
    onMessageCb?.({
      type: 'progress_structured',
      progress: { phase: 'tool_exec', turn_id: 1, iteration: 1, seq: 2, chat_id: 'chat-1', content: '最后回复内容' },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // PhaseDone 携带最后迭代快照（后端 recordFinalIteration attach）。
    onMessageCb?.({
      type: 'progress_structured',
      progress: {
        phase: 'done',
        turn_id: 1,
        iteration: 1,
        seq: 3,
        chat_id: 'chat-1',
        iteration_history: [
          {
            iteration: 1,
            phase: 'done',
            content: '最后回复内容',
            reasoning: '',
            tools: [],
          },
        ],
      },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 修复后：store.iterationHistory 必须包含最后迭代（iteration 1）——
    // 否则 live 渲染中最后 iter 消失（闪烁）。
    const snap = ms.getLive(1)
    // 通过 toRows 断言 committed/live 行保留了最后迭代 content。
    const rows = ms.toRows()
    const turn1 = rows.find((r) => r.turnID === 1 && r.role === 'assistant')
    const liveIters = snap?.iterations ?? []
    const rowIters = turn1?.iterations ?? []
    const hasLastIter = (iters: Array<{ iteration?: number; content?: string }>) =>
      iters.some((it) => it.iteration === 1 && String(it.content ?? '').includes('最后回复内容'))
    expect(hasLastIter(liveIters) || hasLastIter(rowIters)).toBe(true)
  })

  it('PhaseDone iteration_history 必须走 normalize —— Go nil slice 序列化为 null 的 tools 直塞 store 会让渲染层 .map() 崩溃（整页 DOM 消失回归）', async () => {
    // 用户报告："cancel 会话或者 agent turn 结束，整个 web 页面所有 dom 消失"。
    // 根因：PhaseDone 分支曾用 `p.iteration_history as WebIteration[]` 类型断言
    // 直塞 store（跳过 normalizeWebIteration）。后端 Go 的 nil slice 序列化为
    // JSON null —— iteration_history[].tools 可能为 null。渲染层对 null 调
    // .map() 抛 TypeError → React commit 阶段同步错误 → 整树卸载 → 页面空白。
    // 修复：与普通结构化事件一致，走 .map(normalizeWebIteration).filter(Boolean)。
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
    // PhaseDone 携带的最后迭代快照 —— tools 为 null（Go nil slice 的 JSON 序列化）。
    onMessageCb?.({
      type: 'progress_structured',
      progress: {
        phase: 'done',
        turn_id: 1,
        iteration: 1,
        seq: 3,
        chat_id: 'chat-1',
        iteration_history: [
          {
            iteration: 1,
            phase: 'done',
            content: '最后回复内容',
            reasoning: null,
            tools: null,
          },
        ],
      },
    })
    await new Promise<void>((r) => requestAnimationFrame(() => r()))
    // 修复后：normalize 把 null tools 转为 []（渲染层 .map() 不崩），content 保留。
    const snap = ms.getLive(1)
    const iters = snap?.iterations ?? []
    expect(iters.length).toBeGreaterThan(0)
    expect(Array.isArray(iters[0].tools)).toBe(true)
    expect(iters[0].tools).toEqual([])
    // toRows()（渲染层数据源）也不能抛错 —— 模拟渲染读取路径。
    expect(() => ms.toRows()).not.toThrow()
  })
})
