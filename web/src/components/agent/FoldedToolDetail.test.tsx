/**
 * 浮层详情**按需**（D1 §3.3）：`ToolPopoverDetail` / "+N" 溢出菜单的展开卡片。
 *
 * 守护点：
 *   ① `toolsFolded=false` ⇒ 打开浮层**零请求**（默认视图路径逐字不变）；
 *   ② `toolsFolded=true` ⇒ 打开浮层即 `loadIterationDetail(turnID, iteration)`，
 *      详情区先骨架（标题/工具名等轻字段已渲染 —— 无感 G2），成功后完整详情渲染；
 *   ③ 失败 ⇒ 重试按钮（不静默），点击再发一次；
 *   ④ 同一次挂载内不重复请求（effect 依赖不含 status）。
 */
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

import { FoldedToolGroup } from '@/components/agent/FoldedToolGroup'
import { RegionActionsContext, TurnIDContext, type RegionActions } from '@/components/agent/RegionActionsContext'
import i18n from '@/i18n'
import type { WebToolProgress } from '@/types/shared'

beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

// radix Popover（@floating-ui 定位）在 jsdom 里需要 ResizeObserver（同 Collapse.test.tsx）。
class ROStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ROStub

function makeTool(over: Partial<WebToolProgress> = {}): WebToolProgress {
  return {
    name: 'Shell',
    label: 'Shell: ls',
    status: 'done',
    elapsedMs: 0,
    summary: '',
    detail: '',
    args: '',
    toolHints: '',
    iteration: 3,
    ...over,
  }
}

function makeActions(over: Partial<RegionActions> = {}): RegionActions {
  return {
    loadIterationDetail: vi.fn(async () => true),
    fetchInFlight: () => false,
    loadRegionSegment: vi.fn(async () => true),
    segmentInFlight: () => false,
    ...over,
  }
}

function Providers({ actions, turnID = 7, children }: { actions: RegionActions; turnID?: number; children: ReactNode }) {
  return (
    <RegionActionsContext.Provider value={actions}>
      <TurnIDContext.Provider value={turnID}>{children}</TurnIDContext.Provider>
    </RegionActionsContext.Provider>
  )
}

/** 浮层内容容器（radix Portal → document.body）。 */
const popoverContent = () => document.querySelector('[data-slot="popover-content"]')

describe('浮层详情按需：toolsFolded = false（现状路径，零请求）', () => {
  it('打开 pill 浮层不发任何请求，详情直接渲染', () => {
    const actions = makeActions()
    const tool = makeTool({ summary: '已完成' })
    render(
      <Providers actions={actions}>
        <FoldedToolGroup tools={[tool]} toolsFolded={false} iterationNumber={3} />
      </Providers>,
    )
    fireEvent.click(screen.getByTestId('tool-pill'))
    expect(popoverContent()).not.toBeNull()
    expect(actions.loadIterationDetail).not.toHaveBeenCalled()
    expect(screen.queryByTestId('tool-detail-skeleton')).toBeNull()
    expect(popoverContent()?.textContent).toContain('已完成')
  })

  it('不带 toolsFolded（老数据/无标记）同样零请求', () => {
    const actions = makeActions()
    render(
      <Providers actions={actions}>
        <FoldedToolGroup tools={[makeTool()]} />
      </Providers>,
    )
    fireEvent.click(screen.getByTestId('tool-pill'))
    expect(actions.loadIterationDetail).not.toHaveBeenCalled()
  })
})

describe('浮层详情按需：toolsFolded = true（先骨架 → 完整覆盖渲染）', () => {
  it('打开即按 (turnID, iteration) 拉详情；轻字段头部照常先渲染', async () => {
    const d = { resolve: null as null | ((v: boolean) => void) }
    const load = vi.fn(() => new Promise<boolean>((r) => { d.resolve = r }))
    const actions = makeActions({ loadIterationDetail: load })
    render(
      <Providers actions={actions} turnID={7}>
        <FoldedToolGroup tools={[makeTool()]} toolsFolded iterationNumber={3} />
      </Providers>,
    )
    // 打开浮层前：零请求（懒挂 —— 只有打开才发）
    expect(load).not.toHaveBeenCalled()
    fireEvent.click(screen.getByTestId('tool-pill'))
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1))
    // 迭代号取工具自身的（mergeToolRuns 吸收的成员各有自己的编号）
    expect(load).toHaveBeenCalledWith(7, 3)
    // 轻字段已渲染（无感）：工具名在浮层里可见；详情区是骨架
    expect(popoverContent()?.textContent).toContain('Shell')
    expect(screen.getByTestId('tool-detail-skeleton')).toBeTruthy()
    await act(async () => { d.resolve?.(true) })
  })

  it('成功后详情渲染（完整迭代同号覆盖 ⇒ toolsFolded 翻 false）', async () => {
    const load = vi.fn(async () => true)
    const actions = makeActions({ loadIterationDetail: load })
    const { rerender } = render(
      <Providers actions={actions}>
        <FoldedToolGroup tools={[makeTool()]} toolsFolded iterationNumber={3} />
      </Providers>,
    )
    fireEvent.click(screen.getByTestId('tool-pill'))
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1))
    // 同号覆盖后的完整迭代（summary/args 齐全）
    rerender(
      <Providers actions={actions}>
        <FoldedToolGroup
          tools={[makeTool({ summary: '完整摘要', args: '{"path":"/tmp"}' })]}
          toolsFolded={false}
          iterationNumber={3}
        />
      </Providers>,
    )
    await waitFor(() => expect(screen.queryByTestId('tool-detail-skeleton')).toBeNull())
    expect(popoverContent()?.textContent).toContain('完整摘要')
  })

  it('失败 ⇒ 重试按钮（不静默）；点击重试再发一次', async () => {
    const load = vi.fn()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true)
    const actions = makeActions({ loadIterationDetail: load })
    render(
      <Providers actions={actions}>
        <FoldedToolGroup tools={[makeTool()]} toolsFolded iterationNumber={3} />
      </Providers>,
    )
    fireEvent.click(screen.getByTestId('tool-pill'))
    await waitFor(() => expect(screen.getByTestId('tool-detail-retry')).toBeTruthy())
    expect(load).toHaveBeenCalledTimes(1)
    // 失败不自动重试
    await act(async () => {})
    expect(load).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByTestId('tool-detail-retry'))
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2))
  })

  it('无 turn 归属（turnID=0）⇒ 不发请求（standalone/legacy 行零差异）', async () => {
    const actions = makeActions()
    render(
      <Providers actions={actions} turnID={0}>
        <FoldedToolGroup tools={[makeTool()]} toolsFolded iterationNumber={3} />
      </Providers>,
    )
    fireEvent.click(screen.getByTestId('tool-pill'))
    await act(async () => {})
    expect(actions.loadIterationDetail).not.toHaveBeenCalled()
    expect(screen.queryByTestId('tool-detail-skeleton')).toBeNull()
  })
})
