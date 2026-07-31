import { act, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'
import type { GroupPanelPartInitParameters } from 'dockview'

vi.mock('@/workspace/panels/AgentPanel', () => ({ AgentPanel: () => null }))
vi.mock('@/workspace/panels/BackgroundPanel', () => ({ BackgroundPanel: () => null }))
vi.mock('@/workspace/panels/FilePanel', () => ({ FilePanel: () => null }))
vi.mock('@/workspace/panels/TerminalPanel', () => ({ TerminalPanel: () => null }))

import { ContextRing } from '@/components/agent/ContextRing'
import type { DockviewContextValue } from '@/workspace/types'
import { ReactContentRenderer, withDockviewProviders } from './DockviewContainer'

describe('Dockview provider bridge', () => {
  it('provides Radix context to controls rendered in isolated panel roots', () => {
    const ctx = {
      i18n: {
        locale: 'zh-CN',
        setLocale: vi.fn(),
        t: (_key: string, params?: Record<string, string | number>) =>
          `${params?.percent}% · ${params?.used} / ${params?.available}`,
      },
    } as unknown as DockviewContextValue

    render(withDockviewProviders(
      <ContextRing available promptTokens={80_000} maxContext={200_000} usagePercent={40} />,
      ctx,
    ))

    expect(screen.getByTestId('context-ring')).toHaveAccessibleName('40% · 80K / 200K')
  })

  it('does not re-render panel content during active-tab changes', () => {
    const onDidActivePanelChange = vi.fn()
    const renderer = new ReactContentRenderer('agent', {
      current: {} as DockviewContextValue,
    })
    const parameters = {
      params: {
        tabId: 'agent',
        type: 'agent',
        title: 'Agent',
        closable: false,
      },
      api: { id: 'agent' },
      containerApi: { onDidActivePanelChange },
    } as unknown as GroupPanelPartInitParameters

    act(() => renderer.init(parameters))

    expect(onDidActivePanelChange).not.toHaveBeenCalled()

    act(() => renderer.dispose())
  })
})
