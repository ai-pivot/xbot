import '@testing-library/jest-dom/vitest'
/**
 * 组织树 sparkline：2026-10-22 用户反馈「12px 宽 1px 线，在 1500px 截图里几乎不可见」。
 * 现在要求：2px 折线 + 部门状态色；无数据时画 1px 灰色基线（不能是空白）。
 */
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { OrgTree } from './OrgTree'
import type { Lane } from './types'

const LANES: Lane[] = [
  { id: 'agent:court.gongbu/main', name: '工部', type: 'agent', status: 'busy', task: '部署', progress: 0.5 },
  { id: 'agent:court.hubu/main', name: '户部', type: 'agent', status: 'idle', task: '—', progress: 0 },
]

function renderTree(series: Record<string, number[]>) {
  return render(
    <OrgTree
      lanes={LANES}
      laneFilter={null}
      onLaneFilter={() => {}}
      counts={{}}
      series={series}
    />,
  )
}

describe('组织树 sparkline', () => {
  it('有数据：12px 宽、2px 描边折线，颜色用部门状态色', () => {
    const { container } = renderTree({ 'agent:court.gongbu/main': [1, 0, 2, 0, 1, 0] })
    const svg = container.querySelector('[data-testid="sparkline"]') as SVGSVGElement
    expect(svg).toBeTruthy()
    expect(svg.getAttribute('width')).toBe('12')
    expect(svg.getAttribute('data-empty')).toBe('false')
    const line = svg.querySelector('polyline')
    expect(line).toBeTruthy()
    expect(line?.getAttribute('stroke-width')).toBe('2')
    // 忙碌 = --lane-busy（状态色，不是写死的灰）
    expect(line?.getAttribute('stroke')).toBe('var(--lane-busy)')
    expect((line?.getAttribute('points') ?? '').split(' ')).toHaveLength(6)
  })

  it('无数据：画 1px 灰色基线，不留空白', () => {
    const { container } = renderTree({ 'agent:court.hubu/main': [0, 0, 0, 0, 0, 0] })
    const svg = container.querySelector('[data-testid="sparkline"]') as SVGSVGElement
    expect(svg.getAttribute('data-empty')).toBe('true')
    const base = svg.querySelector('line')
    expect(base).toBeTruthy()
    expect(base?.getAttribute('stroke-width')).toBe('1')
    expect(base?.getAttribute('stroke')).toBe('currentColor')
    expect(svg.className.baseVal).toContain('text-text-muted')
    expect(screen.getAllByTestId('sparkline')).toHaveLength(2)
  })
})
