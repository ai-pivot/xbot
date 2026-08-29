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

import type { WebIteration, WebToolProgress } from '@/types/shared'
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
  type IterNum,
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

/** LiveSnapshot['streamStats'] 的字段级合并。
 *
 * 后端 stream_stats 帧在滑动窗口数据不足（帧间 <200ms / 增量 0）时会带
 * tokens_per_sec=0 / ttft_ms=0 —— 这是"无数据"而非"速度为 0"。整体覆盖
 * 会把上一帧的有效 tkps 清零（前端数字消失只剩 "streaming"）。
 *
 * 规则：
 *  - ttftMs：per-Run 不变（后端闭包 firstChunkAt - requestStartAt 固定），
 *    迭代前进也不重置 —— 逐字段合并（>0 才覆盖，0/undefined 保留 prev）。
 *  - tokensPerSec：迭代前进时重置为 0（新迭代从零开始）；非前进时逐字段
 *    合并（>0 才覆盖，0/undefined 保留 prev —— "没数据用本迭代之前的数据"）。
 *  - 其余字段（tpotMs/totalMs/chunks）：逐字段合并。
 */
function mergeStreamStats(
  prev: LiveSnapshot['streamStats'],
  next: LiveSnapshot['streamStats'] | undefined,
  advanced: boolean,
): LiveSnapshot['streamStats'] {
  // 无新数据帧：保留前一帧。
  if (!next) return prev ?? null
  if (!prev) return next // 首帧：next 即权威值（advanced 时也如此 —— prev 为 null 无需重置）
  // ttftMs：per-Run 不变 —— 永远逐字段合并（>0 才覆盖），迭代前进也不重置。
  // tokensPerSec：迭代前进时重置为 0（新迭代从零开始）；非前进时逐字段合并。
  const tokensPerSec = advanced ? 0 : (next.tokensPerSec > 0 ? next.tokensPerSec : prev.tokensPerSec)
  return {
    ttftMs: next.ttftMs > 0 ? next.ttftMs : prev.ttftMs,
    tpotMs: next.tpotMs > 0 ? next.tpotMs : prev.tpotMs,
    tokensPerSec,
    totalMs: next.totalMs > 0 ? next.totalMs : prev.totalMs,
    chunks: next.chunks > 0 ? next.chunks : prev.chunks,
  }
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

/**
 * lazyAdoptLive — 无 active turn 时把 turn 槽创建/升级为 live（打字机的
 * 宽容语义）。空壳占位（frozen 无输出，user-only 历史行组成）会被升级
 * —— 空壳没有可保数据，它只是"DB 有 user 行"的占位符。
 *
 * 场景（用户报告："切换或刷新后只显示 history，live progress 不显示"）：
 *   切回 → fetchHistory → turn 57 的 user 行组成 frozen 空壳 → active_progress
 *   恢复分支见 existing 存在就跳过 → activeTurn=null → stream 事件
 *   turns.get(57) 是 frozen 非 live → 丢弃 → 永远只显示 history。
 */
function lazyAdoptLive(s: ChatState, id: TurnID, snapshot?: LiveSnapshot): ChatState {
  const prev = s.turns.get(id)
  const turns = new Map(s.turns)
  turns.set(id, {
    id,
    user: prev?.user ?? null,
    phase: { kind: 'live', data: snapshot ? { ...snapshot } : { ...EMPTY_LIVE } },
    requestID: prev?.requestID ?? null,
  })
  return { ...s, turns, activeTurn: id, lastSeq: null }
}

/** frozen 空壳判定：无任何输出（占位符，非终态数据）。 */
function isHollowFrozen(t: Turn | undefined): boolean {
  return !!t && t.phase.kind === 'frozen' && !hasOutput(t.phase.data)
}

/** stream 事件携带实质载荷（活动证据）：内容/思考/genui/流式工具任一非空。 */
function hasStreamEvidence(ev: { content?: string; reasoning?: string; genui?: string; streamingTools?: readonly unknown[] }): boolean {
  return (
    (ev.content !== undefined && ev.content !== '') ||
    (ev.reasoning !== undefined && ev.reasoning !== '') ||
    (ev.genui !== undefined && ev.genui !== '') ||
    (ev.streamingTools !== undefined && ev.streamingTools.length > 0)
  )
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
      // notification trigger：turn_start.content 携带通知内容（后端
      // TurnStartInfo）。弱网下 inject_user WS 消息丢失时，turn_started 是
      // 通知内容的唯一载体 —— pending 未命中则用 content 构造通知 user 行
      //（isNotification → 🔔 badge + muted style），否则用户只看到"思考中"
      // 看不到 system notification 本身（用户报告）。通知无 REST 请求
      //（requestID=null），迟到 inject_user 走 useChatMessages →
      // history_replaced 过滤（dbID undefined），不会双行。
      // F#10：nonEmptyStr smart constructor 收窄为 NonEmptyS（原 `as never`
      // 绕过 branded 类型 —— no-as 规则）。
      const notifContent = ev.trigger === 'notification' ? nonEmptyStr(ev.content) : null
      if (user === null && notifContent !== null) {
        user = {
          id: `notif-${ev.turnID}`,
          content: notifContent,
          timestamp: new Date().toISOString(),
          isNotification: true,
          queued: false,
          sending: false,
          requestID: null,
          turnHint: undefined,
          dbID: undefined,
        }
      }
      // ⚠️ 已存在同 ID turn：live（lazy 采纳过）保留数据；frozen 空壳
      // （user-only 占位）升级为 live（turn_started 是权威开始信号）；committed
      // /有输出 frozen 嫁接 user 不动 phase（I3：指针只指 live，保持原值）。
      // 覆盖为空 live 会抹掉已渲染进度（性质测试 seed=1/42 抓出 T3 violated）。
      const existing = next.turns.get(ev.turnID)
      const turns = new Map(next.turns)
      if (existing) {
        if (existing.phase.kind === 'live') {
          turns.set(ev.turnID, {
            id: ev.turnID,
            user: existing.user ?? user,
            phase: existing.phase,
            requestID: existing.requestID,
          })
          return { ...next, turns, pendingUsers: pending, activeTurn: ev.turnID }
        }
        if (existing.phase.kind === 'frozen' && !hasOutput(existing.phase.data)) {
          // 空壳占位 → 升级为 live（turn_started 是权威开始信号）。
          turns.set(ev.turnID, {
            id: ev.turnID,
            user: existing.user ?? user,
            phase: { kind: 'live', data: existing.phase.data },
            requestID: existing.requestID,
          })
          return { ...next, turns, pendingUsers: pending, activeTurn: ev.turnID }
        }
        // committed / 有输出 frozen：嫁接 user，不动 phase（I3：指针只指
        // live，保持原值 —— next 已被预改，须显式回滚 activeTurn/lastSeq）。
        turns.set(ev.turnID, existing.user ? existing : { ...existing, user: user ?? existing.user })
        return { ...next, turns, pendingUsers: pending, activeTurn: s.activeTurn, lastSeq: s.lastSeq }
      }
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
      // turnID null（turn_id=0 缺失）→ 回退 activeTurn（与 stream 一致）。
      // todos 是会话级状态，不因 turn 缺失而丢弃。
      const target = ev.turnID !== null ? ev.turnID : s.activeTurn
      if (target === null) {
        return ev.todos !== undefined ? { ...s, todos: ev.todos } : s
      }
      if (target !== s.activeTurn) {
        const t0 = s.turns.get(target)
        // ⚠️ committed 遮蔽解除（用户报告："sse 不断收到新消息但前端渲染
        // 不变"，dump 铁证：turn-108-c committed 只含 iteration 1，SSE 还在
        // 发 iteration 22）：DB 增量持久化的中间迭代经 history merge 组成
        // committed turn，遮蔽仍在运行的 live。后端不会对已结束的 turn 发
        // 新迭代 —— 迭代事件是活动的权威证据：incoming 迭代号 > committed
        // 已有最大迭代 → 升级回 live（迭代 union 保留，content 用事件值）。
        if (t0 && t0.phase.kind === 'committed' && s.activeTurn === null) {
          const maxIter = t0.phase.payload.iterations.reduce((m, it) => Math.max(m, it.iteration), 0)
          if (ev.iter > maxIter) {
            const live: LiveSnapshot = {
              ...EMPTY_LIVE,
              iter: ev.iter,
              content: t0.phase.payload.content,
              iterations: t0.phase.payload.iterations,
            }
            const turns = new Map(s.turns)
            turns.set(target, { ...t0, phase: { kind: "live", data: live } })
            s = { ...s, turns, activeTurn: target, lastSeq: null }
          } else {
            return s // 已含该迭代的 committed 快照 —— 重放，丢弃
          }
        } else {
          if (s.activeTurn !== null && t0?.phase.kind !== 'live') return s
          if (s.activeTurn !== null && t0?.phase.kind === 'live' && s.activeTurn !== target) return s
          if (t0 && !isHollowFrozen(t0)) return s // committed/有输出 frozen —— 重放，丢弃
          // 无槽（或空壳占位）且无 active → lazy 采纳/升级（切回会话场景）。
          s = lazyAdoptLive(s, target)
        }
      }
      if (s.lastSeq !== null && ev.seq !== null && ev.seq <= s.lastSeq) return s // I5：重放丢弃（null seq 无基准，不比较）
      const t = s.turns.get(target)
      if (!t || t.phase.kind !== 'live') return s

      const prev = t.phase.data
      const advanced = ev.iter > prev.iter
      // ── 迭代 commit（history append 且 iteration 未前进）──
      // 事件 A（snapshotCompletedIteration）：iterationsDelta append 了刚完成
      // 的迭代，但 ev.iter 还停在 N（前进事件后到）。live 的流式文本已随迭代
      // 进入 iterations（权威版本）——必须同步清空，否则同一 content/reasoning
      // 在 committed fold 和 live fold 各渲染一份，直到前进事件到达（用户报告：
      // "每次新的 iter 完成都要闪烁一下"）。防护：gap 修复 delta 可能携带旧迭代
      // 而 live 已在流式更新的迭代 —— 仅当 live 属于刚 commit 的迭代
      // （ev.iter <= appendedMax）才清。
      const merged = mergeIterations(prev.iterations, ev.iterationsDelta)
      const appendedNew = merged.length > prev.iterations.length
      const appendedMax = ev.iterationsDelta.length > 0
        ? Math.max(...ev.iterationsDelta.map((it) => it.iteration))
        : 0
      // committedNow = "刚 commit 的迭代" 恰是 live 当前迭代（delta 补的恰好是
      // prev.iter 这一个）。用 appendedMax === prev.iter 判断 —— 若 delta 补的是
      // 更早的迭代（gap 修复，appendedMax < prev.iter），live 仍在流式更新当前
      // 迭代，绝不能清空其 content/reasoning（用户报告：缺迭代补上时当前流被
      // 重置）。旧实现 `ev.iter <= appendedMax` 在 gap 场景（ev.iter 落后）误判为
      // commit，清空 live 流式内容。
      const committedNow = !advanced && appendedNew && appendedMax === prev.iter
      const data: LiveSnapshot = {
        ...prev,
        iter: ev.iter,
        // 迭代边界：清空流式字段（新迭代从零开始）；commit 同样清空（已进
        // iterations 权威版本）；非前进非 commit 则替换。
        content: advanced
          ? (ev.content ?? '')
          : committedNow
            ? ''
            : (ev.content ?? prev.content),
        reasoning: advanced
          ? (ev.reasoning ?? '')
          : committedNow
            ? ''
            : (ev.reasoning ?? prev.reasoning),
        // I4：append-only 合并（dedup by iteration#，同号权威覆盖）
        iterations: merged,
        activeTools: ev.activeTools,
        // 工具去重（旧前端 mergeProgressState 语义）：工具从 generating 转
        // running 时，stream 事件残留的同名 streamingTools 条目必须清除 ——
        // 否则同一工具渲染两个（一个 executing 带参数 + 一个 generating 无
        // 参数，用户报告 100% 复现）。规则：streamingTools ∩ activeTools = ∅。
        // 迭代前进或 commit 时全部清空（流式字段随迭代边界重置 —— 旧语义）。
        streamingTools: advanced || committedNow
          ? []
          : prev.streamingTools.filter(
              (t) => !ev.activeTools.some((a) => a.name === t.name),
            ),
        genui: advanced || committedNow ? '' : prev.genui,
        todos: ev.todos ?? prev.todos,
        subAgents: ev.subAgents ?? prev.subAgents,
        tokenUsage: ev.tokenUsage ?? prev.tokenUsage,
        streamStats: ev.streamStats ?? prev.streamStats,
      }
      // I5 基准推进：成功处理后 lastSeq = ev.seq（重放检测的比较基准）。
      // 会话级 todos：事件携带时同步 state.todos（turn 结束后存活）。
      const next = withTurn(s, target, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
      return { ...next, lastSeq: ev.seq, todos: ev.todos ?? s.todos }
    }

    // ── stream：仅 active turn；全量替换（无追加/回退歧义） ──
    // ⚠️ 不做 seq gate：stream 是【累积全量推送】（delta_push 默认关闭），
    // 旧前端明确把 stream 字段处理放在 seq 检查【之前】（"stream deltas are
    // cumulative, not ordered by seq"）。seq gate 会按到达序误杀打字机帧。
    // ⚠️ turnID 缺失（后端 gap）回退 activeTurn —— 事件属于当前流。
    // ⚠️ lazy 采纳：切回会话（turn_started 已过、active_progress 未恢复/
    //   失败）时从 stream 事件重建 live turn —— 否则事件永远被丢弃
    //   （"切回来看不到任何新进度"）。
    case 'stream': {
      const target = ev.turnID !== null ? ev.turnID : s.activeTurn
      if (target === null) return s
      if (s.turns.has(target)) {
        const t0 = s.turns.get(target)!
        if (t0.phase.kind !== 'live') {
          // committed 遮蔽解除（同 iteration case）：DB 中间快照组成的
          // committed 收到流式事件（活动证据）→ 升级回 live（迭代保留）。
          // 带内容/载荷的 stream 事件只可能属于运行中的 turn。
          if (t0.phase.kind === 'committed' && s.activeTurn === null && hasStreamEvidence(ev)) {
            const live: LiveSnapshot = {
              ...EMPTY_LIVE,
              content: t0.phase.payload.content,
              iterations: t0.phase.payload.iterations,
            }
            const turns = new Map(s.turns)
            turns.set(target, { ...t0, phase: { kind: 'live', data: live } })
            s = { ...s, turns, activeTurn: target }
          } else if (isHollowFrozen(t0) && s.activeTurn === null) {
            // 空壳占位（user-only 历史行）→ 升级为 live（流式事件是活动的证据）。
            s = lazyAdoptLive(s, target)
          } else {
            return s
          }
        }
      } else {
        if (s.activeTurn !== null) return s // 已有别的活动 turn —— 事件属旧 turn，丢弃
        s = lazyAdoptLive(s, target)
      }
      const t = s.turns.get(target)
      if (!t || t.phase.kind !== 'live') return s
      const prev = t.phase.data
      // 迭代前进（后端 stamp 的 iteration > 当前 iter）：清空流式字段 —— 否则
      // 迭代 N+1 的 stream 到达时，若 content 尚未产出，迭代 N 的旧 content
      // /reasoning 残留到新迭代（"老 content 到新迭代"竞态，用户报告）。
      const advanced = ev.iteration !== null && ev.iteration > prev.iter
      const data: LiveSnapshot = {
        ...prev,
        iter: advanced ? ev.iteration : prev.iter,
        content: advanced ? (ev.content ?? '') : (ev.content !== undefined ? ev.content : prev.content),
        reasoning: advanced ? (ev.reasoning ?? '') : (ev.reasoning !== undefined ? ev.reasoning : prev.reasoning),
        // 工具去重（同名双渲染根治）：streamingTools 是流式检测中的工具
        // （generating，参数不全），activeTools 是结构化事件的执行中工具
        // （running，参数全）。同名共存 → 同一工具渲染两个（用户报告
        // 100% 复现）。规则：streamingTools ∩ activeTools = ∅（旧前端
        // mergeProgressState 同款过滤）。迭代前进时全部清空（流式字段随
        // 迭代边界重置）。
        streamingTools: advanced
          ? []
          : ev.streamingTools !== undefined
            ? ev.streamingTools.filter((t2) => !prev.activeTools.some((a) => a.name === t2.name))
            : prev.streamingTools,
        genui: ev.genui !== undefined ? ev.genui : prev.genui,
        // 实时流式时序（stream_stats）：每个 stream SSE 帧都携带 —— live
        // 据此实时更新 tkps（此前只在 iteration 事件解析，流式帧丢弃导致
        // "到达太晚 + 每迭代不变"）。
        // ⚠️ 字段级合并，不是整体覆盖：后端滑动窗口在帧间不足 200ms 或增量
        // 为 0 时会回传 tokensPerSec=0（"无数据"而非"速度为 0"）——整体覆盖
        // 会把上一帧的有效 tkps 清零 → 前端数字消失只剩 "streaming"。只有
        // 新帧提供了 >0 的字段才更新该字段，0/undefined 保留前一帧。
        // 迭代前进（advanced）时随流式字段一起重置（新迭代从零开始）。
        streamStats: mergeStreamStats(prev.streamStats, ev.streamStats, advanced),
      }
      return withTurn(s, target, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
    }

    // ── phase_done：仅 active turn；fold 最后迭代（T3 根治点）+ 停流 ──
    case 'phase_done': {
      // turnID null（turn_id=0 缺失）→ 回退 activeTurn；todos 会话级不丢弃。
      const target = ev.turnID !== null ? ev.turnID : s.activeTurn
      if (target === null) return ev.todos !== undefined ? { ...s, todos: ev.todos } : s
      if (target !== s.activeTurn) return ev.todos !== undefined ? { ...s, todos: ev.todos } : s
      if (s.lastSeq !== null && ev.seq !== null && ev.seq <= s.lastSeq) return ev.todos !== undefined ? { ...s, todos: ev.todos } : s
      const t = s.turns.get(target)
      if (!t || t.phase.kind !== 'live') return ev.todos !== undefined ? { ...s, todos: ev.todos } : s
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
      // I5 基准推进。会话级 todos：事件携带时同步（turn 结束后存活）。
      const next = withTurn(s, target, (tt) => ({ ...tt, phase: { kind: 'live', data } }))
      return { ...next, lastSeq: ev.seq, todos: ev.todos ?? s.todos }
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
      const iterations0 = mergeIterations(live.iterations, ev.progressHistory)
      // cancel：正在执行的工具（activeTools/streamingTools，从未完成 ——
      // progress_history 不含它们）折进最后迭代（标 error）—— "已渲染内容
      // 永不消失"（cancel 后正在执行的 tool 保留在最新迭代）。不折则丢失。
      const inFlight = [...live.activeTools, ...live.streamingTools].filter((t) =>
        t.status === 'running' || t.status === 'generating' || t.status === 'pending')
      const iterations = inFlight.length > 0
        ? foldInFlightTools(iterations0, inFlight, live.iter)
        : iterations0
      // 最终回复文本：text 顶层 content（v55 唯一权威值）> cancel 定格 content。
      const finalText = ev.content !== null ? ev.content : nonEmptyStr(live.content)
      // v55 渲染层 hasIterations=true 时不渲染顶层 content —— 最终回复必须存在于
      // 迭代内（否则 'all' 折叠的 lastText 取最后迭代 reasoning，回复丢失，
      // notification turn 用户报告："Done processing notification" 不显示）。
      // ⚠️ finalText 属于【进行中的迭代】，不是简单的"最后一个已存在迭代"：
      //    进行中迭代号 = max(live.iter, progressHistory 最后迭代号) ——
      //    progressHistory 可能已补齐全（后端权威快照比前端 live 领先，如
      //    cancel 时后端已到 iter2 而前端只收到 iter1），此时 finalText
      //    （cancel 定格 content）属于 progressHistory 的最后一个迭代。
      //    - 该迭代已在 iterations 里 → 覆盖它（正常完成 / progressHistory 补齐）。
      //    - 未在且比最后一个大（AskUser cancel：AskUser 工具调用中取消，无
      //      in-flight 工具 → 不触发 foldInFlightTools 追加）→ 【追加】新迭代。
      //      旧代码无条件覆盖最后一个已存在迭代，把已完成迭代的 content 替换成
      //      当前迭代文本 —— 用户报告"askuser 取消后迭代渲染混乱顺序错乱"
      //      （iter2 内容变成 iter3 文本）。
      const iterListLast = iterations.length > 0 ? iterations[iterations.length - 1].iteration : 0
      const inFlightIter = Math.max(live.iter, iterListLast)
      const iterationsFinal = finalText !== null && iterations.length > 0
        ? (iterations.some((it) => it.iteration === inFlightIter)
            ? iterations.map((it) => it.iteration === inFlightIter ? { ...it, content: finalText } : it)
            : [...iterations, {
                iteration: inFlightIter,
                content: finalText,
                reasoning: live.reasoning ?? '',
                tools: [],
                toolCount: 0,
              }])
        : iterations

      let payload
      if (finalText !== null) {
        payload = commitViaText(finalText, iterationsFinal)
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

    // ── history_replaced：merge 语义（reload / hydration / rewind —— 同一转移） ──
    // ⚠️ 不能盲替换（用户报告两个 P0）：
    //  1) "发送 user msg 会导致上一个 turn 的 agent 消息消失" —— text_final 的
    //     committed turn 只存在于状态机（useChatMessages 的 messages 不再被
    //     appendAssistant 同步），盲替换会抹掉 DB 快照还没有的唯一数据源。
    //  2) "打字机没了" —— user_echo 触发 messages 变化 → history_replaced 若抹掉
    //     turn_started 刚建的 live turn（echo 与 turn_started 的 listener 时序可
    //     颠倒），activeTurn=null → 后续 stream 事件全被丢弃。
    // merge 规则：DB 权威覆盖它【有】的 turn（带 dbID）；状态机持有的
    //  in-flight（live）与 post-fetch commit（DB 快照缺失）保留；hydration
    //  的 ev.active 在无 live turn 时创建之（刷新恢复 live 此前也是坏的）。
    case 'history_replaced': {
      const turns = new Map<TurnID, Turn>()
      const incomingIds = new Set(ev.turns.map((t) => t.id))

      // 1. incoming（DB 权威）—— 同 turnID 状态机有 live（in-flight）时 live 胜
      //    （SSE 比快照新）；live 缺 user 时从 DB 行嫁接（拿 dbID，rewind 需要）。
      //    ⚠️ incoming 是【无输出空壳】（user-only 历史行组成的 frozen 空壳 ——
      //    DB 快照还没有 assistant 行）时不得覆盖状态机的 committed /
      //    frozen-with-output（集成测试 C 抓出：发 user msg 后上一 turn 的
      //    agent 消息消失 —— 空壳覆盖了唯一数据源）。
      //    ⚠️ committed/frozen-with-output：union 合并（I4 append-only）—— DB 快照
      //    可能【过时】（reload/replay_gap 在 turn 运行中拉过一次，DB 只有中间行；
      //    或最终行尚未持久化）。覆盖会丢最后迭代（用户报告："发新消息后上一
      //    turn 最后一条迭代消息直接消失"—— 发新消息必然触发 REST ack patchUser
      //    → messages 变化 → history_replaced，若 chat.messages 含 turn 的过时 DB
      //    行即复现）。迭代 union（incoming 同号权威覆盖 —— DB 是持久化权威，
      //    append-only 不减）；content 非空优先（状态机 SSE text 是权威 finalizer；
      //    DB 空 content 是 tool_summary 中间行）。
      for (const h of ev.turns) {
        const cur = s.turns.get(h.id)
        if (cur && cur.phase.kind === 'live') {
          // live 胜（SSE 比 DB 快照新）—— 但 live 只含【增量】迭代（重启
          // resume 后 SSE 先到的 lazy 采纳只带 resume Run 的迭代 k+1..；DB
          // committed 携带全量 1..k）。不 union 会竞态性丢失 1..k（SSE 先到
          // + fetchHistory 后到 → "重启后 turn 的 iter 1..k 全消失"、"切换
          // 会话有时能看到迭代有时看不到"）。union：同号 live 权威（SSE 比
          // DB 新）——与 step 3.5 的 active 快照 union 同原则（I4 append-only）。
          const incomingIts = h.phase.kind === 'committed'
            ? h.phase.payload.iterations
            : h.phase.kind === 'frozen'
              ? h.phase.data.iterations
              : []
          if (incomingIts.length === 0) {
            turns.set(h.id, cur.user ? cur : { ...cur, user: h.user })
          } else {
            turns.set(h.id, {
              ...cur,
              user: cur.user ?? h.user,
              phase: {
                kind: 'live',
                data: { ...cur.phase.data, iterations: mergeIterations(incomingIts, cur.phase.data.iterations) },
              },
            })
          }
        } else if (
          cur &&
          cur.phase.kind !== 'live' &&
          h.phase.kind === 'frozen' &&
          !hasOutput(h.phase.data)
        ) {
          turns.set(h.id, cur) // 空壳不覆盖（状态机数据保全）
        } else if (cur && cur.phase.kind !== 'live') {
          turns.set(h.id, mergeTurnData(cur, h)) // union 合并（不丢任何一侧迭代）
        } else {
          turns.set(h.id, h)
        }
      }

      // 2. 状态机独有（DB 快照缺失）—— 有数据则保留（live / committed /
      //    frozen-with-output；空壳 frozen 舍弃）。
      for (const [id, t] of s.turns) {
        if (incomingIds.has(id)) continue
        const keep =
          t.phase.kind === 'live' ||
          t.phase.kind === 'committed' ||
          (t.phase.kind === 'frozen' && hasOutput(t.phase.data))
        if (keep) turns.set(id, t)
      }

      // 3. activeTurn：保留下来的 live 优先；否则 hydration 的 ev.active。
      // ⚠️ 空壳占位（user-only 历史行组成的 frozen 空壳）必须升级为 live ——
      // DB 权威快照声明该 turn 正在运行；不升级则 activeTurn=null → 后续
      // stream 事件全部被丢弃（用户报告："切换或刷新后只显示 history，
      // live progress 不显示"）。committed/有输出 frozen 不动（DB 行更权威，
      // 快照可能滞后于 SSE commit）。
      let activeTurn: TurnID | null = null
      if (s.activeTurn !== null) {
        const t = turns.get(s.activeTurn)
        if (t && t.phase.kind === 'live') activeTurn = s.activeTurn
      }
      if (activeTurn === null && ev.active !== null) {
        const tid = ev.active.turnID
        const existing = turns.get(tid)
        if (existing && existing.phase.kind === 'live') {
          activeTurn = tid
        } else if (!existing || isHollowFrozen(existing)) {
          turns.set(tid, {
            id: tid,
            user: existing?.user ?? null,
            phase: { kind: 'live', data: ev.active.snapshot },
            requestID: existing?.requestID ?? null,
          })
          activeTurn = tid
        }
      }

      // 3.5 ev.active 与已保留的 live turn 同 ID → 快照数据 union 进 live
      // （切换会话竞态修复）：切换后 SSE delta 先到（lazy 采纳，push 协议每事件
      // 只携带【新完成】的 0-1 个迭代），fetchHistory 的 active_progress 快照
      // 携带【完整】iterationHistory —— live 胜出时必须吸收快照迭代，否则
      // 最新 turn 只渲染切换后的最后一两个 live iter（用户报告："切换会话有
      // 概率最新 turn 只渲染最后一两个 live iter"）。
      // I4：mergeIterations union 只增（同号快照权威覆盖 —— 已完成迭代以
      // 服务端为权威）。流式字段 live 非空优先（SSE 比快照新）。
      if (ev.active !== null && activeTurn !== null && activeTurn === ev.active.turnID) {
        const t = turns.get(activeTurn)
        if (t && t.phase.kind === 'live') {
          const snap = ev.active.snapshot
          const d = t.phase.data
          turns.set(activeTurn, {
            ...t,
            phase: {
              kind: 'live',
              data: {
                ...d,
                iter: d.iter > snap.iter ? d.iter : snap.iter,
                content: d.content !== '' ? d.content : snap.content,
                reasoning: d.reasoning !== '' ? d.reasoning : snap.reasoning,
                iterations: mergeIterations(d.iterations, snap.iterations),
                activeTools: d.activeTools.length > 0 ? d.activeTools : snap.activeTools,
                streamingTools: d.streamingTools.length > 0 ? d.streamingTools : snap.streamingTools,
                genui: d.genui !== '' ? d.genui : snap.genui,
                todos: d.todos.length > 0 ? d.todos : snap.todos,
                subAgents: d.subAgents.length > 0 ? d.subAgents : snap.subAgents,
                tokenUsage: d.tokenUsage ?? snap.tokenUsage,
              },
            },
          })
        }
      }

      // 4. lastSeq：保留了 active turn 时维持 per-run seq 连续性（否则重放检测
      //    基准丢失）。无 active 时用事件携带值。
      const lastSeq = activeTurn !== null ? s.lastSeq : ev.lastSeq

      // 5. pendingUsers：已被合并 turns 绑定的（turnHint/requestID 命中）剔除。
      const pendingUsers = s.pendingUsers.filter(
        (u) =>
          !(u.turnHint !== undefined && turns.has(turnID(u.turnHint))) &&
          !(u.requestID !== null && [...turns.values()].some((t) => t.user?.requestID === u.requestID)),
      )

      return { chatID: s.chatID, turns, legacy: ev.legacy, activeTurn, lastSeq, busy: s.busy, pendingUsers, todos: s.todos.length > 0 ? s.todos : ev.todos }
    }

    // ── user_sent：乐观行入 pending 队列 ──
    case 'user_sent': {
      return { ...s, pendingUsers: [...s.pendingUsers, ev.row] }
    }

    // ── user_echo：后端权威回显（带 turn_id）。已绑定同 request -> 幂等；
    //     turn 已存在但 user 空 -> 挂 user；否则入 pending（turnHint 绑定）。 ──
    // ⚠️ 核心修复（双 user 行 + 双思考中）：user_echo 是"同一条 user 消息的
    //    权威回显"，绝不产生【第二条】渲染行 —— 渲染源是状态机 pendingUsers
    //    （user_sent 直通）+ turns[].user（turn_started/echo 绑定）。当
    //    turn_started 已把乐观 user（requestID=R）绑定进 turn 后，迟到且同 R
    //    的 user_echo 若被追加进 pendingUsers，会与 turn.user 构成同消息两行
    //    （Bug：用户看到 user msg + 思考中 完整复制两份）。幂等规则：
    //      - 同 R 已在 turn.user       → 返回不变（权威回显，零副作用）
    //      - 同 R 已在 pendingUsers     → 就地用 echo 替换（清 sending、收敛），不新增（仍一行）
    case 'user_echo': {
      // ① 幂等：同 R 已在 turn.user（turn_started 已绑定乐观行）→ 权威回显，
      //    零副作用返回（绝不再产生第二行 —— 双 user+双思考中根治）。
      if (ev.row.requestID !== null) {
        for (const t of s.turns.values()) {
          if (t.user && t.user.requestID === ev.row.requestID) return s
        }
        // ② pending 已有同 R 行（乐观 user_sent / 更早 echo）：就地用 echo
        // 权威字段替换（清 sending、回填 turnHint/turn 归属），不新增行 ——
        // 仍保持单行（echo 取代乐观语义，历史的 append 副本在此收敛）。
        const existingIdx = s.pendingUsers.findIndex((u) => u.requestID === ev.row.requestID)
        if (existingIdx >= 0) {
          const pendingUsers = s.pendingUsers.slice()
          pendingUsers[existingIdx] = { ...pendingUsers[existingIdx], ...ev.row, id: pendingUsers[existingIdx].id }
          return { ...s, pendingUsers }
        }
      }
      // ③ hint 指向未绑定 turn → 直接挂 user。
      const hint = ev.row.turnHint
      if (hint !== undefined) {
        const tid = turnID(hint)
        const t = s.turns.get(tid)
        if (t && t.user === null) {
          return withTurn(s, tid, (tt) => ({ ...tt, user: ev.row }))
        }
      }
      // ③.5 notification echo 内容幂等（同一通知双行根治）：turn_started(notification)
      //    已用 turn_start.content 构造 notif user 行后，后端 InjectUserMessage 的
      //    inject_user echo 后到 —— web.go 的 WSMessage 只有 Type/TS/ChatID/Content
      //    （无 request_id/turn_id/is_notification）→ ①②③ 全不命中 → ④ 无条件
      //    append → 同一通知渲染两行（turn.user 的 notif-${turnID} 行 + 沉底 echo 行）。
      //    幂等锚点在 turns/pending 侧的 isNotification 行（echo 侧无归属标记可匹配）：
      //    已存在 isNotification 且 content 相同的 user 行 → 同一逻辑消息，丢弃 echo。
      //    echo 自带 requestID/turnHint 的正常路径（①②③）不受影响。
      if (
        ev.row.isNotification ||
        [...s.turns.values()].some((t) => t.user?.isNotification === true && t.user.content === ev.row.content)
      ) {
        if (
          [...s.turns.values()].some((t) => t.user?.isNotification === true && t.user.content === ev.row.content) ||
          s.pendingUsers.some((u) => u.isNotification && u.content === ev.row.content)
        ) {
          return s
        }
      }
      // ④ 全新 user（无未绑定 pending）→ 入 pending（turnHint 后续绑定）。
      return { ...s, pendingUsers: [...s.pendingUsers, ev.row] }
    }

    // ── user_ack：REST 发送成功 —— 清 sending、回填服务端信息 ──
    // ⚠️ queued 显式赋值（resp.queued === true 才排队；成功即非发送中）。
    // turnHint 补填（未被 turn_started 绑定时），供后续 echo/started 嫁接。
    case 'user_ack': {
      const dbID = ev.dbID > 0 ? ev.dbID : undefined
      const idx = s.pendingUsers.findIndex((u) => u.requestID === ev.requestID)
      if (idx >= 0) {
        const pendingUsers = s.pendingUsers.slice()
        const u = pendingUsers[idx]
        pendingUsers[idx] = {
          ...u,
          dbID: dbID ?? u.dbID,
          sending: false,
          queued: ev.queued === true,
          turnHint: u.turnHint ?? ev.turnHint,
        }
        return { ...s, pendingUsers }
      }
      // 已绑定进 turn 的 user（turn_started 先于 REST 完成的时序）。
      for (const t of s.turns.values()) {
        if (t.user?.requestID === ev.requestID) {
          return withTurn(s, t.id, (tt) =>
            tt.user
              ? { ...tt, user: { ...tt.user, dbID: dbID ?? tt.user.dbID, sending: false, queued: ev.queued === true } }
              : tt,
          )
        }
      }
      return s
    }

    // ── user_fail：REST 发送失败 —— 移除乐观行 ──
    case 'user_fail': {
      const pendingUsers = s.pendingUsers.filter((u) => u.requestID !== ev.requestID)
      if (pendingUsers.length === s.pendingUsers.length) return s
      return { ...s, pendingUsers }
    }
  }
}

/** text_final(cancel) 时把正在执行的工具（标 error）折进最后迭代 ——
 *  progress_history 不含从未完成的工具，不折会丢（"已渲染内容永不消失"）。 */
function foldInFlightTools(
  its: readonly WebIteration[],
  tools: readonly WebToolProgress[],
  lastIter: IterNum,
): readonly WebIteration[] {
  const errTools = tools.map((t) => ({ ...t, status: 'error' as const }))
  const arr = [...its]
  const idx = arr.findIndex((it) => it.iteration === lastIter)
  if (idx >= 0) {
    arr[idx] = {
      ...arr[idx],
      tools: [...arr[idx].tools, ...errTools],
      toolCount: (arr[idx].toolCount ?? 0) + errTools.length,
    }
  } else {
    arr.push({ iteration: lastIter, content: '', reasoning: '', tools: errTools, toolCount: errTools.length })
  }
  return arr
}

// ─── foldPhase：live 数据 → committed/frozen（turn_started 收尸） ──

/**
 * history_replaced step1 的 union 合并：状态机 committed/frozen-with-output ×
 * incoming（DB）committed/frozen-with-output。迭代 append-only union（incoming
 * 同号权威覆盖 —— DB 是持久化权威），content 非空优先（状态机 SSE text 是权威
 * finalizer；DB 空 content 是 tool_summary 中间行）。user 嫁接（保留已有，补
 * DB 的 dbID 行）。进此函数的两侧都必有输出（空壳已在 step1 前分流），构造
 * committed 是渲染等价的安全形态。
 */
function mergeTurnData(cur: Turn, h: Turn): Turn {
  const curIts = cur.phase.kind === 'committed' ? cur.phase.payload.iterations : cur.phase.data.iterations
  const incIts = h.phase.kind === 'committed' ? h.phase.payload.iterations : h.phase.data.iterations
  const curContent = cur.phase.kind === 'committed' ? cur.phase.payload.content : cur.phase.data.content
  const incContent = h.phase.kind === 'committed' ? h.phase.payload.content : h.phase.data.content
  const iterations = mergeIterations(curIts, incIts)
  const content = curContent !== '' ? curContent : incContent
  const text = nonEmptyStr(content)
  const its = nonEmptyArr(iterations)
  const phase: Turn['phase'] =
    text !== null
      ? { kind: 'committed', payload: commitViaText(text, iterations) }
      : its !== null
        ? { kind: 'committed', payload: commitViaFold(its, content) }
        : { kind: 'frozen', data: cur.phase.kind === 'frozen' ? cur.phase.data : h.phase.kind === 'frozen' ? h.phase.data : { ...EMPTY_LIVE } }
  return { id: h.id, user: cur.user ?? h.user, phase, requestID: cur.requestID ?? h.requestID }
}

function foldPhase(data: LiveSnapshot): Turn['phase'] {
  const its = nonEmptyArr(data.iterations)
  if (its !== null) return { kind: 'committed', payload: commitViaFold(its, data.content) }
  const text = nonEmptyStr(data.content)
  if (text !== null) return { kind: 'committed', payload: commitViaText(text, []) }
  // 无任何产出：frozen 定格（derive 跳过空 assistant 行；user 行保留）。
  return { kind: 'frozen', data }
}

export { initialChatState }
