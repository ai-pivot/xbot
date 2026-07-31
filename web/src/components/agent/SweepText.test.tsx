import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { SweepText } from './SweepText'

describe('SweepText', () => {
  it('renders one accessible sweep with delayed character spans', () => {
    const { container } = render(<SweepText text="Thinking" />)
    const sweep = screen.getByLabelText('Thinking')

    expect(container.childElementCount).toBe(1)
    expect(sweep).toHaveClass('sweep-text')
    expect(sweep.childElementCount).toBe(8)
    expect(sweep).toHaveTextContent('Thinking')
    expect(sweep.style.getPropertyValue('--sweep-color')).toBe('var(--text-primary)')
    expect(sweep.querySelectorAll('.sweep-text-char')).toHaveLength(8)
    expect(sweep.children[1]).toHaveStyle({ animationDelay: '0.15s' })
  })

  it('accepts a custom color and class name', () => {
    render(<SweepText text="Running tool" color="var(--accent)" className="font-mono" />)
    const sweep = screen.getByLabelText('Running tool')

    expect(sweep).toHaveClass('sweep-text', 'font-mono')
    expect(sweep.style.getPropertyValue('--sweep-color')).toBe('var(--accent)')
  })
})
