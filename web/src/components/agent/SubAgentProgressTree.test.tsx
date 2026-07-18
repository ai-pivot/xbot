/**
 * Tests for SubAgentProgressTree (Spec A §1).
 *
 * Verifies:
 *  - SubAgent nodes render as inline cards with status icons
 *  - Running nodes have the sweep animation class
 *  - Done/error nodes have correct styling
 *  - Children render with indentation
 *  - Empty nodes returns null
 */
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { SubAgentProgressTree } from '@/components/agent/SubAgentProgressTree'
import type { WebSubAgentProgress } from '@/types/shared'

describe('SubAgentProgressTree', () => {
  it('returns null for empty nodes', () => {
    const { container } = render(<SubAgentProgressTree nodes={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders a running SubAgent node with sweep animation', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'explore', instance: 'search-1', status: 'running', desc: 'searching codebase' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    // The card should have the running class
    const card = container.querySelector('.subagent-card--running')
    expect(card).not.toBeNull()
    // Should show role:instance text
    expect(container.textContent).toContain('explore:search-1')
    expect(container.textContent).toContain('searching codebase')
  })

  it('renders a done SubAgent node without sweep animation', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'dev-node', instance: 'fix-1', status: 'done', desc: 'completed task' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    // Should NOT have the running class
    const runningCard = container.querySelector('.subagent-card--running')
    expect(runningCard).toBeNull()
    // Should show the role and desc
    expect(container.textContent).toContain('dev-node:fix-1')
    expect(container.textContent).toContain('completed task')
  })

  it('renders an error SubAgent node', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'reviewer', instance: 'cr-1', status: 'error', desc: 'failed' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    expect(container.textContent).toContain('reviewer:cr-1')
    expect(container.textContent).toContain('failed')
  })

  it('renders children with dashed border connection', () => {
    const nodes: WebSubAgentProgress[] = [
      {
        role: 'main',
        instance: 'task-1',
        status: 'running',
        desc: 'orchestrating',
        children: [
          { role: 'explore', instance: 'sub-1', status: 'done', desc: 'explored' },
          { role: 'dev-node', instance: 'sub-2', status: 'running', desc: 'coding' },
        ],
      },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    // Parent and children should be visible
    expect(container.textContent).toContain('main:task-1')
    expect(container.textContent).toContain('explore:sub-1')
    expect(container.textContent).toContain('dev-node:sub-2')
    // Children area should have dashed border
    const dashedBorders = container.querySelectorAll('.border-dashed')
    expect(dashedBorders.length).toBeGreaterThan(0)
  })

  it('renders multiple top-level nodes', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'explore', instance: 'a', status: 'running', desc: 'search A' },
      { role: 'explore', instance: 'b', status: 'done', desc: 'search B' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    expect(container.textContent).toContain('explore:a')
    expect(container.textContent).toContain('explore:b')
    expect(container.textContent).toContain('search A')
    expect(container.textContent).toContain('search B')
  })

  it('renders node without instance', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'dev-node', status: 'running', desc: 'working' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    expect(container.textContent).toContain('dev-node')
    expect(container.textContent).not.toContain('dev-node:')
  })
})
