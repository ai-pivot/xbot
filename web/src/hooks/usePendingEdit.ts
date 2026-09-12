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
 *   - `begin()` → 单调序号；RPC 成功后 `commit(value, seq)` 写入覆盖（记住提交前的
 *     快照值 before）；`discard()` 放弃覆盖（发送失败 / 服务端拒绝）。
 *   - 覆盖生效期间渲染覆盖值（用户立刻看到自己的编辑）。
 *   - 覆盖清除条件（任一）：
 *       (a) 服务端快照**等于覆盖值** —— 已采纳，覆盖无意义；
 *       (b) 服务端快照相对 before **发生变化** —— push 收敛到别的值 / 第三方
 *           （agent）改了值，一律以服务端为准。
 *   - push 丢失时覆盖持续生效（不等 push），刷新后由 DB / GetActiveProgress
 *     重新水合，最终一致。
 *
 * 三条来自 CR 的硬约束（都必须保留，改动前先读）：
 *   1. **会话身份**：`key`（channel:chatID:agentChatID）变化 → 丢弃 pending。
 *      否则会话 A 的覆盖会在切到 B 后继续渲染（B 的快照恰好等于 A 的 before 时
 *      —— 最典型是"两会话都没 goal"，`null == null` → 永不自愈，B 面板显示 A 的目标）。
 *   2. **提交序号**：`begin()` 发放单调序号，`commit` 只接受**不旧于**已应用序号的
 *      提交 —— 两个并发编辑（B→C）RPC 乱序返回时落后的那个不得回写（否则界面回退
 *      到中间态，且因 before 未变而永不清除）。
 *   3. **比较器必须稳定**（模块级函数）：effect 依赖 `equal`，内联箭头函数会导致
 *      effect 每帧重跑。`todosListEqual` 对 `undefined` 安全（EMPTY 快照）。
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import type { GoalInfo, TodoItem } from '@/types/shared'

interface Pending<T> {
  /** 用户提交的新值（渲染这个）。 */
  value: T
  /** 提交时的服务端快照（用于判断服务端是否已给出新值）。 */
  before: T
  /** 提交序号（单调）—— 只接受不旧于已应用序号的提交。 */
  seq: number
}

export interface PendingEditHandle<T> {
  /** 进入一次编辑事务（分配单调序号；RPC 返回后用它调用 commit）。 */
  begin: () => number
  /** RPC 成功后写入覆盖（比已应用序号更旧的提交被丢弃）。 */
  commit: (value: T, seq: number) => void
  /** 放弃覆盖（发送失败 / 用户取消 / 服务端明确拒绝）。 */
  discard: () => void
}

export function usePendingEdit<T>(
  snapshot: T,
  equal: (a: T, b: T) => boolean,
  /** 会话身份（切换即丢弃覆盖，防跨会话串值）。 */
  key: string,
): readonly [T, PendingEditHandle<T>] {
  const [pending, setPending] = useState<Pending<T> | null>(null)
  const seqRef = useRef(0)
  const appliedSeqRef = useRef(0)

  // 会话切换：丢弃覆盖（覆盖属于"该会话的用户编辑"）。
  const keyRef = useRef(key)
  useEffect(() => {
    if (keyRef.current === key) return
    keyRef.current = key
    appliedSeqRef.current = seqRef.current
    setPending(null)
  }, [key])

  // 服务端快照变化 → 收敛（采纳 / 让位）。
  useEffect(() => {
    if (!pending) return
    if (equal(snapshot, pending.value) || !equal(snapshot, pending.before)) setPending(null)
  }, [snapshot, pending, equal])

  const begin = useCallback(() => {
    seqRef.current += 1
    return seqRef.current
  }, [])

  const commit = useCallback(
    (value: T, seq: number) => {
      // 乱序 RPC：落后（或未知）的提交不得回写。
      if (seq < appliedSeqRef.current) return
      appliedSeqRef.current = seq
      setPending({ value, before: snapshot, seq })
    },
    [snapshot],
  )

  const discard = useCallback(() => {
    appliedSeqRef.current = seqRef.current
    setPending(null)
  }, [])

  return [pending ? pending.value : snapshot, { begin, commit, discard }] as const
}

/** goal 相等：objective + status（nullish 表示"无目标"）。 */
export function goalEqual(a: GoalInfo | null | undefined, b: GoalInfo | null | undefined): boolean {
  const an = a ?? null
  const bn = b ?? null
  if (an === null || bn === null) return an === bn
  return an.objective === bn.objective && an.status === bn.status
}

/** todos 相等：长度 + 每项 text/status。对 undefined 安全（EMPTY 快照）。 */
export function todosListEqual(
  a: readonly TodoItem[] | undefined,
  b: readonly TodoItem[] | undefined,
): boolean {
  if (a === b) return true
  const an = a ?? []
  const bn = b ?? []
  if (an.length !== bn.length) return false
  for (let i = 0; i < an.length; i++) {
    if (an[i].text !== bn[i].text || an[i].status !== bn[i].status) return false
  }
  return true
}
