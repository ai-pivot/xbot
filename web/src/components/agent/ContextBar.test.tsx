import { describe, expect, it } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ContextBar } from './ContextBar'
import type { TodoState } from '@/hooks/useTodos'

function makeTodoState(overrides: Partial<TodoState> = {}): TodoState {
  return {
    todos: [
      { id: 1, text: 'Task 1', done: true },
      { id: 2, text: 'Task 2', done: false },
    ],
    doneCount: 1,
    total: 2,
    currentTask: { id: 2, text: 'Task 2', done: false },
    ...overrides,
  }
}

describe('ContextBar', () => {
  it('renders model name and token info', () => {
    const { container } = renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={12300}
      />,
    )

    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
    // Token info "12.3K/200K" and "6%" are in separate spans within the button
    const text = container.textContent ?? ''
    expect(text).toContain('12.3K')
    expect(text).toContain('200K')
    expect(text).toContain('6%')
  })

  it('shows TODO progress text when todos present', () => {
    const todoState = makeTodoState()
    renderWithProviders(
      <ContextBar
        todoState={todoState}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={100}
      />,
    )

    expect(screen.getByText(/1\/2/)).toBeInTheDocument()
  })

  it('does not show TODO text when no todos', () => {
    renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={100}
      />,
    )

    expect(screen.queryByText(/已完成/)).not.toBeInTheDocument()
  })

  it('shows 0% when promptTokens is 0', () => {
    renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={0}
      />,
    )

    expect(screen.getByText('0%')).toBeInTheDocument()
  })

  it('uses red color for context percentage when >= 80%', () => {
    renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={100000}
        promptTokens={85000}
      />,
    )

    const pctEl = screen.getByText('85%')
    expect(pctEl).toBeInTheDocument()
    expect(pctEl.className).toContain('text-red-500')
  })

  it('does not show token count when maxContext is 0', () => {
    renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={0}
        promptTokens={100}
      />,
    )

    // maxContext=0 means no token info, just model name
    expect(screen.getByText('gpt-4o')).toBeInTheDocument()
    expect(screen.queryByText(/100/)).not.toBeInTheDocument()
  })

  it('shows model as dash when model is empty', () => {
    renderWithProviders(
      <ContextBar
        todoState={null}
        model=""
        maxContext={200000}
        promptTokens={0}
      />,
    )

    expect(screen.getByText('—')).toBeInTheDocument()
  })

  it('expands TODO list on click when todos present', () => {
    const todoState = makeTodoState()
    renderWithProviders(
      <ContextBar
        todoState={todoState}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={0}
      />,
    )

    // Click the bar to expand
    fireEvent.click(screen.getByText(/1\/2/))

    // Should show TODO items
    expect(screen.getByText('Task 1')).toBeInTheDocument()
    expect(screen.getByText('Task 2')).toBeInTheDocument()
  })

  it('formats large token counts with K and M suffixes', () => {
    const { container } = renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={2000000}
        promptTokens={1500000}
      />,
    )

    // 1.5M and 2M (integers don't get .0 suffix)
    const text1 = container.textContent ?? ''
    expect(text1).toContain('1.5M')
    expect(text1).toContain('2M')

    const { container: container2 } = renderWithProviders(
      <ContextBar
        todoState={null}
        model="gpt-4o"
        maxContext={200000}
        promptTokens={999}
      />,
    )

    const text2 = container2.textContent ?? ''
    expect(text2).toContain('999')
    expect(text2).toContain('200K')
  })
})
