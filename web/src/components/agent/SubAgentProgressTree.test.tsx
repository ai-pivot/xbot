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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { SubAgentProgressTree } from '@/components/agent/SubAgentProgressTree'
import type { WebSubAgentProgress } from '@/types/shared'
import { DockviewContext, type DockviewContextValue } from '@/workspace/types'

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
    expect(container.querySelector('.sweep-text')).not.toBeNull()
    expect(container.querySelector('.animate-pulse')).toBeNull()
    // Should show role:instance text
    expect(container.textContent).toContain('explore:search-1')
    expect(container.textContent).toContain('searching codebase')
  })

  it('renders a done SubAgent node without sweep animation', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'dev-node', instance: 'fix-1', status: 'done', desc: 'completed task' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    expect(container.querySelector('.sweep-text')).toBeNull()
    // Should show the role and desc
    expect(container.textContent).toContain('dev-node:fix-1')
    expect(container.textContent).toContain('completed task')
  })

  it('renders an error SubAgent node', () => {
    const nodes: WebSubAgentProgress[] = [
      { role: 'reviewer', instance: 'cr-1', status: 'error', desc: 'failed' },
    ]
    const { container } = render(<SubAgentProgressTree nodes={nodes} />)
    expect(container.querySelector('.sweep-text')).toBeNull()
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
    expect(container.querySelectorAll('.sweep-text')).toHaveLength(2)
    expect(container.querySelector('.animate-pulse')).toBeNull()
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

  it('opens top-level and nested SubAgent tabs from their full session keys', () => {
    const openTab = vi.fn()
    const nodes: WebSubAgentProgress[] = [{
      role: 'orchestrator',
      instance: '1',
      status: 'running',
      sessionKey: 'cli:/repo:Agent-main/orchestrator:1',
      children: [{
        role: 'review',
        instance: '2',
        status: 'running',
        sessionKey: 'cli:/repo:Agent-main/orchestrator:1/review:2',
      }],
    }]

    render(
      <DockviewContext.Provider value={{ openTab } as unknown as DockviewContextValue}>
        <SubAgentProgressTree nodes={nodes} />
      </DockviewContext.Provider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Open SubAgent orchestrator/1' }))
    expect(openTab).toHaveBeenLastCalledWith(expect.objectContaining({
      type: 'agent',
      title: 'orchestrator/1',
      data: {
        subAgentRole: 'orchestrator',
        subAgentInstance: '1',
        parentChatID: '/repo:Agent-main',
        parentChannel: 'cli',
        agentChatID: 'cli:/repo:Agent-main/orchestrator:1',
      },
    }))

    fireEvent.click(screen.getByRole('button', { name: 'Open SubAgent review/2' }))
    expect(openTab).toHaveBeenLastCalledWith(expect.objectContaining({
      type: 'agent',
      title: 'review/2',
      data: {
        subAgentRole: 'review',
        subAgentInstance: '2',
        parentChatID: '/repo:Agent-main/orchestrator:1',
        parentChannel: 'cli',
        agentChatID: 'cli:/repo:Agent-main/orchestrator:1/review:2',
      },
    }))
  })

  it('keeps legacy nodes without session keys read-only', () => {
    const openTab = vi.fn()
    render(
      <DockviewContext.Provider value={{ openTab } as unknown as DockviewContextValue}>
        <SubAgentProgressTree nodes={[{ role: 'review', status: 'running' }]} />
      </DockviewContext.Provider>,
    )

    const node = screen.getByRole('button', { name: 'review' })
    expect(node).toBeDisabled()
    fireEvent.click(node)
    expect(openTab).not.toHaveBeenCalled()
  })
})
