/**
 * usePendingEdit — 会话级字段（goal / todos）的**用户编辑乐观覆盖**。
 *
 * 背景（2026-09-12 用户报告 P0）：web 编辑 goal 按 Enter 后 goal 恢复编辑前的
 * 内容、刷新才生效；todos 同类。
 *
 * 根因在显示层（AgentPanel）：
 *   ① `const goal = progressSnapshot.goal ?? goalOverride` —— 服务端快照（旧值）
 *      压过乐观覆盖（新值），用户立刻看到旧内容；
 *   ② `if (progressSnapshot.goal) setGoalOverride(undefined)` —— 只要快照有值
 *      （哪怕还是旧值）就把覆盖清掉；
 *   ③ todos 干脆没有乐观副本（只依赖后端 push），push 延迟/丢失即回退。
 *
 * 语义（服务端仍是唯一权威，覆盖只是"用户刚改过"的暂态）：
 *   - `commit(value)`：RPC 成功后写入覆盖，同时记住**提交前的快照值** before。
 *   - 覆盖生效期间渲染覆盖值（用户立刻看到自己的编辑）。
 *   - 服务端快照相对 before **发生变化**时清除覆盖 —— 要么是 push 收敛到同样的
 *     新值，要么是第三方（agent）改了值，两种都以服务端为准。
 *   - push 丢失时覆盖持续生效（不等 push），刷新后由 DB / GetActiveProgress
 *    重新水合，最终一致。
 */
import { useCallback, useEffect, useState } from 'react'

import type { GoalInfo, TodoItem } from '@/types/shared'

interface Pending<T> {
  /** 用户提交的新值（渲染这个）。 */
  value: T
  /** 提交时的服务端快照（用于判断服务端是否已给出新值）。 */
  before: T
}

export function usePendingEdit<T>(snapshot: T, equal: (a: T, b: T) => boolean) {
  const [pending, setPending] = useState<Pending<T> | null>(null)

  // 服务端快照相对提交前变化 → 采用服务端（覆盖失效）。
  useEffect(() => {
    if (!pending) return
    if (!equal(snapshot, pending.before)) setPending(null)
  }, [snapshot, pending, equal])

  const commit = useCallback(
    (value: T) => {
      setPending({ value, before: snapshot })
    },
    [snapshot],
  )

  return [pending ? pending.value : snapshot, commit] as const
}

/** goal 相等：objective + status（nullish 表示"无目标"）。 */
export function goalEqual(a: GoalInfo | null | undefined, b: GoalInfo | null | undefined): boolean {
  const an = a ?? null
  const bn = b ?? null
  if (an === null || bn === null) return an === bn
  return an.objective === bn.objective && an.status === bn.status
}

/** todos 相等：长度 + 每项 text/status（与 useTodos 的 todosEqual 同语义）。 */
export function todosListEqual(a: readonly TodoItem[], b: readonly TodoItem[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].text !== b[i].text || a[i].status !== b[i].status) return false
  }
  return true
}
