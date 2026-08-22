/**
 * SidebarSectionStack 测试 —— VSCode 式左侧栏 section 堆叠。
 *
 * 覆盖：
 * - 多 section 垂直堆叠渲染 + header 折叠切换
 * - 折叠状态持久化（localStorage xbot:leftbar:section-collapsed）
 * - 拖拽分隔条调整上方 section 高度 + 持久化（xbot:leftbar:section-heights）
 * - defaultHeight：插件 section 固定初始高度；会话 section 无固定高度（自动 flex）
 * - 重叠 bug 守护：section 容器必须有 overflow-hidden（会话列表自然高度
 *   不得溢出覆盖下方插件区）
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'

import { SidebarSectionStack } from './SidebarSectionStack'

const HEIGHTS_KEY = 'xbot:leftbar:section-heights'
const COLLAPSED_KEY = 'xbot:leftbar:section-collapsed'

beforeEach(() => {
  localStorage.removeItem(HEIGHTS_KEY)
  localStorage.removeItem(COLLAPSED_KEY)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('SidebarSectionStack', () => {
  it('renders sections stacked with headers and separator handles', () => {
    render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    expect(screen.getByText('会话')).toBeTruthy()
    expect(screen.getByText('Git')).toBeTruthy()
    expect(screen.getByText('session-list')).toBeTruthy()
    expect(screen.getByText('git-panel')).toBeTruthy()
    // 相邻 section 之间有 1 个分隔条（拖拽调整上方 section 高度）
    expect(screen.getByRole('separator', { name: 'Resize 会话' })).toBeTruthy()
  })

  it('toggles collapse on header click and persists to localStorage', () => {
    render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    fireEvent.click(screen.getByTitle('收起Git'))
    expect(screen.queryByText('git-panel')).toBeNull()
    expect(JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? '{}')).toEqual({ git: true })

    fireEvent.click(screen.getByTitle('展开Git'))
    expect(screen.getByText('git-panel')).toBeTruthy()
    expect(JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? '{}')).toEqual({ git: false })
  })

  it('restores persisted collapse state on mount', () => {
    localStorage.setItem(COLLAPSED_KEY, JSON.stringify({ git: true }))
    render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    expect(screen.queryByText('git-panel')).toBeNull()
  })

  it('resizes the section above the handle via pointer drag and persists the height', () => {
    // offsetHeight 用于起始高度：模拟会话 section 当前 300px。
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      value: 300,
    })
    const { container } = render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    // 容器高度单独抬高（clamp 上限 = 容器 - MIN_SECTION_H）。
    const stackEl = container.firstElementChild as HTMLElement
    Object.defineProperty(stackEl, 'offsetHeight', { configurable: true, value: 800 })
    const handle = screen.getByRole('separator', { name: 'Resize 会话' })
    fireEvent.pointerDown(handle, { clientY: 100, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientY: 150, pointerId: 1 }) // +50px
    fireEvent.pointerUp(handle, { pointerId: 1 })

    const stored = JSON.parse(localStorage.getItem(HEIGHTS_KEY) ?? '{}')
    expect(stored.sessions).toBe(350)

    // 高度记忆后，section 以固定高度渲染（style.height=350px）。
    const section = container.querySelector<HTMLElement>('[data-section-id="sessions"]')
    expect(section?.style.height).toBe('350px')
  })

  it('applies defaultHeight to sections that have one and flex to the rest (auto layout)', () => {
    const { container } = render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    const sessions = container.querySelector<HTMLElement>('[data-section-id="sessions"]')
    const git = container.querySelector<HTMLElement>('[data-section-id="git"]')
    // 会话 section：自动 layout（flex: 1 1 0%）。
    expect(sessions?.style.flex).toBe('1 1 0%')
    // 插件 section：最后一个 section 也用 flex-1（占满剩余空间，VSCode 行为）。
    expect(git?.style.flex).toBe('1 1 0%')
  })

  it('overflow-guard: every section and the stack container clip content (no overlay)', () => {
    const { container } = render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    // 重叠 bug 根治断言：堆叠容器与每个 section 都必须裁剪内容。
    const stack = container.firstElementChild as HTMLElement
    expect(stack.className).toContain('overflow-hidden')
    for (const el of container.querySelectorAll<HTMLElement>('section[data-section-id]')) {
      expect(el.className).toContain('overflow-hidden')
    }
  })

  it('VSCode 式重排：拖 header 到另一 section 下方 → setSlotOrder 持久化 + 插入线出现', async () => {
    const { layoutRegistry } = await import('@/plugin-runtime/layoutRegistry')
    const spy = vi.spyOn(layoutRegistry, 'setSlotOrder')
    render(
      <SidebarSectionStack
        slotId="desktop.activity_bar"
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
          { id: 'note', title: 'Notes', defaultHeight: 240, content: <div>note-panel</div> },
        ]}
      />,
    )
    const gitHeader = screen.getByTitle('收起Git')
    const noteHeader = screen.getByTitle('收起Notes')
    // jsdom 的 DragEvent 不自动创建 dataTransfer —— 测试注入 mock。
    const dt = () => ({
      setData: vi.fn(),
      getData: (type: string) => type === 'application/x-xbot-layout-item' ? '' : '',
      effectAllowed: 'move',
      dropEffect: 'move',
      types: ['application/x-xbot-layout-item', 'application/x-xbot-layout-slot'],
    })

    // 开始拖 git；悬停在 notes 下方（jsdom rect 全 0，clientY>0 → after）。
    fireEvent.dragStart(gitHeader, { dataTransfer: dt() })
    fireEvent.dragOver(noteHeader, { dataTransfer: dt(), clientY: 10 })
    // 插入线渲染（dropHint: notes, after）。
    expect(screen.getAllByTestId('insertion-line').length).toBe(1)

    fireEvent.drop(noteHeader, { dataTransfer: dt(), clientY: 10 })
    expect(spy).toHaveBeenCalledWith('desktop.activity_bar', ['sessions', 'note', 'git'])
    spy.mockRestore()
  })

  it('VSCode 式重排：拖回原位（no-op）不调用 setSlotOrder', async () => {
    const { layoutRegistry } = await import('@/plugin-runtime/layoutRegistry')
    const spy = vi.spyOn(layoutRegistry, 'setSlotOrder')
    render(
      <SidebarSectionStack
        slotId="desktop.activity_bar"
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    const dt = () => ({ setData: vi.fn(), getData: vi.fn(), effectAllowed: 'move', dropEffect: 'move', types: ['text/plain'] })
    // 拖 sessions 悬停在 sessions 上（自己 → 无插入线），drop 无副作用。
    const sessionsHeader = screen.getByTitle('收起会话')
    fireEvent.dragStart(sessionsHeader, { dataTransfer: dt() })
    fireEvent.dragOver(sessionsHeader, { dataTransfer: dt(), clientY: 10 })
    expect(screen.queryByTestId('insertion-line')).toBeNull()
    fireEvent.drop(sessionsHeader, { dataTransfer: dt(), clientY: 10 })
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })

  it('无 slotId 时 header 不可拖拽（draggable=false）', () => {
    render(
      <SidebarSectionStack
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    expect(screen.getByTitle('收起会话').getAttribute('draggable')).toBe('false')
  })
})
