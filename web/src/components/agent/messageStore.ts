/**
 * MessageStore — 单一消息状态机（方案 A，见 docs/agent/web-message-store.md）。
 *
 * 取代"两套独立数据（committed messages + live progress）+ 启发式去重
 * （buildMessageRows sameTurnIdx/exactDup）"的架构。核心保证：
 *
 *   每个 turn 恰好 1 个 user + 1 个 assistant 槽位；live 是 assistant 的
 *   未完成态（同对象状态迁移 streaming→completed）。唯一性由 Map 结构保证，
 *   渲染层零去重 —— 从结构上消除 exactDup 跨 turn 迭代号匹配误杀、content/
 *   eventSeq 匹配漏网等整类 "turn 消失/重复" bug。
 *
 * 纯状态机：不依赖 React。上层（useChatMessages / useProgressStream）只喂
 * 事件，渲染层只读 toRows()。
 */
import type { ChatMessage, WebIteration, WebToolProgress } from '@/types/shared'

/** Live streaming state —— assistant 的未完成态。 */
export interface LiveState {
  eventSeq: number
  phase: string
  /** 累积流式文本（streamContent）或结构化 content。 */
  content: string
  reasoningStreamContent: string
  iterations: WebIteration[]
  activeTools: WebToolProgress[]
  completedTools: WebToolProgress[]
  streamingTools: WebToolProgress[]
  lastIter: number
  turnID: number
  /** Cancel 后冻结：live 保留渲染（已渲染内容永不消失）。 */
  frozen: boolean
}

export interface TurnSlot {
  turnID: number
  /** user 消息（乐观 / user_echo / DB 回填）。 */
  user?: ChatMessage
  /** 已提交的 assistant（text 事件 / DB reload 回填）。 */
  assistant?: ChatMessage
  /** streaming 进行中（commitAssistant 时清空）。 */
  live?: LiveState
}

const EMPTY_LIVE: LiveState = {
  eventSeq: 0,
  phase: '',
  content: '',
  reasoningStreamContent: '',
  iterations: [],
  activeTools: [],
  completedTools: [],
  streamingTools: [],
  lastIter: 0,
  turnID: 0,
  frozen: false,
}

/** Merge two WebIteration arrays by iteration number (union, prefer non-empty). */
export function mergeIterations(a: WebIteration[], b: WebIteration[]): WebIteration[] {
  if (a.length === 0) return b
  if (b.length === 0) return a
  const map = new Map<number, WebIteration>()
  for (const iter of a) map.set(iter.iteration, iter)
  for (const iter of b) {
    const existing = map.get(iter.iteration)
    if (!existing) {
      map.set(iter.iteration, iter)
    } else {
      const existingHasContent = (existing.content ?? '') !== '' || (existing.reasoning ?? '') !== ''
      const incomingHasContent = (iter.content ?? '') !== '' || (iter.reasoning ?? '') !== ''
      if (incomingHasContent && !existingHasContent) {
        map.set(iter.iteration, iter)
      } else if (incomingHasContent && existingHasContent) {
        // Both have content — merge tools (union by name+label)
        const toolMap = new Map<string, WebToolProgress>()
        for (const t of [...existing.tools, ...iter.tools]) {
          toolMap.set(`${t.name}\x00${t.label}`, t)
        }
        map.set(iter.iteration, { ...existing, tools: [...toolMap.values()] })
      }
      // else: keep existing
    }
  }
  return [...map.values()].sort((x, y) => x.iteration - y.iteration)
}

export class MessageStore {
  private slots = new Map<number, TurnSlot>()
  /** 已排序 turnID（后端分配单调递增，增量维护）。 */
  private turnIDs: number[] = []
  /** 无 turnID 的 persisted 行（legacy）—— 渲染在顶部。 */
  private legacy: ChatMessage[] = []
  /** turnID=0 的乐观 user，待 turn_started 绑定。 */
  private pendingUsers: ChatMessage[] = []
  private echoSeq = 0
  /** 渲染缓存：turnID 集合或任意 slot 结构变化时失效。 */
  private cache: ChatMessage[] | null = null
  private cacheKey = ''
  /** committed 版本：只在渲染结构写（user/assistant/legacy/pending）时 ++。
   *  live 写（updateLive）不 ++ —— useChatMessages 据此区分 committed/live
   *  变化：committed 变化触发 setMessages（低频），live 变化由 MessageList
   *  useSyncExternalStore 局部订阅（每帧，避免 AgentPanel 整树 re-render
   *  卡顿 —— stream-jitter E2E 超时根因）。 */
  private committedVersion = 0
  getCommittedVersion(): number {
    return this.committedVersion
  }
  private bumpCommitted(): void {
    this.committedVersion++
  }

  // ────────────────────────── 写入（事件入口） ──────────────────────────

  /**
   * turn_started。opts.resume = AskUser 续跑（同 turnID）：保留 iterations，
   * 只清流式字段。同时把早于新 turn 的未提交 live commit（模拟
   * commitLiveProgressAndReset —— 旧 turn 的 text 事件可能丢失，live 是唯一
   * 显示）。
   */
  beginTurn(turnID: number, opts?: { resume?: boolean; requestID?: string }): void {
    this.commitStaleLives(turnID)
    const existing = this.slots.get(turnID)
    if (existing) {
      if (opts?.resume) {
        // AskUser 续跑：保留 iterations，清流式字段
        const iters = existing.live?.iterations ?? []
        existing.live = { ...EMPTY_LIVE, turnID, iterations: iters }
        existing.assistant = undefined
      } else if (existing.live && !existing.live.frozen) {
        existing.live = { ...EMPTY_LIVE, turnID }
      }
    } else {
      this.slots.set(turnID, { turnID })
      this.insertTurnID(turnID)
    }
    // 绑定最后一条未持久化 user（turn_started 是权威绑定点）。
    // V2：优先按 turn_started 携带的 requestID 精确匹配（TurnStartInfo.RequestID，
    // protocol/events.go:155 "for user-typed: match optimistic message"）—— 否则
    // 两条消息快速连发 + REST 响应慢时，turn_started(msg1) 会从后往前误绑 msg2
    // （弱网下短暂顺序错乱）。无 requestID 时 fallback 最后一条（向后兼容）。
    this.bindUser(turnID, opts?.requestID)
    this.bumpCommitted()
    this.invalidate()
  }

  /** turn_started 时绑定未持久化 user 到该 turn（原 bindLastUserToTurn）。 */
  bindUser(turnID: number, requestID?: string): void {
    const slot = this.slots.get(turnID)
    if (!slot || slot.user) return
    if (requestID) {
      // 精确匹配：turn_started 的 requestID 指向触发它的乐观 user。
      for (let i = this.pendingUsers.length - 1; i >= 0; i--) {
        const u = this.pendingUsers[i]
        if (u.role === 'user' && !u.persisted && u.requestID === requestID) {
          this.pendingUsers.splice(i, 1)
          slot.user = { ...u, turnID }
          this.bumpCommitted()
          return
        }
      }
      // requestID 没匹配到（echo/REST 已绑定或已移除）—— 回退最后一条。
    }
    for (let i = this.pendingUsers.length - 1; i >= 0; i--) {
      const u = this.pendingUsers[i]
      if (u.role === 'user' && !u.persisted) {
        this.pendingUsers.splice(i, 1)
        slot.user = { ...u, turnID }
        this.bumpCommitted()
        return
      }
    }
  }

  /** user 消息（乐观 turnID=0 → pending；echo/DB → 槽位回填）。 */
  setUser(turnID: number, msg: ChatMessage): void {
    if (turnID <= 0) {
      this.pendingUsers.push(msg)
      this.bumpCommitted()
      this.invalidate()
      return
    }
    let slot = this.slots.get(turnID)
    if (!slot) {
      slot = { turnID }
      this.slots.set(turnID, slot)
      this.insertTurnID(turnID)
    }
    // 回填 DB 权威字段，保留 optimistic 的 requestID/sending/queued
    slot.user = { ...slot.user, ...msg, turnID }
    this.bumpCommitted()
    this.invalidate()
  }

  /** 更新 live（progress_structured / stream_content 事件）。 */
  updateLive(turnID: number, patch: Partial<LiveState>): void {
    // frozen guard 必须在 mutation 之前（Loop2 F3）：cancel 定格（freeze）后
    // 迟到的 progress 事件（SSE 乱序/重放）不得污染 frozen live —— 已渲染内容
    // 永不消失。旧实现先赋值再 return，只跳过 invalidate，迟到事件照样覆盖
    // content/iterations。
    if (this.slots.get(turnID)?.live?.frozen) return
    let slot = this.slots.get(turnID)
    if (!slot) {
      slot = { turnID }
      this.slots.set(turnID, slot)
      this.insertTurnID(turnID)
    }
    const prev = slot.live ?? { ...EMPTY_LIVE, turnID }
    slot.live = {
      ...prev,
      ...patch,
      turnID,
      // iterations 永不回退：union 合并（按迭代号）。reload 的 active_progress
      // 快照（hydration）可能在 turn 运行中到达，其 iteration_history 滞后或为空
      // （服务器 iterationHistories 重启后重累积 / fromIter 增量 / 竞态）——覆盖
      // 语义会清空进行中 turn 的已完成迭代，用户报告"迭代到一半 history 突然只剩
      // live iter，高度变低触发 load more"。union 保证已完成迭代永不消失；
      // turnID 是 slot key，跨 turn 不会污染。
      iterations: mergeIterations(prev.iterations, patch.iterations ?? []),
    }
    this.invalidate()
  }

  /**
   * text 事件：live → assistant 状态迁移（同一逻辑消息，不产生第二行）。
   * content 为空时回退到 live 累积文本；iterations 与 live 合并。
   */
  commitAssistant(turnID: number, content: string, iterations: WebIteration[], eventSeq?: number): void {
    let slot = this.slots.get(turnID)
    if (!slot) {
      slot = { turnID }
      this.slots.set(turnID, slot)
      this.insertTurnID(turnID)
    }
    const live = slot.live
    const merged = live ? mergeIterations(iterations, live.iterations) : iterations
    // text 事件 content 权威（正常 turn 的 finalText = 完整 stream，与 live 累积
    // 一致 → 无缝定住；cancel 的 [interrupted] 是中断标记）。live 累积 content
    // 仅在 text 内容为空时兜底（writeLive 已保留非空，live.content 不因迭代
    // 边界清空而消失 —— 防 "stream 完 content 消失再出现" 闪烁）。
    const finalContent = content || live?.content || ''
    const id = eventSeq != null ? `seq-${eventSeq}` : `asst-${turnID}-${this.echoSeq++}`
    slot.assistant = {
      id,
      role: 'assistant',
      content: finalContent,
      iterations: merged,
      timestamp: new Date().toISOString(),
      isPartial: false,
      turnID,
      persisted: false,
      eventSeq,
    }
    // 同对象迁移 —— 结构上不可能出现双行。但 frozen live（cancel）必须保留：
    // toRows 里 assistant + frozen live 合并渲染（已渲染内容永不消失）。
    if (!slot.live?.frozen) {
      slot.live = undefined
    }
    this.bumpCommitted()
    this.invalidate()
  }

  /** Cancel：冻结 live（保留已渲染内容）。
   *
   *  In-flight tools (activeTools/completedTools/streamingTools) that are NOT
   *  yet in iterationHistory are folded into a synthetic final iteration —
   *  otherwise toRows' frozen branch only renders iterations (which may be
   *  empty for the last iteration: attachIterationDelta doesn't fire for the
   *  last iteration, and WaitingUser doesn't send PhaseDone so
   *  recordFinalIteration doesn't fire either). The AskUser tool call is
   *  the canonical case: it's the ONLY iteration, lives in completedTools,
   *  and vanishes after cancel without this fold. */
  freeze(turnID: number): void {
    const slot = this.slots.get(turnID)
    if (slot?.live && !slot.live.frozen) {
      const live = slot.live
      // Fold in-flight tools into iterations so toRows renders them.
      const inFlightTools = [
        ...live.activeTools,
        ...live.completedTools,
        ...live.streamingTools,
      ].filter((t) => t && t.name)
      if (inFlightTools.length > 0) {
        const maxIter = live.iterations.reduce((m, it) => Math.max(m, it.iteration), 0)
        const lastIter = live.iterations[live.iterations.length - 1]
        // If the last iteration has no tools, fold into it; otherwise append.
        if (lastIter && (!lastIter.tools || lastIter.tools.length === 0)) {
          live.iterations = [
            ...live.iterations.slice(0, -1),
            { ...lastIter, tools: inFlightTools, toolCount: inFlightTools.length },
          ]
        } else {
          live.iterations = [
            ...live.iterations,
            {
              iteration: maxIter + 1,
              content: live.content || '',
              reasoning: live.reasoningStreamContent || '',
              tools: inFlightTools,
              toolCount: inFlightTools.length,
            },
          ]
        }
      }
      slot.live = { ...live, frozen: true }
      this.invalidate()
    }
  }

  /** session(idle)：清理已冻结的 live。 */
  endTurn(turnID: number): void {
    const slot = this.slots.get(turnID)
    if (slot?.live?.frozen) {
      slot.live = undefined
      this.invalidate()
    }
  }

  /** session(idle)：清理无内容的空 live。空 live 是 turn 以 thinking 开始但
   *  无内容产出（PhaseDone/text 都丢失）时的残留壳 —— toRows() 输出 isPartial
   *  assistant 行，AssistantMessage 把它误判为 thinking phase 渲染"思考中…"
   *  （用户报告：idle 后思考中渲染在 agent 消息第一行）。非空 live 不清 ——
   *  defensive finalize（complete → commitAssistant）的职责；frozen live 不清
   *  —— cancel 已渲染内容永不消失。 */
  clearEmptyLives(): void {
    let changed = false
    for (const tid of this.turnIDs) {
      const slot = this.slots.get(tid)
      const live = slot?.live
      if (!live || live.frozen) continue
      const empty = !live.content &&
        !live.reasoningStreamContent &&
        live.iterations.length === 0 &&
        live.activeTools.length === 0 &&
        live.completedTools.length === 0 &&
        live.streamingTools.length === 0
      if (empty) {
        slot.live = undefined
        changed = true
      }
    }
    if (changed) this.invalidate()
  }

  /** 无 turnID 的 persisted 行（legacy）→ 顶部。 */
  addLegacy(msg: ChatMessage): void {
    this.legacy.push(msg)
    this.bumpCommitted()
    this.invalidate()
  }

  /**
   * reload 结果回填：DB 字段权威（dbID/persisted/content），但不覆盖进行中的
   * live（live 是权威的 streaming 状态）。无 turnID 的 persisted 行进 legacy。
   *
   * opts.replace（reload 主路径）：DB 快照权威 —— 删除 DB 快照中没有的已完成
   * turn（无 live）槽位；legacy 由快照重建；pending 乐观 user 只保留
   * eventSeq > watermark 的（watermark 之上的 echo 是快照之后的新数据，
   * 等价原 reconcile 的 watermark 规则）。进行中 turn（有 live）保留。
   * loadMore（无 replace）是增量合并（迭代 union）。
   */
  mergeHistory(rows: ChatMessage[], opts?: { replace?: boolean; watermark?: number }): void {
    if (opts?.replace) {
      const rowTurns = new Set<number>()
      for (const r of rows) if (r.turnID > 0) rowTurns.add(r.turnID)
      for (const tid of [...this.slots.keys()]) {
        const slot = this.slots.get(tid)
        // 进行中 turn（live 权威）保留；notification（eventSeq=-1，DB 快照可能
        // 尚未持久化）保留；已完成但 DB 快照没有 → 删除（rewind 语义）
        if (slot && !slot.live && !rowTurns.has(tid) &&
            slot.user?.eventSeq !== -1 && !slot.user?.isNotification) {
          this.slots.delete(tid)
          this.turnIDs = this.turnIDs.filter((t) => t !== tid)
        }
      }
      // DB 快照权威：legacy 由快照重建；pending 乐观 user 保留"无 seq"与
      // notification（eventSeq=-1 哨兵）行，只删 watermark 之下的 echo ——
      // 乐观行（sendMessage 创建，echo 未到）在 reload 竞态窗口（replay_gap/
      // resync 触发）被删会让发送中的消息闪没（Loop2 F5；AGENTS.md
      // "Notification user messages (eventSeq=-1) survive racing reloads"
      // 同模式）；watermark 之上的 echo 是快照之后的新数据，保留（等价原
      // reconcile 的 watermark 规则）。
      this.legacy = []
      if (opts.watermark != null) {
        this.pendingUsers = this.pendingUsers.filter(
          (u) => u.eventSeq == null || u.eventSeq === -1 || u.eventSeq > opts.watermark!,
        )
      } else {
        this.pendingUsers = []
      }
    }
    for (const row of rows) {
      if (row.turnID > 0) {
        let slot = this.slots.get(row.turnID)
        if (!slot) {
          slot = { turnID: row.turnID }
          this.slots.set(row.turnID, slot)
          this.insertTurnID(row.turnID)
        }
        if (row.role === 'user') {
          slot.user = { ...slot.user, ...row, turnID: row.turnID }
        } else if (row.role === 'assistant') {
          // 始终写入/合并 slot.assistant —— 即使 slot.live 存在（非 frozen）。
          // slot.assistant 包含 DB 的已完成迭代，slot.live 只有当前迭代；
          // toRows() 合并两者（assistant.iterations + live.iterations）显示。
          // 旧逻辑在 slot.live 存在时跳过 assistant 写入 → 切换会话时只显示
          // live iter（已完成迭代丢失，用户报告："只看到 live iter 而非完整
          // turn iter"）。
          if (slot.assistant) {
            // 已有本地/DB 提交：合并迭代（loadMore 边界 union）。content 优先
            // 级：replace（reload，DB 快照权威）→ DB 版本优先；增量（loadMore）
            // → 已有优先（较新批次持有 final reply，较旧批次是 tool_summary
            // 空 content）
            slot.assistant = {
              ...row,
              turnID: row.turnID,
              content: opts?.replace
                ? (row.content || slot.assistant.content)
                : (slot.assistant.content || row.content),
              iterations: mergeIterations(slot.assistant.iterations, row.iterations),
            }
          } else {
            slot.assistant = { ...row, turnID: row.turnID }
          }
        }
      } else if (row.persisted !== false) {
        this.addLegacy(row)
      }
    }
    this.bumpCommitted()
    this.invalidate()
  }

  /** 清空（session 切换 / /new）。 */
  clear(): void {
    this.slots.clear()
    this.turnIDs = []
    this.legacy = []
    this.pendingUsers = []
    this.cache = null
    this.cacheKey = ''
    this.bumpCommitted()
  }

  /** REST 响应回填 optimistic user（persisted/turnID/dbID/timestamp/queued）。
   *  turnID 从 0 变 >0 时把 user 从 pending 迁移到对应 slot（绑定）。 */
  patchUserById(id: string, patch: Partial<ChatMessage>): void {
    for (let i = 0; i < this.pendingUsers.length; i++) {
      const u = this.pendingUsers[i]
      if (u.id === id) {
        const updated = { ...u, ...patch }
        if (updated.turnID > 0) {
          // REST 响应给了 turnID → 绑定到 slot（REST 响应是权威绑定点，
          // 早于/晚于 turn_started 都正确）
          this.pendingUsers.splice(i, 1)
          let slot = this.slots.get(updated.turnID)
          if (!slot) {
            slot = { turnID: updated.turnID }
            this.slots.set(updated.turnID, slot)
            this.insertTurnID(updated.turnID)
          }
          slot.user = updated
        } else {
          this.pendingUsers[i] = updated
        }
        this.bumpCommitted()
        this.invalidate()
        return
      }
    }
    for (const slot of this.slots.values()) {
      if (slot.user?.id === id) {
        slot.user = { ...slot.user, ...patch }
        this.bumpCommitted()
        this.invalidate()
        return
      }
    }
  }

  /** 按 id 移除（sendMessage 失败 / removeMessage）。 */
  removeById(id: string): void {
    const pendingLen = this.pendingUsers.length
    this.pendingUsers = this.pendingUsers.filter((u) => u.id !== id)
    if (this.pendingUsers.length !== pendingLen) {
      this.bumpCommitted()
      this.invalidate()
      return
    }
    for (const slot of this.slots.values()) {
      if (slot.user?.id === id) {
        slot.user = undefined
        this.bumpCommitted()
        this.invalidate()
        return
      }
      if (slot.assistant?.id === id) {
        slot.assistant = undefined
        this.bumpCommitted()
        this.invalidate()
        return
      }
    }
  }

  /** 按 requestID 查找 user（乐观或 echo）—— user_echo 替换 optimistic 用。 */
  findUserByRequestID(requestID: string): { id: string; persisted: boolean; turnID: number } | undefined {
    for (const u of this.pendingUsers) {
      if (u.requestID === requestID) return { id: u.id, persisted: Boolean(u.persisted), turnID: u.turnID }
    }
    for (const slot of this.slots.values()) {
      if (slot.user?.requestID === requestID) {
        return { id: slot.user.id, persisted: Boolean(slot.user.persisted), turnID: slot.user.turnID }
      }
    }
    return undefined
  }

  // ────────────────────────── 读取 ──────────────────────────

  /** 排序后的渲染行：legacy（顶部）→ 各 turn（user + assistant/live）→ pending user。 */
  toRows(): ChatMessage[] {
    const key = this.renderKey()
    if (this.cache && this.cacheKey === key) return this.cache
    const rows: ChatMessage[] = []
    for (const msg of this.legacy) rows.push(msg)
    for (const tid of this.turnIDs) {
      const slot = this.slots.get(tid)
      if (!slot) continue
      if (slot.user) rows.push(slot.user)
      if (slot.assistant) {
        if (slot.live?.frozen) {
          // Cancel：assistant=[interrupted]，live 有流式内容 —— 合并渲染（同对象）。
          // LIVE content 优先：已渲染的流式内容永不消失，[interrupted] 只是中断标记
          // （现状 buildMessageRows 的 committed||live 让 [interrupted] 覆盖流式文本
          //  —— cancel 后用户看到的内容消失，违反要求）。
          // ⚠️ isPartial:true 必须保留（V5）：否则 MessageList 的
          // liveId = rows.find(r => r.isPartial) 匹配不到该行 → liveProgress 不传给
          // 该行 → LiveIteration 不渲染 activeTools → cancel 后正在执行的 tool
          // （最新 iter）从 UI 消失（用户报告）。与非 frozen live 分支（else if
          // slot.live，isPartial:true + id=turn-{tid}-live）保持一致；frozen 行
          // 保留 assistant 的 id（同对象，不产生 turn-360-live 第二行）。
          rows.push({
            ...slot.assistant,
            content: slot.live.content || slot.assistant.content,
            iterations: mergeIterations(slot.live.iterations, slot.assistant.iterations),
            isPartial: true,
          })
        } else if (slot.live) {
          // slot 同时有 committed assistant（reload 回填 DB 历史）和 live（当前
          // 进行中的迭代）—— 合并 assistant 的已完成 iterations + live 的当前
          // iteration，输出为 isPartial 行。否则只显示 live 的当前迭代（切换
          // 会话时 reload 先到、hydration 后到，用户只看到 live iter 而非完整
          // turn iter）。
          rows.push({
            ...slot.assistant,
            content: slot.live.content || slot.assistant.content || '',
            iterations: mergeIterations(slot.assistant.iterations ?? [], slot.live.iterations),
            isPartial: true,
            id: `turn-${tid}-live`,
          })
        } else {
          rows.push(slot.assistant)
        }
      } else if (slot.live) {
        rows.push({
          id: `turn-${tid}-live`,
          role: 'assistant',
          content: slot.live.content || '',
          iterations: slot.live.iterations,
          timestamp: new Date().toISOString(),
          isPartial: true,
          turnID: tid,
        })
      }
    }
    for (const u of this.pendingUsers) rows.push(u)
    this.cache = rows
    this.cacheKey = key
    return rows
  }

  getLive(turnID: number): LiveState | undefined {
    return this.slots.get(turnID)?.live
  }

  /**
   * 找有可见内容的 live 的 turnID（用于 commit 时对齐 turn 归属）。
   *
   * ProgressStore.lastTurnID 可能过时（turn_started 在 SSE 上丢失时停留在
   * N-1），而 MessageStore 的 live 由事件 turn_id 写入正确的 slot N。
   * commitLiveProgressAndReset 若用过时的 lastTurnID commit，cancel 内容会
   * 同时落在旧 slot（lastTurnID）和 live slot → 重复渲染（用户报告："cancel
   * 一个消息后发新 user msg，被 cancel 的 turn 的 live progress 在 user msg
   * 后重复渲染"）。优先返回 frozen（cancel）live 的 turnID —— 内容已渲染的
   * turn 归属必须与 commit 目标一致。
   */
  liveTurnIDWithContent(): number {
    let best = 0
    for (const tid of this.turnIDs) {
      const live = this.slots.get(tid)?.live
      if (!live) continue
      if (!live.content && live.iterations.length === 0 && live.activeTools.length === 0 && live.streamingTools.length === 0) {
        continue
      }
      if (live.frozen) return tid
      if (tid > best) best = tid
    }
    return best
  }

  hasLive(turnID: number): boolean {
    return Boolean(this.slots.get(turnID)?.live)
  }

  /** slot 结构状态（不含 live 流式字段）—— 供缓存 key 用。 */
  private renderKey(): string {
    return `${this.turnIDs.join(',')}|${this.legacy.length}|${this.pendingUsers.length}`
  }

  private listeners = new Set<() => void>()

  /** 订阅 store 变化（含 live 更新）。返回取消函数。箭头函数绑定 this ——
   *  React useSyncExternalStore 独立调用 subscribe（解引用），普通方法会丢
   *  this（this.listeners undefined 崩溃）。 */
  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn)
    return () => {
      this.listeners.delete(fn)
    }
  }

  private notify(): void {
    for (const fn of this.listeners) fn()
  }

  private invalidate(): void {
    this.cache = null
    this.notify()
  }

  /** 把早于 turnID 的未提交 live commit（旧 turn text 事件丢失的兜底）。 */
  private commitStaleLives(newTurnID: number): void {
    for (const tid of this.turnIDs) {
      if (tid >= newTurnID) break
      const slot = this.slots.get(tid)
      if (slot?.live && !slot.live.frozen) {
        const live = slot.live
        slot.assistant = {
          id: `seq-${live.eventSeq || 0}-stale`,
          role: 'assistant',
          content: live.content || '',
          iterations: live.iterations,
          timestamp: new Date().toISOString(),
          isPartial: false,
          turnID: tid,
          persisted: false,
        }
        slot.live = undefined
      }
    }
  }

  private insertTurnID(turnID: number): void {
    if (this.turnIDs.includes(turnID)) return
    this.turnIDs.push(turnID)
    this.turnIDs.sort((a, b) => a - b)
  }
}
