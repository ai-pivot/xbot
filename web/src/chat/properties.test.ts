/**
 * properties.test.ts — 状态机性质测试（证明的可执行化，design doc §5 + §9.2）。
 *
 * 零依赖：种子化 LCG 伪随机生成器（失败时打印 seed —— 完全可复现）。
 *
 * 性质（∀ 随机事件序列 σ）：
 *   P1  I1-I3 不变量保持（槽位唯一 / committed 可渲染 / 活动唯一）
 *   P2  T1：deriveRows（ρ）total —— 不抛（DOM 永不消失）
 *   P3  T4：每 turn 至多一行 assistant（无 ghost 行）
 *   P4  T5：行序单调（turnID 升序，turn 内 user < assistant）
 *   P5  T3 跟踪：已进入 iterations 的迭代号集合单调不减（append-only）
 */

import { describe, expect, it } from 'vitest'
import { deriveRows } from './derive'
import { reduce } from './reduce'
import { initialChatState, iterNum, turnID, type DomainEvent } from './types'
import { assertInvariants } from './reduce.test'

// ─── 种子化 LCG（可复现） ──────────────────────────────────────

function makeRng(seed: number): () => number {
  let s = seed >>> 0
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0
    return s / 0x100000000
  }
}

// ─── 事件生成器（模拟真实 SSE 的混乱：乱序/迟到/重放/取消/切换） ──

interface GenOptions {
  readonly maxTurns: number
  readonly length: number
}

function generateEvents(rng: () => number, o: GenOptions): DomainEvent[] {
  const evs: DomainEvent[] = []
  let nextTurn = 1
  let seq = 100
  const pick = <T,>(xs: readonly T[]): T => xs[Math.floor(rng() * xs.length)]

  for (let i = 0; i < o.length; i++) {
    const kind = rng()
    if (nextTurn <= o.maxTurns && kind < 0.15) {
      // turn_started（偶尔重复发送 —— 模拟 SSE replay 的 duplicate turn_started）
      const t = turnID(nextTurn++)
      const dup = rng() < 0.2
      evs.push({ type: 'turn_started', turnID: t, requestID: rng() < 0.5 ? `req-${t}` : null, trigger: 'user' })
      if (dup) evs.push({ type: 'turn_started', turnID: t, requestID: null, trigger: 'user' })
    } else if (kind < 0.35) {
      // iteration：乱序 target（模拟迟到 —— 旧 turn 的事件）+ 重放 seq
      const t = turnID(Math.max(1, Math.floor(rng() * nextTurn)))
      const replay = rng() < 0.2
      const useSeq = replay && evs.length > 0 ? 50 : seq++ // 50 < 当前 lastSeq → 重放
      evs.push({
        type: 'iteration',
        turnID: t,
        iter: iterNum(1 + Math.floor(rng() * 3)),
        seq: useSeq as never,
        content: rng() < 0.7 ? `内容-${i}` : undefined,
        reasoning: rng() < 0.3 ? `思考-${i}` : undefined,
        activeTools: [],
        completedTools: [],
        iterationsDelta: rng() < 0.5 ? [{ iteration: 1 + Math.floor(rng() * 3), content: `迭代-${i}`, reasoning: '', tools: [], toolCount: 0 }] : [],
        todos: undefined,
        subAgents: undefined,
      })
    } else if (kind < 0.55) {
      // stream
      const t = turnID(Math.max(1, Math.floor(rng() * nextTurn)))
      evs.push({
        type: 'stream',
        turnID: t,
        seq: rng() < 0.2 ? null : (seq++ as never),
        content: rng() < 0.8 ? `流式-${i}` : undefined,
        reasoning: undefined,
        streamingTools: undefined,
        genui: undefined,
      })
    } else if (kind < 0.7) {
      // phase_done（含 finalIteration）
      const t = turnID(Math.max(1, Math.floor(rng() * nextTurn)))
      evs.push({
        type: 'phase_done',
        turnID: t,
        seq: seq++ as never,
        finalIteration: rng() < 0.6 ? { iteration: 1 + Math.floor(rng() * 3), content: `最终迭代-${i}`, reasoning: '', tools: [], toolCount: 0 } : null,
        todos: undefined,
      })
    } else if (kind < 0.85) {
      // text_final（正常/取消/无归属）
      const hasTurn = rng() < 0.8
      const t = hasTurn ? turnID(Math.max(1, Math.floor(rng() * nextTurn))) : null
      const hasContent = rng() < 0.7
      evs.push({
        type: 'text_final',
        turnID: t,
        content: hasContent ? (`最终回复-${i}` as never) : null,
        progressHistory: rng() < 0.5 ? [{ iteration: 1 + Math.floor(rng() * 3), content: `历史迭代-${i}`, reasoning: '', tools: [], toolCount: 0 }] : [],
        cancelled: rng() < 0.3,
      })
    } else if (kind < 0.95) {
      evs.push({ type: 'session', busy: rng() < 0.5 })
    } else {
      // user_sent（乐观行）
      evs.push({
        type: 'user_sent',
        row: {
          id: `u-${i}`,
          content: `用户消息-${i}` as never,
          timestamp: `2026-01-01T00:00:${String(i % 60).padStart(2, '0')}Z`,
          isNotification: false,
          queued: false,
          sending: false,
          requestID: `req-${i}`,
          dbID: undefined,
        },
      })
    }
  }
  return evs
}

// ─── 迭代跟踪器（P5：观察到的迭代号集合单调不减） ──────────────

function observedIterations(state: ReturnType<typeof initialChatState>): Map<number, Set<number>> {
  const m = new Map<number, Set<number>>()
  for (const t of state.turns.values()) {
    const its = t.phase.kind === 'committed' ? t.phase.payload.iterations : t.phase.data.iterations
    if (!m.has(t.id)) m.set(t.id, new Set())
    for (const it of its) m.get(t.id)!.add(it.iteration)
  }
  return m
}

function isSubset(a: Map<number, Set<number>>, b: Map<number, Set<number>>): boolean {
  for (const [turn, iters] of a) {
    if (iters.size === 0) continue // 空 turn 槽被删（idle 清壳）≠ 迭代丢失
    const target = b.get(turn)
    if (!target) return false
    for (const it of iters) if (!target.has(it)) return false
  }
  return true
}

// ─── 性质测试 ─────────────────────────────────────────────────

describe('TDSM 性质测试（P1-P5，种子可复现）', () => {
  const SEEDS = [1, 42, 2026, 777, 31415, 99991, 123456, 88888888]

  for (const seed of SEEDS) {
    it(`seed=${seed}: I1-I6 保持 + ρ total + 每 turn ≤1 行 + 迭代 append-only`, () => {
      const rng = makeRng(seed)
      const events = generateEvents(rng, { maxTurns: 5, length: 400 })
      let state = initialChatState('chat-1')
      let prevObserved = observedIterations(state)

      for (let i = 0; i < events.length; i++) {
        const before = state
        state = reduce(state, events[i])

        // P1：I1-I3 不变量（每步检查 —— 归纳法的可执行化）。
        assertInvariants(state)

        // P2（T1）：deriveRows total —— 任何一步都不抛（DOM 永不消失）。
        let rows: ReturnType<typeof deriveRows>
        try {
          rows = deriveRows(state)
        } catch (e) {
          throw new Error(
            `ρ not total at step ${i} (event=${events[i].type}, seed=${seed}): ${String(e)}`,
          )
        }

        // P3（T4）：每 turn 至多一行 assistant。
        const assistantByTurn = new Map<number, number>()
        for (const r of rows) {
          if (r.kind !== 'user') {
            assistantByTurn.set(r.turnID, (assistantByTurn.get(r.turnID) ?? 0) + 1)
          }
        }
        for (const [t, n] of assistantByTurn) {
          if (n > 1) {
            throw new Error(`ghost rows: turn ${t} has ${n} assistant rows at step ${i}, seed=${seed}`)
          }
        }

        // P4（T5）：turn 段行序 turnID 严格升序；turn 内 user < assistant。
        const turnRows = rows.filter((r) => r.turnID > 0)
        for (let j = 1; j < turnRows.length; j++) {
          if (turnRows[j].turnID < turnRows[j - 1].turnID) {
            throw new Error(`row order violated at step ${i}, seed=${seed}`)
          }
        }
        for (let j = 1; j < turnRows.length; j++) {
          if (
            turnRows[j].turnID === turnRows[j - 1].turnID &&
            turnRows[j].kind === 'user' &&
            turnRows[j - 1].kind !== 'user'
          ) {
            throw new Error(`user after assistant in turn at step ${i}, seed=${seed}`)
          }
        }

        // P5（T3 跟踪）：已观察的迭代号集合单调不减。
        const nowObserved = observedIterations(state)
        if (!isSubset(prevObserved, nowObserved)) {
          const lost: string[] = []
          for (const [t, iters] of prevObserved) {
            for (const it of iters) if (!nowObserved.get(t)?.has(it)) lost.push(`turn${t}#iter${it}`)
          }
          throw new Error(
            `iteration disappeared (T3 violated) at step ${i} (event=${events[i].type}, seed=${seed}): lost [${lost.join(', ')}]`,
          )
        }
        prevObserved = nowObserved
        void before
      }
    })
  }
})
