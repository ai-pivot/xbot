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
    if (a[i].id !== b[i].id || a[i].text !== b[i].text || a[i].done !== b[i].done) {
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
    const doneCount = todos.filter((t) => t.done).length
    const currentTask = todos.find((t) => !t.done) ?? null
    return { todos, doneCount, total, currentTask }
  }, [todos])
}
