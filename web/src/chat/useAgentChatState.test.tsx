/**
 * useAgentChatState.test.tsx — hook 级全链路集成测试（真实 ChatStore +
 * normalize + reduce + derive，mock ws.onMessage 注入原始 SSE 形状事件）。
 *
 * 场景对应用户报告：
 *  A. 打字机：echo → turn_started → stream×N 逐增（含无 turn_id / 低 seq 容错）
 *  B. turn 结束：phase_done + text → committed（迭代+content 保全）
 *  C. 发新消息：history_replaced 不含 turn41 → committed 不消失
 *  D. echo 先于 turn_started（时序颠倒）→ live 不被打断
 *  E. 切会话属主门控：旧会话 messages（resolvedChatID≠chatID）不灌入新 store
 */

import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { ChatMessage } from '@/types/shared'
import { useAgentChatState } from './useAgentChatState'

function makeWS() {
  let cb: ((raw: unknown) => void) | undefined
  return {
    onMessage: (fn: (raw: unknown) => void) => {
      cb = fn
      return () => { cb = undefined }
    },
    emit: (raw: unknown) => { cb?.(raw) },
  }
}

function mountHook(ws: ReturnType<typeof makeWS>, progressChatID = 'chat-1') {
  let messages: readonly ChatMessage[] = []
  let historyReady = false
  let historyOwner: string | null = null
  let activeProgress: unknown = null
  const api = renderHook(() =>
    useAgentChatState({
      progressChatID,
      ws: ws as never,
      historyMessages: messages,
      historyReady,
      historyOwner,
      historyChatID: 'chat-1',
      initialProgress: activeProgress,
      resetKey: 'k',
    }),
  )
  return {
    ...api,
    setHistory(next: readonly ChatMessage[], owner: string | null = 'chat-1', ready = true) {
      messages = next
      historyOwner = owner
      historyReady = ready
      api.rerender()
    },
    rerenderWithActiveProgress(ap: unknown) {
      activeProgress = ap
      api.rerender()
    },
  }
}

const histMsg = (over: Partial<ChatMessage>): ChatMessage => ({
  id: 'm1', role: 'user', content: '历史消息', iterations: [], timestamp: 't',
  isPartial: false, turnID: 0, ...over,
})

describe('useAgentChatState 全链路', () => {
  it('A+B: 打字机逐增 → text 后 committed，迭代与最终 content 保全', async () => {
    const ws = makeWS()
    const h = mountHook(ws)

    h.setHistory([histMsg({})])
    await waitFor(() => expect(h.result.current.messages.some((m) => m.content === '历史消息')).toBe(true))

    ws.emit({ type: 'user_echo', content: '帮我写个报告', turn_id: 41, chat_id: 'chat-1' })
    ws.emit({ type: 'session', session: { action: 'busy' }, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 41, seq: 1, turn_start: { trigger: 'user', content: '帮我写个报告' } }, chat_id: 'chat-1' })

    // 打字机：3 帧递增（rAF flush 后断言）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('第一'))
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一第二' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('第一第二'))
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一第二第三' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('第一第二第三'))

    // 迭代快照 + PhaseDone 附最后迭代（tools:null —— Go nil slice 容错）。
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 41, iteration: 1, seq: 2, iteration_history: [{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: null }] }, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'done', turn_id: 41, iteration: 1, seq: 3, iteration_history: [{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: [] }] }, chat_id: 'chat-1' })
    ws.emit({ type: 'text', content: '第一第二第三（最终）', turn_id: 41, chat_id: 'chat-1', progress_history: JSON.stringify([{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: [] }]) })
    ws.emit({ type: 'session', session: { action: 'idle' }, chat_id: 'chat-1' })

    await waitFor(() => {
      const committed = h.result.current.messages.find((m) => !m.isPartial && m.role === 'assistant' && m.turnID === 41)
      expect(committed?.content).toContain('（最终）')
      expect(committed?.iterations).toHaveLength(1)
    })
    expect(h.result.current.messages.find((m) => m.isPartial && m.turnID === 41)).toBeUndefined()
  })

  it('A2: stream 事件【无 turn_id】仍喂 active turn（打字机容错 —— 后端已知 gap）', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 9, seq: 1 }, chat_id: 'chat-1' })
    // 无 turn_id 的 stream —— 不得被丢弃。
    ws.emit({ type: 'stream_content', progress: { iteration: 1, stream_content: '无 turn_id 的流式' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('无 turn_id 的流式'))
  })

  it('A4: 真实 SSE 形状 —— progress_structured + phase 缺失 + stream_content 字段 = 打字机帧（不得误判为 iteration）', async () => {
    // Web channel 把所有 ProgressEvent 转发为 type='progress_structured'（无独立
    // stream_content 消息类型）。打字机帧的 phase 为空、载荷在 stream_content
    // 字段。误判为 iteration 会丢弃 stream_content → 打字机死掉（用户报告）。
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 33, seq: 1 }, chat_id: 'chat-1' })
    // 三帧真实形状的打字机事件。
    ws.emit({ type: 'progress_structured', progress: { turn_id: 33, iteration: 1, seq: 2, stream_content: '真实形状第' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('真实形状第'))
    ws.emit({ type: 'progress_structured', progress: { turn_id: 33, iteration: 1, seq: 3, stream_content: '真实形状第一' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('真实形状第一'))
    ws.emit({ type: 'progress_structured', progress: { turn_id: 33, iteration: 1, seq: 4, reasoning_stream_content: '思考流式' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.reasoningStreamContent).toContain('思考流式'))
    // turn 仍 live（未误入 committed/frozen）。
    expect(h.result.current.liveProgress.turnID).toBe(33)
  })

  it('A3: stream 低 seq 不被 gate 误杀（累积全量不按 seq 排序 —— 旧前端语义）', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 9, seq: 1 }, chat_id: 'chat-1' })
    // 结构化事件推进 lastSeq=50。
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 9, iteration: 1, seq: 50, active_tools: [] }, chat_id: 'chat-1' })
    // 低 seq（7 < 50）的 stream 全量帧 —— 必须应用（打字机帧）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 9, iteration: 1, seq: 7, stream_content: '低 seq 但内容更新' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('低 seq 但内容更新'))
  })

  it('C: 发新 user msg（history_replaced 不含 turn41）→ 上一 turn agent 消息不消失', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'user_echo', content: '问题1', turn_id: 41, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 41, seq: 1 }, chat_id: 'chat-1' })
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: 'turn41 的回答' }, chat_id: 'chat-1' })
    ws.emit({ type: 'text', content: 'turn41 的回答', turn_id: 41, chat_id: 'chat-1', progress_history: '[]' })
    await waitFor(() => expect(h.result.current.messages.some((m) => m.role === 'assistant' && m.turnID === 41 && !m.isPartial)).toBe(true))

    // 发第二条消息：echo → messages 变化（history_replaced 不含 turn41）。
    ws.emit({ type: 'user_echo', content: '问题2', turn_id: 42, chat_id: 'chat-1' })
    act(() => h.setHistory([histMsg({ id: 'u41', content: '问题1', turnID: 41 }), histMsg({ id: 'u42', content: '问题2', turnID: 42 })]))
    expect(h.result.current.messages.some((m) => m.role === 'assistant' && m.turnID === 41 && !m.isPartial)).toBe(true)
  })

  it('D: echo 先于 turn_started（时序颠倒）→ live turn 不被 history_replaced 打断', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'user_echo', content: 'hi', turn_id: 7, chat_id: 'chat-1' })
    act(() => h.setHistory([histMsg({ id: 'u7', content: 'hi', turnID: 7 })]))
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 7, seq: 1 }, chat_id: 'chat-1' })
    ws.emit({ type: 'stream_content', progress: { turn_id: 7, iteration: 1, stream_content: '流式内容A' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('流式内容A'))
    // echo 再触发 messages 变化 → live 仍活，流式续接。
    act(() => h.setHistory([histMsg({ id: 'u7', content: 'hi', turnID: 7 }), histMsg({ id: 'u7b', content: 'hi', turnID: 7 })]))
    ws.emit({ type: 'stream_content', progress: { turn_id: 7, iteration: 1, stream_content: '流式内容AB' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('流式内容AB'))
  })

  it('E: 切会话属主门控 —— resolvedChatID ≠ chatID 的旧 messages 不灌入（跨会话污染根因）', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    // 切换窗口：historyReady=true 但 resolvedChatID 还是旧会话 'chat-OLD'。
    act(() => h.setHistory(
      [histMsg({ id: 'old1', content: '旧会话的消息' })],
      'chat-OLD',
    ))
    // 新 store 保持空 —— 旧会话消息不得出现。
    expect(h.result.current.messages.some((m) => m.content === '旧会话的消息')).toBe(false)
    // fetch 完成：属主变为当前 chat → 正常灌入。
    act(() => h.setHistory([histMsg({ id: 'new1', content: '新会话的消息' })], 'chat-1'))
    await waitFor(() => expect(h.result.current.messages.some((m) => m.content === '新会话的消息')).toBe(true))
    expect(h.result.current.messages.some((m) => m.content === '旧会话的消息')).toBe(false)
  })

  it('F: 切回会话（store 空、turn_started 已过）→ stream/iteration 事件 lazy 重建 live turn → 进度可见', async () => {
    // 用户报告："发消息后能看到打字机；切换会话切回来，看不到任何新进度"。
    // 根因：切回后新 store 空 + turn_started 不会再发 + active_progress 恢复
    // 失败/未完成时，stream 事件 turns.get(57)=undefined 被永久丢弃。
    // lazy 采纳：事件带 turnID、无该槽、无 active → 重建 live turn。
    const ws = makeWS()
    const h = mountHook(ws)
    // 无 turn_started（它属于切换前）—— stream 直接到达。
    ws.emit({ type: 'stream_content', progress: { turn_id: 57, iteration: 2, stream_content: '切回后的流式进度' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('切回后的流式进度'))
    expect(h.result.current.liveProgress.turnID).toBe(57)
    // 后续 stream 继续喂养（打字机持续）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 57, iteration: 2, stream_content: '切回后的流式进度（继续）' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('（继续）'))
    // iteration 事件（同 turn）正常处理；text 收尾完整。
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 57, iteration: 2, seq: 9, iteration_history: [{ iteration: 2, content: '切回后的流式进度（继续）', reasoning: '', tools: [] }] }, chat_id: 'chat-1' })
    ws.emit({ type: 'text', content: '切回后 turn 的最终回复', turn_id: 57, chat_id: 'chat-1', progress_history: '[]' })
    await waitFor(() => {
      const committed = h.result.current.messages.find((m) => !m.isPartial && m.role === 'assistant' && m.turnID === 57)
      expect(committed?.content).toBe('切回后 turn 的最终回复')
    })
  })

  it('F2: lazy 采纳不与既有活动 turn 竞争（旧 turn 迟到事件仍丢弃）', async () => {
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 8, seq: 1 }, chat_id: 'chat-1' })
    ws.emit({ type: 'stream_content', progress: { turn_id: 8, iteration: 1, stream_content: 'turn8 流式' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('turn8 流式'))
    // 旧 turn 5 的迟到事件（有 active turn 8）→ 丢弃，不采纳、不打断。
    ws.emit({ type: 'stream_content', progress: { turn_id: 5, iteration: 1, stream_content: '旧 turn5 迟到' }, chat_id: 'chat-1' })
    expect(h.result.current.liveProgress.streamContent).toContain('turn8 流式')
    expect(h.result.current.liveProgress.turnID).toBe(8)
  })

  it('G: 工具长时间执行（零新 SSE 事件）→ active_progress 快照恢复 live（含运行中工具）', async () => {    // 用户报告："live 恢复现在靠新 sse 事件，但工具执行会卡非常久，用户很久
    // 看不到 live"。恢复必须依赖【active_progress 快照】（后端
    // lastProgressSnapshot 每事件更新、工具执行期间存活），不依赖新事件。
    // 场景：切回/刷新 → fetchHistory 返回 user 行（turn 57）+ active_progress
    // 声明 turn 57 正在执行 Shell（active_tools running）→ 无任何 SSE 事件
    // 到达 → live 行必须立即可见（工具 + 已完成迭代 + 流式 content）。
    const ws = makeWS()
    const h = mountHook(ws)
    act(() => h.setHistory(
      [histMsg({ id: 'u57', role: 'user', content: '跑个长任务', turnID: 57 })],
      'chat-1',
    ))
    await act(async () => {
      h.rerenderWithActiveProgress?.({
        turn_id: 57,
        phase: 'tool_exec',
        iteration: 3,
        seq: 120,
        stream_content: '',
        active_tools: [{ name: 'Shell', label: 'Shell', status: 'running', elapsed_ms: 42000, iteration: 3 }],
        completed_tools: [],
        iteration_history: [
          { iteration: 1, content: '迭代1输出', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 2, content: '迭代2输出', reasoning: '', tools: [], toolCount: 0 },
        ],
      })
    })
    // 无任何 SSE 事件 —— live 必须从快照恢复。
    await waitFor(() => {
      expect(h.result.current.liveProgress.turnID).toBe(57)
      expect(h.result.current.liveProgress.activeTools.map((t) => t.name)).toContain('Shell')
      expect(h.result.current.liveProgress.iterationHistory).toHaveLength(2)
    })
    // 渲染行：user + live（isPartial）。
    expect(h.result.current.messages.some((m) => m.isPartial && m.turnID === 57)).toBe(true)
    // 工具跑完后的下一个 SSE 事件正常接续（无 lazy 冲突）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 57, iteration: 4, stream_content: '工具完成后的流式' }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toContain('工具完成后的流式'))
  })

  it('H: 同一工具不得双渲染 —— generating 残留随 running 到达清除（100% 复现回归）', async () => {    // 用户报告："一个执行中 tool 会同时渲染两个 tool，一个有参数（executing）
    // 一个没参数（generating）"。根因：streamingTools（流式检测中，generating，
    // 参数不全）与 activeTools（结构化事件，running，参数全）同名共存。
    // 旧前端在 mergeProgressState 里做名字过滤 —— 新状态机在 reduce 层
    // 维护 streamingTools ∩ activeTools = ∅。
    const ws = makeWS()
    const h = mountHook(ws)
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 12, seq: 1 }, chat_id: 'chat-1' })
    // 1. 流式阶段：工具名已检测（generating，参数生成中）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 12, iteration: 1, seq: 2, stream_content: '', streaming_tools: [{ name: 'Shell', label: '', status: 'generating', iteration: 1 }] }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.streamingTools.map((t) => t.name)).toContain('Shell'))
    // 2. 参数生成完 → 结构化事件：Shell 进入 activeTools（running，带参数）。
    //    streamingTools 里的同名 generating 残留必须被清除。
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 12, iteration: 1, seq: 3, active_tools: [{ name: 'Shell', label: 'Shell', status: 'running', elapsed_ms: 5, iteration: 1, args: 'ls -la' }] }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.activeTools.map((t) => t.name)).toContain('Shell'))
    // 双渲染断言：Shell 只出现一次（activeTools），streamingTools 无同名。
    expect(h.result.current.liveProgress.streamingTools.some((t) => t.name === 'Shell')).toBe(false)
    // 3. 反向时序：activeTools 已 running，迟到的 stream 帧仍带 generating
    //    同名 → 必须被过滤。
    ws.emit({ type: 'stream_content', progress: { turn_id: 12, iteration: 1, seq: 4, stream_content: '', streaming_tools: [{ name: 'Shell', label: '', status: 'generating', iteration: 1 }] }, chat_id: 'chat-1' })
    expect(h.result.current.liveProgress.streamingTools.some((t) => t.name === 'Shell')).toBe(false)
    expect(h.result.current.liveProgress.activeTools).toHaveLength(1)
  })

  it('I: 切换会话竞态 —— lazy 采纳（仅切换后 delta）后 active_progress 快照 union 补全全部迭代', async () => {
    // 用户报告："切换会话有概率最新 turn 只渲染最后一两个 live iter"。
    // 根因：push 协议每事件只携带【新完成】的 0-1 个迭代；切换后 SSE delta
    // 先到（lazy 采纳，只含切换后 1-2 个迭代）→ fetchHistory 的 active_progress
    // 快照携带【完整】iterationHistory → merge step 3 只在"无 live"时使用
    // 快照 → live 胜出时快照迭代被整体丢弃 → 只渲染最后一两个 iter。
    // 修复：merge step 3.5 —— ev.active 与保留 live 同 ID 时 union 快照迭代。
    const ws = makeWS()
    const h = mountHook(ws)
    // 切换后：turn_started 已过 → lazy 采纳 + 迭代 delta（仅 iter 4、5 到达）。
    ws.emit({ type: 'stream_content', progress: { turn_id: 33, iteration: 4, stream_content: 'iter4 流式' }, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 33, iteration: 5, seq: 60, iteration_history: [{ iteration: 5, content: 'iter5', reasoning: '', tools: [] }] }, chat_id: 'chat-1' })
    await waitFor(() => expect(h.result.current.liveProgress.turnID).toBe(33))
    // fetchHistory 完成：user 行（turn 33）+ active_progress 快照（完整 iter 1-5）。
    act(() => h.setHistory([histMsg({ id: 'u33', role: 'user', content: '长任务', turnID: 33 })]))
    await act(async () => {
      h.rerenderWithActiveProgress?.({
        turn_id: 33,
        phase: 'tool_exec',
        iteration: 5,
        seq: 55,
        stream_content: 'iter5 流式',
        active_tools: [],
        completed_tools: [],
        iteration_history: [
          { iteration: 1, content: 'iter1', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 2, content: 'iter2', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 3, content: 'iter3', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 4, content: 'iter4', reasoning: '', tools: [], toolCount: 0 },
          { iteration: 5, content: 'iter5', reasoning: '', tools: [], toolCount: 0 },
        ],
      })
    })
    // live 胜出（SSE 流式内容保留）+ 快照迭代 union 补全 —— 全部 5 个迭代可见。
    // dispatch 经 rAF 合并通知 → 断言用 waitFor（同步读会拿到旧快照）。
    await waitFor(() => expect(h.result.current.liveProgress.turnID).toBe(33))
    await waitFor(() => expect(h.result.current.liveProgress.streamContent).toBe('iter5 流式'))
    await waitFor(() =>
      expect(h.result.current.liveProgress.iterationHistory.map((i) => i.iteration)).toEqual([1, 2, 3, 4, 5]),
    )
  })
})
