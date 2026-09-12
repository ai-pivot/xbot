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
  /** jsdom 无 CSS 引擎 → 断言「类契约」：触屏必须常显 + 触控目标 ≥32px。 */
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

  it('触屏：操作按钮常显（不能依赖 hover），且为 32px 触控目标', () => {
    mockTouch(true)
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo: vi.fn() })

    const actions = screen.getAllByTestId('todo-actions')[0]   // 每行一个
    expect(actions.className).toContain('opacity-100')
    expect(actions.className).not.toContain('opacity-0')
    expect(actions.className).not.toContain('group-hover:opacity-100')

    for (const id of ['todo-set-goal', 'todo-edit', 'todo-delete']) {
      const btn = screen.getAllByTestId(id)[0]
      expect(btn.className).toContain('size-8')
      expect(btn.className).not.toContain('size-5')
    }
    // 状态切换同样要有足够命中区（图标 12px → 触屏 16px + p-2 外扩）
    const toggle = screen.getAllByTestId('todo-status')[0]
    expect(toggle.className).toContain('p-2')
  })

  it('桌面：仍保持 hover 才显示（避免每行堆三个图标），图标尺寸 20px', () => {
    mockTouch(false)
    renderTray(baseItems, { onUpdateTodos: vi.fn(), onSetGoalTodo: vi.fn() })

    const actions = screen.getAllByTestId('todo-actions')[0]
    expect(actions.className).toContain('opacity-0')
    expect(actions.className).toContain('group-hover:opacity-100')
    expect(screen.getAllByTestId('todo-edit')[0].className).toContain('size-5')
    expect(screen.getAllByTestId('todo-edit')[0].className).not.toContain('size-8')
    expect(screen.getAllByTestId('todo-status')[0].className).not.toContain('p-2')
  })

  it('触屏：点操作按钮真的生效（编辑入口与删除）', () => {
    mockTouch(true)
    const onUpdateTodos = vi.fn()
    renderTray(baseItems, { onUpdateTodos, onSetGoalTodo: vi.fn() })

    fireEvent.click(screen.getAllByTestId('todo-edit')[0])    // 进编辑态
    expect(screen.getByTestId('todo-edit-input')).toBeInTheDocument()
    fireEvent.keyDown(screen.getByTestId('todo-edit-input'), { key: 'Escape' })

    fireEvent.click(screen.getAllByTestId('todo-delete')[2])  // 删掉「更新文档」
    expect(onUpdateTodos).toHaveBeenCalledTimes(1)
    const next = onUpdateTodos.mock.calls[0][0] as TodoItem[]
    expect(next.map((t) => t.text)).toEqual(['修复登录超时', '补齐单测'])
  })
})
