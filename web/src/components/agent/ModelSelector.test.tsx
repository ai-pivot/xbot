import { fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'

import { ModelSelector } from '@/components/agent/ModelSelector'
import { renderWithProviders } from '@/test-utils'
import type { ModelEntry, Subscription } from '@/types/shared'

vi.mock('@/hooks/useWSConnection', () => ({ useWSConnection: () => ({}) }))
vi.mock('@/components/agent/api', () => ({ selectModel: vi.fn() }))

const subscriptions = [{ id: 'cpa', name: 'CPA' }] as Subscription[]
const entries: ModelEntry[] = [
  { sub_id: 'cpa', sub_name: 'CPA', model: 'visible-model', status: 'normal' },
  { sub_id: 'cpa', sub_name: 'CPA', model: 'manual-offline-model', status: 'offline' },
  { sub_id: 'cpa', sub_name: 'CPA', model: 'hidden-model', status: 'disabled' },
  { sub_id: 'other', sub_name: 'Disabled-only subscription', model: 'hidden-other', status: 'disabled' },
]

function renderSelector(modelEntries = entries) {
  renderWithProviders(<ModelSelector
    channel="web"
    chatID="chat-1"
    currentSubID="cpa"
    currentModel="visible-model"
    subscriptions={subscriptions}
    modelEntries={modelEntries}
    thinkingMode="off"
    busy={false}
    onModelSelected={vi.fn()}
    onThinkingModeChange={vi.fn(async () => true)}
  />)
  fireEvent.click(screen.getByRole('button', { name: /Choose model|选择模型/i }))
  return screen.getByRole('dialog')
}

describe('ModelSelector visibility', () => {
  it('omits disabled models and empty subscriptions but keeps enabled offline models', () => {
    const picker = renderSelector()
    expect(within(picker).getByRole('button', { name: 'visible-model' })).toBeEnabled()
    expect(within(picker).getByRole('button', { name: 'manual-offline-model' })).toBeEnabled()
    expect(within(picker).queryByText('hidden-model')).not.toBeInTheDocument()
    expect(within(picker).queryByText('hidden-other')).not.toBeInTheDocument()
    expect(within(picker).queryByText('Disabled-only subscription')).not.toBeInTheDocument()
  })

  it('does not reveal a disabled model through search', () => {
    const picker = renderSelector()
    fireEvent.change(within(picker).getByPlaceholderText(/search models|搜索模型/i), { target: { value: 'hidden' } })
    expect(within(picker).queryByText('hidden-model')).not.toBeInTheDocument()
    expect(within(picker).queryByText('hidden-other')).not.toBeInTheDocument()
    expect(within(picker).queryByText('CPA')).not.toBeInTheDocument()
  })
})
