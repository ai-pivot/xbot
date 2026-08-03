# Web Frontend Linearizability — Formal Proof

Scope: the Agent panel message list (`useChatMessages` + `useProgressStream` +
`progressStore` + `MessageList.buildMessageRows`).

## Model

State `S = (M, P, L)`:

- `M` — committed message array (React state, updated only via `setMessages`
  functional updaters, which React applies in call order with `prev` always
  being the latest committed value).
- `P` — progressStore snapshot (read via `useSyncExternalStore`; updated by
  `mutate` (rAF-throttled) or `reset()`/`freeze()` (synchronous flush)).
- `L` — liveMessage, a pure `useMemo` derivation `L = f(P)` (no side effects).

Rendering: `Rows = BuildMessageRows(M, L)` — a pure function evaluated with
the (M, L) pair of the current render.

## Operations & linearization points

| Operation | Implementation | Linearization point | Atomic |
|---|---|---|---|
| SendMessage (optimistic user) | `setMessages(prev => [...prev, newMsg])` | updater application | ✓ |
| EchoUser (REST response) | `setMessages(prev => map(...))` | updater application | ✓ |
| BindUserToTurn | `setMessages(prev => map(...))` | updater application | ✓ |
| AppendAssistant + reset | `flushSync(() => { appendAssistant(); resetProgress() })` (AgentPanel) | the flushSync render | ✓ M+P same batch |
| ProgressEvent (stream) | `store.mutate(...)` → rAF `flush()` | next-frame flush | ✓ snapshot atomic |
| Cancel freeze | `store.freeze()` synchronous flush | immediately | ✓ |
| Reload | async fetch → `setMessages(reconcile(...))` | setMessages | ✓ (loading gate) |

## Invariants

- **I1 (M order)**: M's accumulation order == backend production order.
  - committed rows: append-only arrival order (mirrors append-only DB ids,
    Lemma A in AGENTS.md) + `insertBeforeLastUser` inserts a late-committed
    assistant before the LAST user (deterministic — the newest user is the one
    that triggered the commit).
  - reload: `reconcileHistoryWithLiveRows` is a pure function (history in DB
    order + surviving live rows at the end); applied atomically.
- **I2 (L derivation)**: `L = f(P)` pure; `turnID = P.turnID || P.lastTurnID`.
- **I3 (render atomicity)**: every render reads the same-batch (M, L).
- **I4 (turn-boundary atomicity)**: M commit + P reset are in one `flushSync`;
  `reset()`/`freeze()` synchronously update P and cancel the pending rAF.
- **I5 (BuildMessageRows determinism)**: rows = pure `(M, L)` — array
  accumulation order; a live row with `turnID>0` and no same-turn committed
  assistant is inserted after the last row with `turnID <= live.turnID`
  (frozen/cancelled rows land above the newest user — no flicker).

## Lemmas

- **L1 (batching)**: `createRoot` (React 18/19) auto-batches all state updates
  in one event-loop turn — across hooks — into a single atomic render.
- **L2 (sync flush)**: `reset()`/`freeze()` update the snapshot synchronously
  and cancel the pending rAF; `useSyncExternalStore` re-reads in the same turn.
- **L3 (pure functions)**: appendAssistant insert rules, BuildMessageRows and
  reconcileHistoryWithLiveRows are pure and deterministic.
- **L4 (ordered updaters)**: functional updaters apply in call order.

## Theorems

- **T1 (render linearizability)**: by I3 + L1, every render is one atomic
  linearized snapshot of (M, L) — no intermediate state is ever observed.
- **T2 (no flicker at turn boundary)**: by I4 + L2, the committed message and
  the live-clear land in the same render; a cancelled turn's frozen content is
  positioned above the newest user (I5) — never one frame below.
- **T3 (order correctness)**: by I1 + I5, rendered order == backend order —
  turns never interleave, user rows keep their natural order.
- **T4 (reload consistency)**: reconcile is pure; loading gates live rendering
  during fetch; the swap is one atomic setMessages.

## Boundary conditions (explicit)

- **Stream content (L) is rAF-throttled**: ordinary ProgressEvents update P at
  the next frame. This is inherent to streaming (one snapshot per frame); each
  frame's snapshot is internally consistent (T1). Turn boundaries never wait
  on rAF — they use flushSync/reset (L2).
- **`messagesRef.current`** may lag one batch behind the committed state until
  the updater runs; all read sites are inside updaters or after async
  boundaries where the updater has already applied.

## Conclusion

The Web frontend satisfies linearizability for the message history: every
render is an atomic snapshot, turn boundaries commit atomically, and ordering
is deterministic and backend-consistent. No known violations.
