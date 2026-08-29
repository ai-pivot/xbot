/**
 * useTodos — derives TODO display state from a ProgressSnapshot's todos field.
 *
 * Mirrors the TUI's todosEqual change detection: only re-derives when the
 * todo slice actually changes (id, text, done), preventing unnecessary
 * re-renders on every progress frame.
 */
import { useMemo } from 'react'
import type { TodoItem } from '@/types/shared'

/** Compare two todo slices for equality (same as TUI's todosEqual). */
export function todosEqual(a: TodoItem[], b: TodoItem[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].id !== b[i].id || a[i].text !== b[i].text || a[i].status !== b[i].status) {
      return false
    }
  }
  return true
}

export interface TodoState {
  /** All todo items. */
  todos: TodoItem[]
  /** Number of completed todos. */
  doneCount: number
  /** Total number of todos. */
  total: number
  /** First incomplete todo (the "current task"), or null if all done. */
  currentTask: TodoItem | null
}

export function useTodos(todos: TodoItem[]): TodoState {
  return useMemo(() => {
    const total = todos.length
    // ⚠️ t.status 是 string（"pending"/"doing"/"done"），不是 boolean！
    // filter(t => t.status) 对任何非空 string 都是 truthy（包括 "pending"）
    // → doneCount === total → "4/4 全部完成"但展开全是圆圈（用户报告 P0）。
    const doneCount = todos.filter((t) => t.status === 'done').length
    const currentTask = todos.find((t) => t.status !== 'done') ?? null
    return { todos, doneCount, total, currentTask }
  }, [todos])
}
