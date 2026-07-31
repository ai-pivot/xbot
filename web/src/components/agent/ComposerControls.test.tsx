import { fireEvent, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ContextRing, formatTokensAsK } from './ContextRing'
import { thinkingModeLabel, thinkingModeToStep, thinkingStepToMode } from './ThinkingModeControl'
import { TodoPullOut } from './TodoPullOut'

describe('composer controls', () => {
  it('formats context usage in K and exposes percentage plus used/available values', () => {
    expect(formatTokensAsK(0)).toBe('0K')
    expect(formatTokensAsK(1250)).toBe('1.3K')
    renderWithProviders(
      <TooltipProvider>
        <ContextRing
          available
          promptTokens={160_000}
          maxContext={200_000}
          usagePercent={80}
        />
      </TooltipProvider>,
    )
    const ring = screen.getByTestId('context-ring')
    expect(ring).toHaveAccessibleName('80% · 160K / 200K')
    expect(ring.querySelector('circle:last-child')).toHaveClass('text-status-error')
  })

  it('shows an empty neutral ring while usage is unknown', () => {
    renderWithProviders(
      <TooltipProvider>
        <ContextRing
          available={false}
          promptTokens={0}
          maxContext={200_000}
          usagePercent={null}
        />
      </TooltipProvider>,
    )

    const ring = screen.getByTestId('context-ring')
    expect(ring).toHaveAccessibleName('Unknown / 200K')
    expect(ring.querySelectorAll('circle')).toHaveLength(1)
  })

  it('keeps the real over-limit percentage while capping the drawn ring', () => {
    renderWithProviders(
      <TooltipProvider>
        <ContextRing
          available
          promptTokens={250_000}
          maxContext={200_000}
          usagePercent={125}
        />
      </TooltipProvider>,
    )

    const ring = screen.getByTestId('context-ring')
    expect(ring).toHaveAccessibleName('125% · 250K / 200K')
    expect(ring.querySelector('circle:last-child')).toHaveAttribute('stroke-dashoffset', '0')
  })

  it('maps all four think positions and preserves the legacy enabled value', () => {
    expect([0, 1, 2, 3].map(thinkingStepToMode)).toEqual(['disabled', '', 'think', 'think-max'])
    expect(thinkingModeToStep('enabled')).toBe(2)
    expect(thinkingModeLabel('enabled')).toBe('think+')
    expect(thinkingModeLabel('think-max')).toBe('think++')
  })

  it('shows only the compact TODO toolbar until expanded, then folds without unmounting', () => {
    renderWithProviders(
      <TodoPullOut
        todoState={{
          todos: [
            { id: 1, text: 'done task', done: true },
            { id: 2, text: 'current task', done: false },
          ],
          doneCount: 1,
          total: 2,
          currentTask: { id: 2, text: 'current task', done: false },
        }}
      />,
    )

    const button = screen.getByRole('button')
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(button).toHaveTextContent('current task')
    expect(screen.getByText('done task').closest('.fold-container')).toHaveAttribute('data-state', 'closed')

    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('done task')).toBeInTheDocument()
    expect(screen.getByText('done task').closest('.fold-container')).toHaveAttribute('data-state', 'open')

    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByText('done task').closest('[aria-hidden]')).toHaveAttribute('aria-hidden', 'true')
  })
})
