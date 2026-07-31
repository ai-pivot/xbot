import { screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { SessionInfo } from './SessionInfo'
import type { TabManager } from '@/hooks/useTabManager'

const mocks = vi.hoisted(() => ({
  rpc: vi.fn(),
  fetchCwd: vi.fn(),
  fetchSessionSubscription: vi.fn(),
  setCwd: vi.fn(),
  sessionStore: {
    activeSession: { channel: 'cli', chatID: '/repo:Agent-main' },
    activeSessionId: '/repo:Agent-main',
    sessions: [{
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      lastActive: '2026-07-08T00:00:00Z',
      preview: '',
      status: 'idle',
      isCurrent: true,
      children: [{
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        label: 'review/1',
        lastActive: '2026-07-08T00:00:01Z',
        preview: '',
        status: 'idle',
        isCurrent: false,
        type: 'agent',
        role: 'review',
        instance: '1',
        parentChannel: 'cli',
        parentChatID: '/repo:Agent-main',
        fullKey: 'cli:/repo:Agent-main/review:1',
        agentChatID: 'cli:/repo:Agent-main/review:1',
      }],
    }],
  },
}))

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({ connected: true, rpc: mocks.rpc }),
}))

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => mocks.sessionStore,
}))

vi.mock('@/providers/CwdProvider', () => ({
  useCwd: () => ({ cwd: '/repo', loading: false }),
}))

vi.mock('@/components/agent/api', () => ({
  fetchCwd: mocks.fetchCwd,
  fetchSessionSubscription: mocks.fetchSessionSubscription,
  setCwd: mocks.setCwd,
}))

describe('SessionInfo', () => {
  beforeEach(() => {
    mocks.rpc.mockReset()
    mocks.fetchCwd.mockReset()
    mocks.fetchSessionSubscription.mockReset()
    mocks.setCwd.mockReset()
    mocks.fetchCwd.mockImplementation((session: { chatID: string }) => Promise.resolve({ dir: session.chatID === '/repo:Agent-main' ? '/repo' : '/unexpected' }))
    mocks.fetchSessionSubscription.mockImplementation((session: { chatID: string }) => Promise.resolve({ model: session.chatID === 'cli:/repo:Agent-main/review:1' ? 'sub-model' : 'main-model' }))
    mocks.rpc.mockImplementation((method: string) => {
      if (method === 'get_agent_session_dump_by_full_key') return Promise.resolve({
        modelName: 'dump-model',
        subscriptionID: 'sub-1',
        promptTokens: 100,
        completionTokens: 25,
        maxContextTokens: 200000,
      })
      if (method === 'get_settings') return Promise.resolve({ model: 'fallback-model' })
      return Promise.resolve({})
    })
  })

  it('uses the focused SubAgent tab as the displayed session scope', async () => {
    const tabManager = {
      activeTabId: 'agent-tab',
      tabs: [{
        id: 'agent-tab',
        type: 'agent',
        title: 'review/1',
        closable: true,
        data: {
          subAgentRole: 'review',
          subAgentInstance: '1',
          parentChannel: 'cli',
          parentChatID: '/repo:Agent-main',
          agentChatID: 'cli:/repo:Agent-main/review:1',
        },
      }],
    } as unknown as TabManager

    renderWithProviders(<SessionInfo tabManager={tabManager} />)

    await waitFor(() => {
      expect(mocks.fetchCwd).toHaveBeenCalledWith({
        channel: 'cli',
        chatID: '/repo:Agent-main',
      })
    })
    expect(mocks.fetchSessionSubscription).toHaveBeenCalledWith({
      channel: 'agent',
      chatID: 'cli:/repo:Agent-main/review:1',
    })
    expect(mocks.rpc).toHaveBeenCalledWith('get_agent_session_dump_by_full_key', {
      full_key: 'cli:/repo:Agent-main/review:1',
    })
    expect(await screen.findByText('/repo')).toBeInTheDocument()
    expect(await screen.findByText('dump-model')).toBeInTheDocument()
    expect(await screen.findByText('sub-1')).toBeInTheDocument()
    expect(await screen.findByText('125')).toBeInTheDocument()
    expect(await screen.findByText('200000')).toBeInTheDocument()
  })
})
