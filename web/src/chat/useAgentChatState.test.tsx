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
  const api = renderHook(() =>
    useAgentChatState({
      progressChatID,
      ws: ws as never,
      historyMessages: messages,
      historyReady,
      historyOwner,
      historyChatID: 'chat-1',
      initialProgress: null,
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
})
