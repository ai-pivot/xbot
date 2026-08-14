/**
 * reduce.ts — 状态机转移表（全部业务规则集中于此，8 case 穷尽）。
 *
 * 纯函数：reduce : ChatState × DomainEvent → ChatState
 *   - 返回原引用 = 无变化（React 零渲染 —— 迟到/旧 turn 事件的统一语义）
 *   - immutable 替换 —— 永不原地修改（readonly 类型阻止）
 *
 * 不变量维护（每条标注在转移点，归纳证明见 design doc §5.4）：
 *   I1 槽位唯一   — turns: ReadonlyMap（Map key 语义）
 *   I2 committed 可渲染 — CommittedPayload 构造函数签名
 *   I3 活动唯一   — activeTurn 唯一指针；turn_started 收尸旧 active
 *   I4 迭代 append-only — iterations 仅 3 处写入（append/fold/commit union），只增
 *   I5 seq 单调（per-turn）— lastSeq 属于 activeTurn；turn_started 重置
 *   I6 无 null   — normalize 已保证（reducer 零格式防御）
 */

import type { WebIteration } from '@/types/shared'
import {
  EMPTY_LIVE,
  commitViaFold,
  commitViaText,
  initialChatState,
  nonEmptyArr,
  nonEmptyStr,
  turnID,
  type ChatState,
  type DomainEvent,
  type LiveSnapshot,
  type Turn,
  type TurnID,
} from './types'

// ─── 工具：迭代合并（I4 append-only + 权威覆盖语义） ──────────

/**
 * union 迭代按 iteration# 排序；同号时 authoritative 优先（text 的
 * progressHistory 是后端权威，覆盖 live 中可能残缺的同号快照）。
 * 长度只增不减（除非 authoritative 提供了更多/更新内容 —— 同号替换）。
 */
function mergeIterations(
  base: readonly WebIteration[],
  authoritative: readonly WebIteration[],
): readonly WebIteration[] {
  if (authoritative.length === 0) return base
  const byNum = new Map<number, WebIteration>()
  for (const it of base) byNum.set(it.iteration, it)
  for (const it of authoritative) byNum.set(it.iteration, it) // 权威覆盖同号
  return [...byNum.values()].sort((a, b) => a.iteration - b.iteration)
}

/** turn 是否有实质产出（决定收尸方式：fold commit / 空壳清除）。 */
function hasOutput(live: LiveSnapshot): boolean {
  return (
    live.content !== '' ||
    live.reasoning !== '' ||
    live.iterations.length > 0 ||
    live.genui !== ''
  )
}

const withTurn = (s: ChatState, id: TurnID, patch: (t: Turn) => Turn): ChatState => {
  const turns = new Map(s.turns)
  const t = turns.get(id)
  if (!t) return s
  turns.set(id, patch(t))
  return { ...s, turns }
}

// ─── reduce：8 case 穷尽（never 检查由 TS 判别联合保证） ───────

export function reduce(s: ChatState, ev: DomainEvent): ChatState {
  switch (ev.type) {
    // ── turn_started：收尸旧 active + 新 turn 进 live + 绑定 user ──
    case 'turn_started': {
      // I5：seq 属于 per-run —— 新 turn 重置。
      let next: ChatState = { ...s, activeTurn: ev.turnID, lastSeq: null }

      // I3 维护：收尸旧 active（唯一 live 产生点 —— 新 live 之前旧 active 必须离场）。
      if (s.activeTurn !== null && s.activeTurn !== ev.turnID) {
        const old = s.turns.get(s.activeTurn)
        if (old && old.phase.kind === 'live') {
          const folded: Turn =
            hasOutput(old.phase.data) || old.user !== null
              ? { ...old, phase: foldPhase(old.phase.data) }
              : { ...old, phase: { kind: 'frozen', data: old.phase.data } } // 无产出：定格（derive 跳过空 assistant）
          const turns = new Map(next.turns)
          turns.set(old.id, folded)
          next = { ...next, turns }
        }
      }

      // 新 turn 槽（I1：Map set —— 槽位唯一）。
      // requestID 精确绑定（V2 语义）→ turnHint 绑定（user_echo 先于
      // turn_started 到达时，echo 行带 turn_id 提示）。失配留在 pending。
      let user = null as Turn['user']
      let pending = s.pendingUsers
      let idx = -1
      if (ev.requestID !== null) {
        idx = s.pendingUsers.findIndex((u) => u.requestID === ev.requestID)
      }
      if (idx < 0) {
        idx = s.pendingUsers.findIndex((u) => u.turnHint !== undefined && u.turnHint === ev.turnID)
      }
      if (idx >= 0) {
        user = s.pendingUsers[idx]
        pending = s.pendingUsers.filter((_, i) => i !== idx)
      }
      const turns = new Map(next.turns)
      turns.set(ev.turnID, {
        id: ev.turnID,
        user,
        phase: { kind: 'live', data: { ...EMPTY_LIVE } },
        requestID: ev.requestID,
      })
      return { ...next, turns, pendingUsers: pending }
    }

    // ── iteration：仅 active turn；迭代 append-only（I4） ──
    case 'iteration': {
      if (ev.turnID !== s.activeTurn) return s // 迟到/旧 turn：引用不变，零渲染
      if (s.lastSeq !== null && ev.seq <= s.lastSeq) return s // I5：重放丢弃
      const t = s.turns.get(ev.turnID)
      if (!t || t.phase.kind !== 'live') return s

      const prev = t.phase.data
      const advanced = ev.iter > prev.iter
      const data: LiveSnapshot = {
        ...prev,
        iter: ev.iter,
        // 迭代边界：清空流式字段（新迭代从零开始）；非前进则替换。
        content: advanced ? (ev.content ?? '') : (ev.content ?? prev.content),
        reasoning: advanced ? (ev.reasoning ?? '') : (ev.reasoning ?? prev.reasoning),
        // I4：append-only 合并（dedup by iteration#，同号权威覆盖）
        iterations: mergeIterations(prev.iterations, ev.iterationsDelta),
        activeTools: ev.activeTools,
        todos: ev.todos ?? prev.todos,
        subAgents: ev.subAgents ?? prev.subAgents,
        tokenUsage: ev.tokenUsage ?? prev.tokenUsage,
      }
      // I5 基准推进：成功处理后 lastSeq = ev.seq（重放检测的比较基准）。
      const next = withTurn(s, ev.turnID, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
      return { ...next, lastSeq: ev.seq }
    }

    // ── stream：仅 active turn；全量替换（无追加/回退歧义） ──
    case 'stream': {
      if (ev.turnID !== s.activeTurn) return s
      if (ev.seq !== null && s.lastSeq !== null && ev.seq <= s.lastSeq) return s
      const t = s.turns.get(ev.turnID)
      if (!t || t.phase.kind !== 'live') return s
      const prev = t.phase.data
      const data: LiveSnapshot = {
        ...prev,
        content: ev.content !== undefined ? ev.content : prev.content,
        reasoning: ev.reasoning !== undefined ? ev.reasoning : prev.reasoning,
        streamingTools: ev.streamingTools ?? prev.streamingTools,
        genui: ev.genui !== undefined ? ev.genui : prev.genui,
      }
      // I5 基准推进（stream 可无 seq —— 仅在携带时推进）。
      const next = withTurn(s, ev.turnID, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
      return ev.seq !== null ? { ...next, lastSeq: ev.seq } : next
    }

    // ── phase_done：仅 active turn；fold 最后迭代（T3 根治点）+ 停流 ──
    case 'phase_done': {
      if (ev.turnID !== s.activeTurn) return s
      if (s.lastSeq !== null && ev.seq <= s.lastSeq) return s
      const t = s.turns.get(ev.turnID)
      if (!t || t.phase.kind !== 'live') return s
      const prev = t.phase.data
      // I4：finalIteration（后端 recordFinalIteration 补记的最后迭代）fold 进
      // iterations —— text 到达前它已在 committed 路径的数据里（不依赖 text 重建）。
      const data: LiveSnapshot = {
        ...prev,
        iterations: ev.finalIteration
          ? mergeIterations(prev.iterations, [ev.finalIteration])
          : prev.iterations,
        streaming: false,
        todos: ev.todos ?? prev.todos,
      }
      // I5 基准推进。
      const next = withTurn(s, ev.turnID, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
      return { ...next, lastSeq: ev.seq }
    }

    // ── text_final：权威 finalizer —— live/frozen → committed（I2 构造） ──
    case 'text_final': {
      // turnID 为 null（legacy 无归属）→ 尝试 activeTurn；两者皆空 → 不动。
      const target = ev.turnID !== null ? ev.turnID : s.activeTurn
      if (target === null) return s
      const t = s.turns.get(target)
      if (!t) return s

      if (t.phase.kind === 'committed') return s // 已提交（重放）—— 幂等

      // live / frozen → committed。cancelled 时保留 cancel 定格内容作为 fold content。
      const live = t.phase.data
      // T3 + 权威：iterations = union(live.iterations, progressHistory)，
      // 同号 progressHistory 覆盖（后端权威），append-only。
      const iterations = mergeIterations(live.iterations, ev.progressHistory)
      // 最终回复文本：text 顶层 content（v55 唯一权威值）> cancel 定格 content。
      const finalText = ev.content !== null ? ev.content : nonEmptyStr(live.content)

      let payload
      if (finalText !== null) {
        payload = commitViaText(finalText, iterations)
      } else {
        const nonEmptyIts = nonEmptyArr(iterations)
        if (nonEmptyIts === null) {
          // 完全无产出（text 也空、iterations 也空）—— frozen 定格。
          // I2：不可构造空 committed。该 turn 渲染 user 行（若有）。
          // I3 修复：活动指针必须同步清空 —— text_final 是 turn 终态事件，
          // 即使无产出也要结束该 turn（性质测试 seed=777 抓出：activeTurn
          // 残留指向 frozen turn，后续 iteration 事件因 kind!=='live' 被静默
          // 丢弃，而 I3 断言失败）。
          if (t.phase.kind === 'frozen') {
            return s.activeTurn === target ? { ...s, activeTurn: null } : s
          }
          const frozenTurns = new Map(s.turns)
          frozenTurns.set(target, { ...t, phase: { kind: 'frozen', data: live } })
          const activeTurn = s.activeTurn === target ? null : s.activeTurn
          return { ...s, turns: frozenTurns, activeTurn }
        }
        payload = commitViaFold(nonEmptyIts, live.content)
      }

      const turns = new Map(s.turns)
      turns.set(target, { ...t, phase: { kind: 'committed', payload } })
      // commit 后：若这是 active turn，活动指针清空（turn 结束）。
      const activeTurn = s.activeTurn === target ? null : s.activeTurn
      return { ...s, turns, activeTurn }
    }

    // ── session：busy/idle —— idle 是 live 的收尾兜底（幽灵行灭绝） ──
    case 'session': {
      if (ev.busy) return s.busy ? s : { ...s, busy: true }
      // idle：active live 的兜底收尾。
      if (s.activeTurn === null) return s.busy ? { ...s, busy: false } : s
      const t = s.turns.get(s.activeTurn)
      if (!t || t.phase.kind !== 'live') return s.busy ? { ...s, busy: false } : s
      const live = t.phase.data
      if (hasOutput(live)) {
        // 有产出：frozen 定格（text 迟到仍可 commit —— turnID 匹配 frozen）。
        const turns = new Map(s.turns)
        turns.set(t.id, { ...t, phase: { kind: 'frozen', data: { ...live, streaming: false } } })
        return { ...s, turns, activeTurn: null, busy: false }
      }
      // 无产出：删槽（空壳行灭绝）+ pendingUsers 保留（user 行仍渲染）。
      const turns = new Map(s.turns)
      turns.delete(t.id)
      return { ...s, turns, activeTurn: null, busy: false }
    }

    // ── history_replaced：全量替换（reload / hydration / rewind —— 同一转移） ──
    case 'history_replaced': {
      const turns = new Map<TurnID, Turn>()
      for (const t of ev.turns) turns.set(t.id, t)
      return {
        chatID: s.chatID,
        turns,
        legacy: ev.legacy,
        activeTurn: ev.active ? ev.active.turnID : null,
        lastSeq: ev.lastSeq,
        busy: s.busy,
        // 切换会话/首屏：pending 清空（乐观行属于旧 chat —— Bug 2 根治点）。
        pendingUsers: [],
      }
    }

    // ── user_sent：乐观行入 pending 队列 ──
    case 'user_sent': {
      return { ...s, pendingUsers: [...s.pendingUsers, ev.row] }
    }

    // ── user_echo：后端权威回声（带 turn_id）。turn 已存在 → 直接挂 user；
    //     turn 未创建（echo 先于 turn_started）→ 入 pending（turnHint 绑定）。 ──
    case 'user_echo': {
      const hint = ev.row.turnHint
      if (hint !== undefined) {
        const tid = turnID(hint)
        const t = s.turns.get(tid)
        if (t && t.user === null) {
          return withTurn(s, tid, (tt) => ({ ...tt, user: ev.row }))
        }
      }
      // 去重：同 requestID 的乐观行已被 echo 取代（echo 是权威）。
      const filtered = ev.row.requestID !== null
        ? s.pendingUsers.filter((u) => u.requestID !== ev.row.requestID)
        : s.pendingUsers
      return { ...s, pendingUsers: [...filtered, ev.row] }
    }

    // ── user_ack：requestID 匹配回填 dbID（pending 或已绑定 turn.user） ──
    case 'user_ack': {
      const idx = s.pendingUsers.findIndex((u) => u.requestID === ev.requestID)
      if (idx >= 0) {
        const pendingUsers = s.pendingUsers.slice()
        pendingUsers[idx] = { ...pendingUsers[idx], dbID: ev.dbID, sending: false, queued: false }
        return { ...s, pendingUsers }
      }
      // 已绑定进 turn 的 user。
      for (const t of s.turns.values()) {
        if (t.user?.requestID === ev.requestID) {
          return withTurn(s, t.id, (tt) =>
            tt.user ? { ...tt, user: { ...tt.user, dbID: ev.dbID, sending: false, queued: false } } : tt,
          )
        }
      }
      return s
    }
  }
}

// ─── foldPhase：live 数据 → committed/frozen（turn_started 收尸） ──

function foldPhase(data: LiveSnapshot): Turn['phase'] {
  const its = nonEmptyArr(data.iterations)
  if (its !== null) return { kind: 'committed', payload: commitViaFold(its, data.content) }
  const text = nonEmptyStr(data.content)
  if (text !== null) return { kind: 'committed', payload: commitViaText(text, []) }
  // 无任何产出：frozen 定格（derive 跳过空 assistant 行；user 行保留）。
  return { kind: 'frozen', data }
}

export { initialChatState }
