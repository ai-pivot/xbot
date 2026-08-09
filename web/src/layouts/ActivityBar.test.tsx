import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ActivityBar } from './ActivityBar'

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

  it('renders sidebar toggle button', () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
        sidebarCollapsed={false}
        onToggleSidebar={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('Toggle sidebar')).toBeInTheDocument()
  })

  it('highlights toggle when sidebar is collapsed', () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
        sidebarCollapsed={true}
        onToggleSidebar={vi.fn()}
      />,
    )

    const toggle = screen.getByLabelText('Toggle sidebar')
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
  })

  it('calls onToggleSidebar when toggle is clicked', async () => {
    const onToggle = vi.fn()
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
        onToggleSidebar={onToggle}
      />,
    )

    const toggle = screen.getByLabelText('Toggle sidebar')
    await toggle.click()
    expect(onToggle).toHaveBeenCalledOnce()
  })

  it('calls onOpenSettings when settings is clicked', async () => {
    const onOpenSettings = vi.fn()
    renderWithProviders(
      <ActivityBar
        onOpenSettings={onOpenSettings}
      />,
    )

    const settings = screen.getByLabelText('Appearance')
    await settings.click()
    expect(onOpenSettings).toHaveBeenCalledOnce()
  })

  it('does not render sidebar toggle when onToggleSidebar is not provided', () => {
    renderWithProviders(
      <ActivityBar
        onOpenSettings={vi.fn()}
      />,
    )

    expect(screen.queryByLabelText('Toggle sidebar')).not.toBeInTheDocument()
  })

  it('does not fetch identities (channel filtering removed)', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    renderWithProviders(
      <ActivityBar onOpenSettings={vi.fn()} />,
    )

    expect(fetchSpy).not.toHaveBeenCalledWith(
      '/api/account/identities/list',
      expect.anything(),
    )
  })
})
