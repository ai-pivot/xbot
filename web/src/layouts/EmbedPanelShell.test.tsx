/**
 * Tests for EmbedPanelShell —— **宿主嵌入态的实时通道**（2026-10-14 修）。
 *
 * 背景（根因）：嵌入态（`?xbot_panel=<id>&xbot_embed=1`）只渲面板本体、**不挂 `AgentPanel`**，
 * 而 SSE 订阅的唯一建立点是 `hooks/useActiveSSESubscription`（此前只有 `AgentPanel` 一个消费方）
 * ⇒ 无人 `addSubscription` ⇒ `MultiSSEManager.connect()` 因 `!chatID` 直接 return
 *（`providers/sseConnection.ts:200-202`）⇒ `ws.connected` 恒 `false`
 * ⇒ 任务/终端/文件面板只吃打开那一刻的 REST 快照（`TasksPanel` 因此显示
 * `sidebar.disconnectedHint`「实时连接已断开，状态可能延迟」）。
 *
 * 判据（有判别力）：
 *  ① 有活跃会话 ⇒ 本组件**必须**发起 SSE 订阅，且 chatID/channel 取自该会话；
 *  ② 无活跃会话 ⇒ **不得**订阅（不许拿空 chatID 去订阅）。
 * mutation 自证：删掉组件里的 `useActiveSSESubscription({...})` 调用 ⇒ ① 必红（② 仍绿 ⇒ 说明判据不是恒真）。
 */
import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

const addSubscription = vi.fn((chatID: string, channel: string) => `sub:${chatID}:${channel}`)
const removeSubscription = vi.fn()
const fakeWS = {
  addSubscription,
  removeSubscription,
  connected: false,
  chatID: null as string | null,
  channel: null as string | null,
}

vi.mock('@/hooks/useWSConnection', () => ({ useWSConnection: () => fakeWS }))

const sessionState: { activeSession: { channel: string; chatID: string } | null } = {
  activeSession: { channel: 'cli', chatID: 'chat-1' },
}
vi.mock('@/hooks/useSessionStore', () => ({ useSessionStore: () => sessionState }))

// 面板本体 / 插件运行时 / 宿主桥：本判据只关心"订阅接线"，这些在单测里不加载。
vi.mock('@/workspace/panelRender', () => ({ renderPanelBody: () => null }))
vi.mock('@/plugin-runtime/usePluginViewPanels', () => ({ usePluginViewPanels: () => [] }))
vi.mock('@/hooks/useTabManager', () => ({ useTabManager: () => ({}) }))
vi.mock('@/hooks/useTerminal', () => ({ useTerminal: () => ({}) }))
vi.mock('@/workspace/hostBridge', () => ({ postHostNav: () => false }))

import { EmbedPanelShell } from '@/layouts/EmbedPanelShell'

describe('EmbedPanelShell 实时通道（嵌入态必须自建 SSE 订阅）', () => {
  it('① 有活跃会话 ⇒ 以该会话的 chatID/channel 发起订阅', () => {
    sessionState.activeSession = { channel: 'cli', chatID: 'chat-1' }
    addSubscription.mockClear()
    render(<EmbedPanelShell panelId="tasks" />)
    expect(addSubscription).toHaveBeenCalledWith('chat-1', 'cli')
  })

  it('② 无活跃会话 ⇒ 不订阅（不得拿空 chatID 订阅）', () => {
    sessionState.activeSession = null
    addSubscription.mockClear()
    render(<EmbedPanelShell panelId="tasks" />)
    expect(addSubscription).not.toHaveBeenCalled()
  })
})
