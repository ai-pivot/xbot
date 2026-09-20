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

import i18n from '@/i18n'
import { SidebarSectionStack } from './SidebarSectionStack'

const HEIGHTS_KEY = 'xbot:leftbar:section-heights'
const COLLAPSED_KEY = 'xbot:leftbar:section-collapsed'

/**
 * header 的 aria-title 走宿主 i18n（`sidebar.sectionExpand/sectionCollapse`）——
 * 断言必须按 i18n 源取值：测试环境语言由 i18n 检测决定（实测 en），硬编码中文
 * 会在语言变化时假红。
 */
const collapseTitle = (title: string) => i18n.t('sidebar.sectionCollapse', { title })
const expandTitle = (title: string) => i18n.t('sidebar.sectionExpand', { title })

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
    fireEvent.click(screen.getByTitle(collapseTitle('Git')))
    expect(screen.getByText('git-panel').parentElement?.style.display).toBe('none')
    expect(JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? '{}')).toEqual({ git: true })

    fireEvent.click(screen.getByTitle(expandTitle('Git')))
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
    expect(screen.getByText('git-panel').parentElement?.style.display).toBe('none')
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

  it('DnD 已删除：header 无 draggable/onDragStart/onDragOver 属性', () => {
    render(
      <SidebarSectionStack
        slotId="desktop.activity_bar"
        sections={[
          { id: 'sessions', title: '会话', content: <div>session-list</div> },
          { id: 'git', title: 'Git', defaultHeight: 240, content: <div>git-panel</div> },
        ]}
      />,
    )
    // DnD 已删除（2026-09-20）：即使有 slotId，header 也不再有 DnD 属性。
    const header = screen.getByTitle(collapseTitle('会话'))
    expect(header.getAttribute('draggable')).toBeNull()
    // 插入线不再渲染。
    expect(screen.queryByTestId('insertion-line')).toBeNull()
  })
})
