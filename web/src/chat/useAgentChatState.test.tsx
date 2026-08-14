/**
 * useAgentChatState.test.tsx — hook 级全链路集成测试（真实 ChatStore +
 * normalize + reduce + derive，mock ws.onMessage 注入原始 SSE 形状事件）。
 *
 * 场景对应用户报告：
 *  A. 打字机：user_echo → turn_started → stream_content×N → live 行 content 逐增
 *  B. turn 结束：phase_done + text → committed
 *  C. 发新消息：user_echo(2) + history_replaced（不含 turn1）→ turn1 不消失
 *  D. 首屏：historyReady gate + fetchHistory 后 stream 继续
 */

import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
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

const histMsg = (over: Partial<ChatMessage>): ChatMessage => ({
  id: 'm1', role: 'user', content: '历史消息', iterations: [], timestamp: 't',
  isPartial: false, turnID: 0, ...over,
})

describe('useAgentChatState 全链路', () => {
  it('A+B: 打字机逐增 → text 后 committed，迭代与最终 content 保全', async () => {
    const ws = makeWS()
    let messages: readonly ChatMessage[] = []
    let historyReady = false
    const { result, rerender } = renderHook(() =>
      useAgentChatState({
        progressChatID: 'chat-1',
        ws: ws as never,
        historyMessages: messages,
        historyReady,
        initialProgress: null,
        resetKey: 'k',
      }),
    )

    // fetchHistory 完成：historyReady + 历史注入。
    await act(async () => { historyReady = true; messages = [histMsg({})]; rerender() })
    await waitFor(() => expect(result.current.messages.some((m) => m.content === '历史消息')).toBe(true))

    // user 发消息 → queue-admission echo（带权威 turn_id）。
    ws.emit({ type: 'user_echo', content: '帮我写个报告', turn_id: 41, chat_id: 'chat-1' })
    // busy + turn_started（SSE progress_structured）。
    ws.emit({ type: 'session', session: { action: 'busy' }, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 41, seq: 1, turn_start: { trigger: 'user', content: '帮我写个报告' } }, chat_id: 'chat-1' })

    // 打字机：stream_content 全量累积（3 帧递增）。断言经 rAF flush。
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一' }, chat_id: 'chat-1' })
    await waitFor(() => expect(result.current.liveProgress.streamContent).toContain('第一'))
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一第二' }, chat_id: 'chat-1' })
    await waitFor(() => expect(result.current.liveProgress.streamContent).toContain('第一第二'))
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: '第一第二第三' }, chat_id: 'chat-1' })
    await waitFor(() => expect(result.current.liveProgress.streamContent).toContain('第一第二第三'))

    // 迭代完成 + PhaseDone 附最后迭代。
    ws.emit({ type: 'progress_structured', progress: { phase: 'tool_exec', turn_id: 41, iteration: 1, seq: 2, iteration_history: [{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: null }] }, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'done', turn_id: 41, iteration: 1, seq: 3, iteration_history: [{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: [] }] }, chat_id: 'chat-1' })
    // 权威 text。
    ws.emit({ type: 'text', content: '第一第二第三（最终）', turn_id: 41, chat_id: 'chat-1', progress_history: JSON.stringify([{ iteration: 1, content: '第一第二第三', reasoning: '思考', tools: [] }]) })
    ws.emit({ type: 'session', session: { action: 'idle' }, chat_id: 'chat-1' })

    await waitFor(() => {
      const committed = result.current.messages.find((m) => !m.isPartial && m.role === 'assistant' && m.turnID === 41)
      expect(committed?.content).toContain('（最终）')
      expect(committed?.iterations).toHaveLength(1)
    })
    // live 行已收尾（不再有 turn 41 的 isPartial 行）。
    expect(result.current.messages.find((m) => m.isPartial && m.turnID === 41)).toBeUndefined()
  })

  it('C: 发新 user msg（history_replaced 不含 turn41）→ 上一 turn agent 消息不消失', async () => {
    const ws = makeWS()
    let messages: readonly ChatMessage[] = []
    let historyReady = false
    const { result, rerender } = renderHook(() =>
      useAgentChatState({
        progressChatID: 'chat-1',
        ws: ws as never,
        historyMessages: messages,
        historyReady,
        initialProgress: null,
        resetKey: 'k',
      }),
    )
    // turn 41 完整生命周期（echo → started → stream → text）。
    ws.emit({ type: 'user_echo', content: '问题1', turn_id: 41, chat_id: 'chat-1' })
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 41, seq: 1 }, chat_id: 'chat-1' })
    ws.emit({ type: 'stream_content', progress: { turn_id: 41, iteration: 1, stream_content: 'turn41 的回答' }, chat_id: 'chat-1' })
    ws.emit({ type: 'text', content: 'turn41 的回答', turn_id: 41, chat_id: 'chat-1', progress_history: '[]' })
    await waitFor(() => expect(result.current.messages.some((m) => m.role === 'assistant' && m.turnID === 41 && !m.isPartial)).toBe(true))

    // 用户发第二条消息：echo（带 turn 42）→ messages 变化（echo 入列）→
    // history_replaced 到达且【不含 turn 41】（fetchHistory 快照里还没有）。
    await act(async () => {
      historyReady = true
      messages = [
        histMsg({ id: 'u41', content: '问题1', turnID: 41 }),
      ]
      rerender()
    })
    ws.emit({ type: 'user_echo', content: '问题2', turn_id: 42, chat_id: 'chat-1' })
    await act(async () => {
      messages = [histMsg({ id: 'u41', content: '问题1', turnID: 41 }), histMsg({ id: 'u42', content: '问题2', turnID: 42 })]
      rerender()
    })
    // ⚠️ 修复点：turn 41 的 committed assistant 必须仍在渲染输出里。
    expect(result.current.messages.some((m) => m.role === 'assistant' && m.turnID === 41 && !m.isPartial)).toBe(true)
  })

  it('D: echo 先于 turn_started（时序颠倒）→ live turn 不被 history_replaced 打断', async () => {
    const ws = makeWS()
    let messages: readonly ChatMessage[] = []
    let historyReady = true
    const { result, rerender } = renderHook(() =>
      useAgentChatState({
        progressChatID: 'chat-1',
        ws: ws as never,
        historyMessages: messages,
        historyReady,
        initialProgress: null,
        resetKey: 'k',
      }),
    )
    // echo 先到（messages 变化 → history_replaced），turn_started 后到。
    ws.emit({ type: 'user_echo', content: 'hi', turn_id: 7, chat_id: 'chat-1' })
    await act(async () => { messages = [histMsg({ id: 'u7', content: 'hi', turnID: 7 })]; rerender() })
    ws.emit({ type: 'progress_structured', progress: { phase: 'turn_started', turn_id: 7, seq: 1 }, chat_id: 'chat-1' })
    // stream 持续到达 —— 打字机必须工作（live 存活）。断言经 rAF flush。
    ws.emit({ type: 'stream_content', progress: { turn_id: 7, iteration: 1, stream_content: '流式内容A' }, chat_id: 'chat-1' })
    await waitFor(() => expect(result.current.liveProgress.streamContent).toContain('流式内容A'))
    // echo 再触发一次 messages 变化（第二个 listener 时序）→ live 仍活。
    await act(async () => { messages = [histMsg({ id: 'u7', content: 'hi', turnID: 7 }), histMsg({ id: 'u7b', content: 'hi', turnID: 7 })]; rerender() })
    ws.emit({ type: 'stream_content', progress: { turn_id: 7, iteration: 1, stream_content: '流式内容AB' }, chat_id: 'chat-1' })
    await waitFor(() => expect(result.current.liveProgress.streamContent).toContain('流式内容AB'))
  })
})
