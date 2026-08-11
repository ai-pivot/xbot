/**
 * Tests for plugin web UI components and WidgetZone style mapping.
 */
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'
import { render, screen } from '@testing-library/react'

import { BadgeWidget, MetricWidget, ProgressWidget, SparklineWidget, TableWidget, ListWidget, renderDeclarativeComponent } from '@/plugins/components'
import { WidgetSpanView } from '@/plugins/WidgetZone'

describe('declarative plugin components', () => {
  it('renders a badge with tone classes', () => {
    render(<BadgeWidget props={{ text: 'CI ok', tone: 'success', pulse: true }} />)
    const el = screen.getByText('CI ok')
    expect(el).toHaveClass('text-green-600')
    expect(el).toHaveClass('bg-green-50')
  })

  it('degrades unknown tone to normal', () => {
    render(<BadgeWidget props={{ text: 'x', tone: 'bogus' }} />)
    expect(screen.getByText('x')).toHaveClass('text-slate-700')
  })

  it('progress clamps value to 0..100', () => {
    render(<ProgressWidget props={{ value: 250, max: 100 }} />)
    // value is clamped to 100 → shows "100%"
    expect(screen.getByText('100%')).toBeInTheDocument()
  })

  it('metric renders label + value + delta', () => {
    render(<MetricWidget props={{ label: 'CPU', value: '82%', delta: '+3%', tone: 'warning' }} />)
    expect(screen.getByText('CPU')).toBeInTheDocument()
    expect(screen.getByText('82%')).toBeInTheDocument()
    expect(screen.getByText('+3%')).toBeInTheDocument()
  })

  it('sparkline renders svg for numeric data', () => {
    const { container } = render(<SparklineWidget props={{ data: [1, 5, 3, 8], color: '#f00' }} />)
    expect(container.querySelector('svg')).not.toBeNull()
    expect(container.querySelector('polyline')).not.toBeNull()
  })

  it('sparkline renders bars when type=bar', () => {
    const { container } = render(<SparklineWidget props={{ data: [1, 2], type: 'bar' }} />)
    expect(container.querySelector('rect')).not.toBeNull()
  })

  it('sparkline handles empty data gracefully', () => {
    render(<SparklineWidget props={{ data: [] }} />)
    expect(screen.getByText('no data')).toBeInTheDocument()
  })

  it('table renders columns and cell badges', () => {
    render(
      <TableWidget
        props={{
          columns: ['id', 'status'],
          rows: [
            { id: '#128', status: { text: '✓', tone: 'success' } },
            { id: '#127', status: { text: '✗', tone: 'error' } },
          ],
        }}
      />,
    )
    expect(screen.getByText('#128')).toBeInTheDocument()
    expect(screen.getByText('✓')).toHaveClass('text-green-600')
    expect(screen.getByText('✗')).toHaveClass('text-red-600')
  })

  it('list renders key/value pairs', () => {
    render(
      <ListWidget
        props={{
          title: 'Todos',
          items: [
            { key: 'deploy', value: 'running', tone: 'warning' },
            { key: 'tests', value: 'done', tone: 'success' },
          ],
        }}
      />,
    )
    expect(screen.getByText('Todos')).toBeInTheDocument()
    expect(screen.getByText('deploy')).toBeInTheDocument()
    expect(screen.getByText('done')).toHaveClass('text-green-600')
  })

  it('renderDeclarativeComponent dispatches by type', () => {
    const { container } = render(<>{renderDeclarativeComponent('progress', { value: 50 })}</>)
    expect(container.querySelector('.bg-slate-100')).not.toBeNull()
  })

  it('renderDeclarativeComponent degrades unknown type to badge', () => {
    render(<>{renderDeclarativeComponent('unknown-type', { text: 'fallback' })}</>)
    expect(screen.getByText('fallback')).toBeInTheDocument()
  })
})

describe('WidgetSpanView style mapping', () => {
  it('maps normal style', () => {
    render(<WidgetSpanView span={{ text: 't', style: 'normal' }} />)
    expect(screen.getByText('t')).toHaveClass('text-gray-800')
  })

  it('maps success style', () => {
    render(<WidgetSpanView span={{ text: 'ok', style: 'success' }} />)
    expect(screen.getByText('ok')).toHaveClass('text-green-600')
  })

  it('maps error style', () => {
    render(<WidgetSpanView span={{ text: 'err', style: 'error' }} />)
    expect(screen.getByText('err')).toHaveClass('text-red-600')
  })

  it('renders href as a link when present', () => {
    render(<WidgetSpanView span={{ text: 'docs', style: 'info', href: 'https://x' }} />)
    const a = screen.getByText('docs')
    expect(a.tagName).toBe('A')
    expect(a).toHaveAttribute('href', 'https://x')
  })

  it('degrades unknown style to normal', () => {
    render(<WidgetSpanView span={{ text: 'x', style: 'bogus' }} />)
    expect(screen.getByText('x')).toHaveClass('text-gray-800')
  })
})

// Keep vi imported (used by future interactive tests).
void vi
