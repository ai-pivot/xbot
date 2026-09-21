# Web Frontend Linearizability — Formal Proof (Raft Model)

Scope: the Agent panel message list (`useChatMessages` + `useProgressStream` +
`progressStore` + `MessageStore` + `messageOrder` + `sseConnection`).

Architecture follows the Raft model (`web_hub.go:276`):
**AppendEntries (push) is best-effort, InstallSnapshot (pull) is authoritative.**

## Model

Client state `S = (M, P, L, W)`:

- `M` — `MessageStore` committed state: `Map<turnID, TurnSlot{user?, assistant?, live?}>`.
  Updated only via store methods; rendered via `toRows()` (pure, sorted by turnID).
- `P` — `ProgressStore` snapshot (read via `useSyncExternalStore`; updated by
  `mutate` (rAF-throttled) or `reset()`/`freeze()`/`resetAndReplace` (sync flush)).
- `L` — liveMessage, pure derivation `L = f(P)` (no side effects).
- `W` — watermark pair `(w_sse, w_prog)`: `w_sse = lastSeqCache[route]` (SSE
  envelope seq, per-route), `w_prog = progressStore.eventSeq` (`ProgressEvent.Seq`,
  per-Run). Two INDEPENDENT monotonic sequences — must never be cross-compared.
  Both the `w_sse` cache (`lastSeqCache`) and the reconnect snapshot cache are
  LRU-bounded to 8 sessions (`webCache.ts`, `MAX_CACHED_SESSIONS`); evicting an
  idle route only costs a cold re-connect (no `last_event_id` → one
  `restoreActiveProgress`), never the ACTIVE route (touched by every event).

Server authoritative state:

- **Log** — `eventStream` ring (per-route, 512 entries, monotonic envelope seq)
  + `session_messages` (DB, append-only, monotonic turn_id).
- **Snapshot** — `lastProgressSnapshot` + `iterationHistories` (in-memory);
  `get_history` (DB) via REST.

Rendering: `Rows = orderMessageRows(bindTurnIDs(toRows(M, L)))` — pure.

## Operations & linearization points

| Operation | Implementation | Linearization point | Atomic |
|---|---|---|---|
| SendMessage (optimistic user) | `store.setUser(0, msg)` | store method call | ✓ |
| EchoUser (user_echo, turn_id) | `store.setUser(turnID, msg)` / `patchUserById` | store method call | ✓ |
| BindUserToTurn | `beginTurn(turnID)` → `bindUser` | store method call | ✓ |
| AppendAssistant + reset | `flushSync(() => { appendAssistant(); resetProgress() })` (AgentPanel) | the flushSync render | ✓ M+P same batch |
| ProgressEvent (stream) | `store.mutate(...)` → rAF `flush()` | next-frame flush | ✓ snapshot atomic |
| Cancel freeze | `store.freeze()` sync flush + `messageStore.freeze(turnID)` | immediately | ✓ |
| Reload (snapshot install) | async fetch → `store.mergeHistory(rows, {replace})` + hydration | setMessages | ✓ (loading gate) |
| SSE replay (catch-up) | `handleEvent` → seq dedup + apply; `restoreActiveProgress` on gap | per-event apply | ✓ per event |
| turn_started (new turn) | `messageStore.beginTurn(turnID)` + guards | store method call | ✓ |

## Invariants

- **I1 (M order)**: `toRows()` outputs slots in monotonic turnID order
  (`turnIDs` array maintained incrementally, sorted on insert); within a turn
  user (role 0) precedes assistant (role 1) — enforced by `orderMessageRows`
  `(turnID, roleRank)` stable sort.
- **I2 (L derivation)**: `L = f(P)` pure; `turnID = P.turnID || (frozen ? store.lastTurnID : 0)`.
- **I3 (render atomicity)**: every render reads the same-batch (M, L).
- **I4 (turn-boundary atomicity)**: M commit + P reset are in one `flushSync`;
  `reset()`/`freeze()` synchronously update P and cancel the pending rAF.
- **I5 (slot uniqueness)**: per turnID ≤ 1 user + ≤ 1 assistant (Map structure);
  `commitAssistant` migrates live→assistant in place (no second row).
- **I6 (watermark monotonicity)**: `w_sse` strictly increases per-route (server
  assigns envelope seq once per pushed event; client applies in order);
  `w_prog` strictly increases within a Run (per-Run counter, `engine_wire.go:456`).
- **I7 (snapshot authority)**: after a successful reload, M is a superset of the
  DB snapshot (replace semantics delete DB-absent completed turns, preserve
  in-flight live); P is hydrated from `active_progress` when turn matches.

## Lemmas

- **L1 (batching)**: `createRoot` auto-batches all state updates in one
  event-loop turn across hooks into a single atomic render.
- **L2 (sync flush)**: `reset()`/`freeze()` update the snapshot synchronously
  and cancel the pending rAF; `useSyncExternalStore` re-reads in the same turn.
- **L3 (pure functions)**: `toRows`, `bindTurnIDs`, `orderMessageRows`,
  `mergeHistory`, `reconcileHistoryWithLiveRows` are pure and deterministic.
- **L4 (ordered updaters)**: functional updaters apply in call order.
- **L5 (log order)**: DB `session_messages` ids are append-only monotonic;
  `turn_id` is allocated at queue-admission (`admitToMsgCh`) and restored on
  restart (`restoreTurnIDSeq`) — turn_id is globally monotonic per session.
- **L6 (replay completeness)**: `eventStream.replayAfter(fromSeq)` returns every
  retained stateful event with `seq > fromSeq`; ring eviction raises
  `evictedThrough` and forces `resync_required` (never silent loss).
- **L7 (best-effort push)**: `deliverToSubscribers` drops to a full `sendCh`
  ONLY after the authoritative server state (`lastProgressSnapshot` +
  `iterationHistories`) is already updated — a gap is always recoverable by
  pull (`restoreActiveProgress` / reload).

## Theorems

- **T1 (render linearizability)**: by I3 + L1, every render is one atomic
  linearized snapshot of (M, L) — no intermediate state is ever observed.
- **T2 (no flicker at turn boundary)**: by I4 + L2, committed message and
  live-clear land in the same render; a cancelled turn's frozen content is
  positioned above the newest user (I5) — never one frame below.
- **T3 (order correctness)**: by I1 + L5, rendered order == backend order —
  turns never interleave, user rows keep their natural order.
- **T4 (reload consistency)**: mergeHistory(replace) is pure; the swap is one
  atomic `setMessages`; live rows survive (never gate live on loading).
- **T5 (catch-up convergence)**: by L6 + L7, after an SSE reconnect the client
  applies every replayed stateful event with `seq > w_sse` in order; if the
  ring evicted the cursor, `resync_required` forces a snapshot install (reload).
  Finite replay + at most one reload ⇒ `(M, P)` converge to server state.
- **T6 (gap repair)**: an iterationHistory internal gap (delta lost on the
  wire) is detected (`hasIterationGapNow`) and triggers `onIterationGap` →
  reload → hydration `store.replace(live)` (allowed while streaming when
  `hasIterationGapNow()`) repairs the missing iterations from the authoritative
  snapshot. One-shot via `iterationGapFiredRef`, re-armed when contiguous.
- **T7 (cross-turn isolation)**: all events route by turn_id to the correct
  slot (MessageStore) / pass `finalizedTurnID` + stale-watermark guards
  (ProgressStore); a late event from turn N never mutates turn N+1's state.

## Known violations (preconditions currently unmet — audit 2026-08)

The theorems above hold **given their preconditions**. The following are
violations of I6/I1 preconditions found in the audit (`docs/agent/web-consistency-design.md`):

- **V1 (P1-1) — `progressStore.replace()` raises `eventSeq`** (`progressStore.ts:940-942`):
  hydration `store.replace(live)` takes `eventSeq = max(...)`. `active_progress.seq`
  is a per-Run counter that can reach thousands; the NEXT Run restarts at 1.
  A stale snapshot + lost `turn_started` ⇒ `w_prog` exceeds all new-Run seqs ⇒
  every new event is dropped by the stale-watermark check (`setStructuredTools`
  `seq <= eventSeq`) ⇒ T5/T6 convergence fails (new turn never renders).
  **Fix**: align with `resetAndReplace()` (`:487-495`, already correct) — replace
  must NOT drive `w_prog`.
- **V2 (P1-2) — `bindUser` picks the LAST unpersisted pending user**
  (`messageStore.ts:143-155`): with two fast sends and a slow REST response,
  `turn_started(msg1)` binds msg2 into turn 1's slot — a transient I1 violation
  (user_echo / patchUserById eventually corrects, so T3 holds only eventually).
  **Fix**: `TurnStartInfo.RequestID` (`protocol/events.go:155`) exists — match it first.
- **V3 (P2-1) — `historyReady` gate drops events** (`useProgressStream.ts:781`):
  during the session-switch window, `writeLiveToMessageStore` returns early. If
  the turn starts after the fetch but before `historyReady=true` AND
  `active_progress` is null, the turn_started + early iterations are lost with
  no hydration fallback (T6 gap-repair does not cover a never-received delta).
  **Fix**: buffer-and-replay, or re-reload when the gate window overlapped a turn.
- **V4 (P2-2) — turn_started-loss fallback skips `beginTurn`**
  (`useProgressStream.ts:1283-1301`): the progress_structured turnID-change
  fallback updates `store.lastTurnID` but never calls `messageStore.beginTurn`,
  so the optimistic user stays `turnID=0` (renders at the bottom) until REST
  corrects it — a transient I1 ordering violation. **Fix**: call `beginTurn`
  in the fallback, mirroring the turn_started branch.

## Boundary conditions (explicit)

- **Stream content (L) is rAF-throttled**: ordinary ProgressEvents update P at
  the next frame. Each frame's snapshot is internally consistent (T1); turn
  boundaries never wait on rAF (L2).
- **`messagesRef.current`** may lag one batch behind until the updater runs; all
  read sites are inside updaters or after async boundaries where it has applied.
- **Two seq domains are NOT interchangeable** (I6): `w_sse` (envelope, per-route)
  vs `w_prog` (ProgressEvent.Seq, per-Run). Cross-comparing them corrupts both
  catch-up and stale-watermark logic.

## Conclusion

The Raft-modeled pipeline (log + snapshot + state machine) is sound: push is
best-effort (L7), pull is authoritative (L6), and the render layer is
linearizable (T1-T4) with catch-up convergence (T5-T7) under I6/I1. **The four
known violations V1-V4 are precondition breaks, not design flaws** — each has a
small, testable fix. Until V1-V2 land, the system guarantees *eventual*
consistency for the affected edge cases (fast double-send, session-switch race);
after all four, linearizability holds unconditionally.

## 迭代窗口一致性（新增不变量 I8）— 2026-09-21 P0

**I8**：一个 turn 的 `iterations` 必须是**单一窗口**（相邻 +1；同号重复不算 gap）；
`iterationsTruncated` 记录该窗口之前"未加载"的迭代数。

**为什么需要**：两侧权威都是**有界窗口**而非全量 —— 服务端 `BoundHistoryIterations`（60）与
客户端 `boundIterationTail`/`SNAPSHOT_ITERATION_LIMIT`（60）。状态机里还可能残留用户**切走
那一刻的窗口**（如 `[1..93]`）。把两个**不相邻**的窗口 union（旧 I4 append-only 语义）会造出
gap ⇒ 渲染层的线性一致性守卫 `continuousIterations` 在第一个 gap 处截断 ⇒ **最新窗口永久
不可见**（新迭代"出现即消失"、历史窗口不再前进），只有整页刷新（丢掉状态机旧窗口）才恢复。
DB 取证：turn 有 510 个连续迭代，前端渲染窗口停在第 93 个。

**规则**（`web/src/chat/reduce.ts` 的 `mergeIterationWindows`）：

- 并集**相接/重叠**（`[1..3] × [4]`、或一侧自身带缺号的 `[1,2,4] × [4]`）⇒ 原样并集
  （resume 竞态"lazy live 只有 `[4]` + DB `[1..3]`"的 union 语义**不变**；内部 gap 交给守卫 +
  下一次 reload 修复）。
- **完全不相邻**（同一 turn 的两个窗口）⇒ 保留**较新**的一侧（权威窗口），丢弃过期窗口；
  截断计数跟随窗口（`LiveSnapshot.iterationsTruncated` → `derive` 的 live 行 →
  AssistantMessage 的「更早的 N 个迭代未加载」）。
- `iteration` case 的 delta 合并**不适用**（小增量、含 gap 修复语义）。

**应用点（四处窗口合并）**：`history_replaced` 的 live 胜分支 / DB→live 升级（step 3）/
`ev.active` 快照 union（step 3.5）/ `mergeTurnData`。守护：`web/src/chat/p0-window-gap.test.ts`。

## 迭代完整性（不变量 I8，用户 2026-09-21 定稿）

> **「不能有任何 gap，任何 gap 都是破坏线性一致性」**

**I8**：一个 turn 的 `iteration_history` 必须**完整**（iteration 1..N 连续、无损）地到达客户端。
**任何"有界窗口/尾部截断"都是违规** —— 两侧各截一次会造出两个不相邻的集合，合并即产生 gap，
渲染层的连续前缀守卫只能在 gap 处截断（= 用户看到的「历史停在旧位置 / 中间迭代不见 /
新迭代出现即消失」，且**取不回来**）。

- **已删除的实现（禁止复活）**：服务端 `channel.BoundHistoryIterations` +
  `maxHistoryIterationsPerTurn=60`、`agent.maxActiveSnapshotIterations=60`；
  客户端 `normalize.ts` 的 `boundIterationTail` / `SNAPSHOT_ITERATION_LIMIT`。
- **体积/性能归渲染层**：`TurnBody` 迭代级窗口化（只挂载视口附近的块 + contain，代价与
  迭代数解耦）+ `MessageList` 虚拟行。不得以丢数据换体积。
- **「落后 ⇒ 整会话重载」**：`history_replaced` 检测权威迭代不完整（`incompleteTurnSig`：
  `fromN` / `gap@N`）⇒ `ChatState.resyncToken++`（**同一缺口形状只自增一次 ⇒ 无重载循环**）
  ⇒ `AgentPanel` `reload()` + loading 屏。这是"落后/不完整"的统一出口：**不拼接、不截断、
  不静默**。
- 完整下发后 `[1..93] ∪ [1..435] = [1..435]` ⇒ 连续、无损、最新可见（线性一致性成立）。
