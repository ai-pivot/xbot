import { describe, expect, it, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ActivityBar } from './ActivityBar'
import type { SessionStore } from '@/hooks/useSessionStore'

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): Partial<SessionStore> => ({
    activeChannel: null,
    setActiveChannel: vi.fn(),
  }),
}))

// Mock fetch for identities
const mockIdentities = [
  { id: 1, channel: 'cli', channel_user_id: '张三' },
  { id: 2, channel: 'feishu', channel_user_id: '李四' },
]

beforeEach(() => {
  globalThis.fetch = vi.fn(() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ identities: mockIdentities }),
    } as Response),
  )
})

vi.mock('@/components/ui/tooltip', () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

describe('ActivityBar', () => {
  it('renders settings icon', () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('Appearance')).toBeInTheDocument()
  })

  it('renders aggregate globe icon', () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('All Channels')).toBeInTheDocument()
  })

  it('renders channel identity icons after fetch', async () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    // After fetch resolves, CLI and Feishu icons should appear
    expect(await screen.findByLabelText('CLI')).toBeInTheDocument()
    expect(screen.getByLabelText('Feishu')).toBeInTheDocument()
  })

  it('renders badge with first character of channel_user_id', async () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    // Wait for identities to load
    await screen.findByLabelText('CLI')

    // Badge should show first character of channel_user_id
    const cliButton = screen.getByLabelText('CLI')
    const badge = cliButton.querySelector('.text-\\[8px\\]')
    expect(badge).toHaveTextContent('张')
  })
})
