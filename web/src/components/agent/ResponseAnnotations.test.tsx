import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { renderWithProviders } from '@/test-utils'
import { UserMessage } from './UserMessage'
import { formatAnnotatedMessage } from '@/lib/messageAnnotations'
import { parseResponseAnnotations } from './ResponseAnnotations'

const entries = [
  { text: '**Quoted text**\nSecond line', annotation: 'Please clarify', source: { turnId: 9, startOffset: 683 } },
  { text: 'Another quote', annotation: 'Second comment' },
]
const envelope = (body = 'Actual request', data: unknown = entries) => `Response annotations:\nInternal instructions — not user-facing.\n<response-annotations>${JSON.stringify(data)}</response-annotations>\n\nMy request:\n${body}`

describe('response annotation presentation', () => {
  it('renders the current formatter envelope using the shared protocol validator', () => {
    const content = formatAnnotatedMessage('Current request', [{ id: '1', quote: 'Current quote', comment: 'Current comment', source: {} }], (key) => key)
    renderWithProviders(<UserMessage content={content} />)
    expect(screen.getByText('Current request')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /1 (条注释|annotation)/ })).toBeInTheDocument()
    expect(parseResponseAnnotations(content.replace('"source": {}', '"source": null'))).toBeNull()
  })

  it('extracts only valid generated envelopes and discards source metadata from the view', () => {
    expect(parseResponseAnnotations(envelope())).toEqual({
      annotations: entries.map(({ text, annotation }) => ({ text, annotation })),
      body: 'Actual request',
    })
    expect(parseResponseAnnotations(envelope('', []))).toBeNull()
    expect(parseResponseAnnotations(envelope('', [{ text: 'missing comment' }]))).toBeNull()
    expect(parseResponseAnnotations(envelope().replace(JSON.stringify(entries), 'bad JSON'))).toBeNull()
    expect(parseResponseAnnotations('Quoted example\n' + envelope())).toBeNull()
    expect(parseResponseAnnotations('<response-annotations>[]</response-annotations>')).toBeNull()
  })

  it('shows a compact count and request without protocol text; click opens a portal with every annotation', async () => {
    renderWithProviders(<UserMessage content={envelope()} />)
    expect(screen.getByText('Actual request')).toBeInTheDocument()
    expect(screen.queryByText(/Internal instructions/)).not.toBeInTheDocument()
    expect(screen.queryByText('Please clarify')).not.toBeInTheDocument()
    const trigger = screen.getByRole('button', { name: /2 (条注释|annotations)/ })
    fireEvent.click(trigger)
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Please clarify')
    expect(dialog).toHaveTextContent('Second comment')
    expect(dialog).toHaveTextContent('**Quoted text**')
    expect(screen.getByTestId('user-bubble')).not.toContainElement(dialog)
    expect(dialog).not.toHaveTextContent('startOffset')
    fireEvent.click(trigger)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('opens on hover, lets the pointer enter the card, and closes after leaving', async () => {
    renderWithProviders(<UserMessage content={envelope()} />)
    const trigger = screen.getByRole('button', { name: /2 (条注释|annotations)/ })
    fireEvent.pointerEnter(trigger, { pointerType: 'mouse' })
    const dialog = await screen.findByRole('dialog')
    fireEvent.pointerLeave(trigger)
    fireEvent.pointerEnter(dialog)
    // Crossing the small gap must not close the card before it can be read.
    await new Promise((resolve) => setTimeout(resolve, 220))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.pointerLeave(dialog)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('opens with keyboard focus and dismisses with Escape', async () => {
    renderWithProviders(<UserMessage content={envelope()} />)
    const trigger = screen.getByRole('button', { name: /2 (条注释|annotations)/ })
    fireEvent.focus(trigger)
    await screen.findByRole('dialog')
    fireEvent.keyDown(trigger, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('supports an annotation-only message and retains the full envelope when editing', () => {
    const content = envelope('')
    const { rerender } = renderWithProviders(<UserMessage content={content} />)
    expect(screen.getByRole('button', { name: /2 (条注释|annotations)/ })).toBeInTheDocument()
    rerender(<UserMessage content={content} isEditing onRewind={vi.fn()} />)
    expect(screen.getByRole('textbox')).toHaveValue(content)
  })

  it('keeps malformed envelopes visible rather than silently hiding user data', () => {
    renderWithProviders(<UserMessage content={envelope('Keep my request', [{ text: 'invalid' }])} />)
    expect(screen.queryByRole('button', { name: /条注释|annotations/ })).not.toBeInTheDocument()
    expect(screen.getByTestId('user-bubble')).toHaveTextContent('Internal instructions')
    expect(screen.getByTestId('user-bubble')).toHaveTextContent('Keep my request')
  })
})
