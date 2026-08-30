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

const mockChannels = ['cli', 'feishu']

beforeEach(() => {
  globalThis.fetch = vi.fn(() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({
        ok: true,
        data: { channels: mockChannels },
        error: null,
      }),
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
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/channels/list',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('renders no identity badge after fetch (multi-user removal)', async () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    // Wait for channels to load
    await screen.findByLabelText('CLI')

    // No identity badge — the canonical identity system was removed; the
    // channel icons are plain (no channel_user_id badge).
    const cliButton = screen.getByLabelText('CLI')
    const badge = cliButton.querySelector('.text-\\[8px\\]')
    expect(badge).toBeNull()
  })
})
