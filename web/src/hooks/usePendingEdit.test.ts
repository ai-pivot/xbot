/**
 * usePendingEdit 单测 —— 会话级字段乐观覆盖的三条 CR 硬约束：
 *   1. 会话身份（key 变化丢弃覆盖，防跨会话串值）；
 *   2. 提交序号（乱序 RPC 的落后提交不得回写）；
 *   3. 收敛语义（快照 == 覆盖值 → 采纳；快照相对 before 变化 → 让位）。
 */
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { goalEqual, todosListEqual, usePendingEdit } from './usePendingEdit'
import type { TodoItem } from '@/types/shared'

const goal = (objective: string) => ({ objective, status: 'active' })

describe('usePendingEdit', () => {
  it('commit 后渲染覆盖值；快照等于覆盖值（已采纳）→ 清除', () => {
    const { result, rerender } = renderHook(
      ({ snap }: { snap: ReturnType<typeof goal> | null }) => usePendingEdit(snap, goalEqual, 'web:c1'),
      { initialProps: { snap: goal('A') } },
    )
    act(() => result.current[1].commit(goal('B'), result.current[1].begin()))
    expect(result.current[0]?.objective).toBe('B')

    // 服务端采纳 B → 覆盖清除，仍显示 B（来源变成快照）
    rerender({ snap: goal('B') })
    expect(result.current[0]?.objective).toBe('B')
  })

  it('快照相对 before 变化（第三方改值）→ 让位给服务端', () => {
    const { result, rerender } = renderHook(
      ({ snap }: { snap: ReturnType<typeof goal> | null }) => usePendingEdit(snap, goalEqual, 'web:c1'),
      { initialProps: { snap: goal('A') } },
    )
    act(() => result.current[1].commit(goal('B'), result.current[1].begin()))
    expect(result.current[0]?.objective).toBe('B')
    rerender({ snap: goal('C') })
    expect(result.current[0]?.objective).toBe('C')
  })

  it('无 push（快照不变）→ 覆盖持续生效（不回退）', () => {
    const { result, rerender } = renderHook(
      ({ snap }: { snap: ReturnType<typeof goal> | null }) => usePendingEdit(snap, goalEqual, 'web:c1'),
      { initialProps: { snap: goal('A') } },
    )
    act(() => result.current[1].commit(goal('B'), result.current[1].begin()))
    rerender({ snap: goal('A') })
    expect(result.current[0]?.objective).toBe('B')
  })

  it('乱序 RPC：落后的提交不得回写（B→C，seq1 后到）', () => {
    const { result } = renderHook(() => usePendingEdit(goal('A'), goalEqual, 'web:c1'))
    let seqB = 0
    act(() => {
      seqB = result.current[1].begin()
    })
    let seqC = 0
    act(() => {
      seqC = result.current[1].begin()
    })
    act(() => result.current[1].commit(goal('C'), seqC))
    expect(result.current[0]?.objective).toBe('C')
    // 落后提交（seqB < seqC）被丢弃
    act(() => result.current[1].commit(goal('B'), seqB))
    expect(result.current[0]?.objective).toBe('C')
  })

  it('会话切换（key 变化）→ 丢弃覆盖（防跨会话串值）', () => {
    const snapshot = goal('A')
    const { result, rerender } = renderHook(
      ({ key }: { key: string }) => usePendingEdit(snapshot, goalEqual, key),
      { initialProps: { key: 'web:c1' } },
    )
    act(() => result.current[1].commit(goal('B'), result.current[1].begin()))
    expect(result.current[0]?.objective).toBe('B')
    // 切到会话 2：覆盖被丢弃 → 渲染回快照（真实场景里 B 的快照是 null → banner 消失）
    rerender({ key: 'web:c2' })
    expect(result.current[0]?.objective).toBe('A')
  })

  it('discard() 放弃覆盖（发送失败回滚）', () => {
    const { result } = renderHook(() => usePendingEdit(goal('A'), goalEqual, 'web:c1'))
    act(() => result.current[1].commit(goal('B'), result.current[1].begin()))
    expect(result.current[0]?.objective).toBe('B')
    act(() => result.current[1].discard())
    expect(result.current[0]?.objective).toBe('A')
  })
})

describe('goalEqual / todosListEqual', () => {
  it('goalEqual：nullish 等价，objective+status 比较', () => {
    expect(goalEqual(null, undefined)).toBe(true)
    expect(goalEqual(null, goal('A'))).toBe(false)
    expect(goalEqual(goal('A'), { objective: 'A', status: 'active' })).toBe(true)
    expect(goalEqual(goal('A'), { objective: 'A', status: 'completed' })).toBe(false)
  })

  it('todosListEqual：undefined 安全 + 逐项 text/status', () => {
    const a: TodoItem[] = [{ text: 'x', status: 'done' }]
    expect(todosListEqual(undefined, undefined)).toBe(true)
    expect(todosListEqual(undefined, [])).toBe(true)
    expect(todosListEqual(a, a)).toBe(true)
    expect(todosListEqual(a, [{ text: 'x', status: 'pending' }])).toBe(false)
    expect(todosListEqual(a, [])).toBe(false)
  })
})
