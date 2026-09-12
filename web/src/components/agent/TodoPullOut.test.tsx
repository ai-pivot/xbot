/**
 * TodoPullOut — editable checklist + per-item "set as goal".
 *
 * The toolbar used to be read-only. Users can now rename an item inline, toggle
 * it done, delete it, and promote any item to the session goal with one click.
 * Without `onUpdateTodos` the list must stay read-only (a surface that has no
 * session to write back to).
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { TodoPullOut } from '@/components/agent/TodoPullOut'
import type { TodoState } from '@/hooks/useTodos'
import type { TodoItem } from '@/types/shared'

function state(items: TodoItem[]): TodoState {
  const done = items.filter((t) => t.status === 'done').length
  return {
    todos: items,
    doneCount: done,
    total: items.length,
    currentTask: items.find((t) => t.status !== 'done') ?? null,
  }
}

const baseItems: TodoItem[] = [
  { text: '修复登录超时', status: 'doing' },
  { text: '补齐单测', status: 'pending' },
  { text: '更新文档', status: 'done' },
]

function renderTray(items = baseItems, extra: Partial<React.ComponentProps<typeof TodoPullOut>> = {}) {
  const utils = renderWithProviders(<TodoPullOut todoState={state(items)} {...extra} />)
  // Expand the list (the collapsed body is unmounted by AnimatedCollapse).
  fireEvent.click(screen.getByTestId('todo-toggle'))
  return utils
}

describe('TodoPullOut — editing', () => {
  it('stays read-only when no onUpdateTodos is provided', () => {
    renderTray(baseItems)
    expect(screen.queryByTestId('todo-edit')).toBeNull()
    expect(screen.queryByTestId('todo-delete')).toBeNull()
    // every row's status control is inert without a write path
    screen.getAllByTestId('todo-status').forEach((b) => expect(b).toBeDisabled())
  })

  it('renames an item: click text → input prefilled → Enter commits', () => {
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos })

    fireEvent.click(screen.getAllByTestId('todo-text')[1])
    const input = screen.getByTestId('todo-edit-input') as HTMLInputElement
    expect(input.value).toBe('补齐单测') // prefilled with the current text

    fireEvent.change(input, { target: { value: '补齐 e2e 测试' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(onUpdateTodos).toHaveBeenCalledTimes(1)
    const next = onUpdateTodos.mock.calls[0][0] as TodoItem[]
    expect(next[1].text).toBe('补齐 e2e 测试')
    // untouched rows keep their status and text
    expect(next[0]).toEqual(baseItems[0])
    expect(next[2]).toEqual(baseItems[2])
  })

  it('cancel with Escape does not persist anything', () => {
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos })
    fireEvent.click(screen.getAllByTestId('todo-text')[0])
    const input = screen.getByTestId('todo-edit-input')
    fireEvent.change(input, { target: { value: '不该保存' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(onUpdateTodos).not.toHaveBeenCalled()
    // the row is back to its original text (the header also renders it, so
    // assert on the row itself)
    expect(screen.getAllByTestId('todo-text')[0]).toHaveTextContent('修复登录超时')
  })

  it('clearing the text keeps the original item (a checklist item is never erased)', () => {
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos })
    fireEvent.click(screen.getAllByTestId('todo-text')[0])
    const input = screen.getByTestId('todo-edit-input')
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onUpdateTodos).not.toHaveBeenCalled()
  })

  it('toggles done from the status icon', () => {
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos })
    fireEvent.click(screen.getAllByTestId('todo-status')[0]) // doing → done
    const next = onUpdateTodos.mock.calls[0][0] as TodoItem[]
    expect(next[0].status).toBe('done')
  })

  it('deletes an item', () => {
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos })
    fireEvent.click(screen.getAllByTestId('todo-delete')[1])
    const next = onUpdateTodos.mock.calls[0][0] as TodoItem[]
    expect(next.map((t) => t.text)).toEqual(['修复登录超时', '更新文档'])
  })
})

describe('TodoPullOut — set a TODO as the goal', () => {
  it('promotes a row to the goal in one click, passing its text', () => {
    const onSetGoalTodo = vi.fn()
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo })
    fireEvent.click(screen.getAllByTestId('todo-set-goal')[0])
    expect(onSetGoalTodo).toHaveBeenCalledWith('修复登录超时')
  })

  it('hides the per-row goal action when no handler is wired', () => {
    renderTray(baseItems, { onUpdateTodos: vi.fn() })
    expect(screen.queryByTestId('todo-set-goal')).toBeNull()
  })

  it('marks the row whose text is the active goal', () => {
    renderTray(baseItems, { onUpdateTodos: vi.fn(), goalText: '补齐单测' })
    const row = screen.getAllByTestId('todo-item')[1]
    expect(within(row).getByTestId('todo-goal-badge')).toBeInTheDocument()
    // …and only that row
    expect(screen.getAllByTestId('todo-goal-badge')).toHaveLength(1)
  })
})

describe('TodoPullOut — 触屏（无 hover）交互', () => {
  const orig = window.matchMedia
  /** jsdom 无 CSS 引擎 → 断言「类契约」+ DOM 结构。 */
  function mockTouch(isTouch: boolean) {
    window.matchMedia = ((query: string) => ({
      matches: isTouch && query.includes('(hover: none)'),
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia
  }
  afterEach(() => {
    window.matchMedia = orig
  })

  it('触屏：只留一个 ⋯ 按钮（32px），三个操作收进菜单 —— 不再并排占宽', () => {
    mockTouch(true)
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo: vi.fn() })

    // 唯一常驻操作：⋯（32px 触控目标）
    const more = screen.getAllByTestId('todo-more')
    expect(more).toHaveLength(baseItems.length)
    expect(more[0].className).toContain('size-8')

    // 三个操作不再内联渲染（否则并排 ≈104px，把正文挤没）
    expect(screen.queryAllByTestId('todo-edit')).toHaveLength(0)
    expect(screen.queryAllByTestId('todo-delete')).toHaveLength(0)
    expect(screen.queryAllByTestId('todo-set-goal')).toHaveLength(0)
    expect(screen.queryByTestId('todo-actions')).toBeNull()
    expect(screen.queryByTestId('todo-actions-menu')).toBeNull() // 未点击不渲染菜单

    // 状态切换仍有足够命中区（外扩，不改视觉位置）
    expect(screen.getAllByTestId('todo-status')[0].className).toContain('p-2.5')
  })

  it('桌面：仍是 hover 内联三个图标（20px），无 ⋯', () => {
    mockTouch(false)
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo: vi.fn() })

    const actions = screen.getAllByTestId('todo-actions')[0]
    expect(actions.className).toContain('opacity-0')
    expect(actions.className).toContain('group-hover:opacity-100')
    expect(screen.getAllByTestId('todo-edit')[0].className).toContain('size-5')
    expect(screen.getAllByTestId('todo-status')[0].className).not.toContain('p-2')
    expect(screen.queryAllByTestId('todo-more')).toHaveLength(0)
  })

  it('触屏：点 ⋯ → 菜单出现（三个操作带文字标签），点菜单项真实生效', () => {
    mockTouch(true)
    const onUpdateTodos = vi.fn()
    const onSetGoalTodo = vi.fn()
    renderTray(baseItems, { onUpdateTodos, onSetGoalTodo })

    fireEvent.click(screen.getAllByTestId('todo-more')[0])
    const menu = screen.getByTestId('todo-actions-menu')
    expect(menu).toBeInTheDocument()
    // 菜单项 ≥40px（h-10）且带文字标签，不是只有图标
    for (const id of ['todo-set-goal', 'todo-edit', 'todo-delete']) {
      const item = within(menu).getByTestId(id)
      expect(item.className).toContain('h-10')
      expect(item.textContent?.trim().length ?? 0).toBeGreaterThan(1)
    }

    // 删除真实生效
    fireEvent.click(within(menu).getByTestId('todo-delete'))
    expect(onUpdateTodos).toHaveBeenCalledTimes(1)
    // 点的是第 0 行（修复登录超时）的 ⋯ → 删除后剩后两条
    expect((onUpdateTodos.mock.calls[0][0] as TodoItem[]).map((x) => x.text)).toEqual([
      '补齐单测',
      '更新文档',
    ])
  })

  it('触屏：菜单里的「编辑」进入编辑态，设为目标调用回调', () => {
    mockTouch(true)
    const onSetGoalTodo = vi.fn()
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo })

    fireEvent.click(screen.getAllByTestId('todo-more')[1])
    fireEvent.click(screen.getByTestId('todo-edit'))
    expect(screen.getByTestId('todo-edit-input')).toBeInTheDocument()
    fireEvent.keyDown(screen.getByTestId('todo-edit-input'), { key: 'Escape' })

    fireEvent.click(screen.getAllByTestId('todo-more')[1])
    fireEvent.click(screen.getByTestId('todo-set-goal'))
    expect(onSetGoalTodo).toHaveBeenCalledWith('补齐单测')
  })
})
