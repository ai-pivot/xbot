# Design: Atomic Turn Lifecycle for Background Notifications

> Status: Proposal — solves the race between bg-notification-injected user messages and user-typed/queued messages on both TUI and Web.

## 1. Root Cause Analysis

### 1.1 The Two Delivery Channels

When a background notification is drained **after a turn completes** (`chatProcessLoop` → `drainAndProcessNotifications`), the backend delivers it through **two decoupled channels**:

```
drainAndProcessNotifications
  └─ injectBgUserMessage(channel, chatID, senderID, content)
       ├─ (A) injectCLIUserMessage  → channel.InjectUserMessage(qualifiedChatID, content)
       │       TUI: asyncCh <- cliInjectedUserMsg{content, chatID}     // caller's goroutine
       │       Web: WS  inject_user{content, chatID}                    // hub goroutine
       │
       └─ (B) injectInboundWithMetadata → bus.Inbound <- msg            // chatProcessLoop goroutine
              └─ chatProcessLoop picks it up → processMessage → Run() → progress events + reply
```

**Channel (A)** tells the frontend "display this user message."
**Channel (B)** tells the agent loop "process this message and respond."

These are **independent** — no atomic linkage, no shared identifier.

### 1.2 Why the Frontend Gets Confused

| Problem | Cause |
|---|---|
| **"搞错消息"** (wrong message) | `ProgressEvent` has **no `TurnID`**. Both frontends associate a user message with its agent response purely by **arrival order**. If a notification and a user-typed message arrive in quick succession, the frontend cannot tell which message the agent is responding to. |
| **Web can't distinguish notifications** | The `inject_user` WS message carries only `content` + `chatID` — **no `is_notification` flag**. The web displays it as an ordinary user-typed message. |
| **TUI reply/notification race** | The turn's reply goes through `sendMessage → directSend → msgChan → handleOutbound goroutine → asyncCh`. The notification display goes through `InjectUserMessage → direct asyncCh write` (caller's goroutine). Different goroutines → the notification can arrive in `asyncCh` **before** the reply of the just-completed turn. |
| **Fragile TUI guards** | `handleInjectedUserMsg` compensates with `shouldQueue`, `turnAutoStarted`, `replyProcessed` + 2s timeout. These are **patches** on top of the design flaw — each covers one symptom but adds new edge cases. |

### 1.3 The Three Delivery Paths to TUI asyncCh

All three write to `asyncCh` from **different goroutines**:

| Path | Message type | Goroutine |
|---|---|---|
| Reply | `cliOutboundMsg` | `handleOutbound` (reads `msgChan`) |
| Progress | `cliProgressMsg` | `handleProgressDrain` (reads `progressSlot`) |
| Injected user msg | `cliInjectedUserMsg` | caller of `InjectUserMessage` (direct write) |

Only the **read** side is single-goroutine (`handleAsyncDrain`). The **write** side races.

### 1.4 What's Missing

- `protocol.ProgressEvent` has **no `TurnID`** field.
- `channel.OutboundMsg` has **no `TurnID`** field.
- The web frontend uses `turnID: 0` everywhere (placeholder, never set).
- The TUI uses a **local** `agentTurnID` counter — not propagated to/from the backend, so it can't be used for cross-channel association.

---

## 2. Solution: Turn-ID + `turn_started` Event

### 2.1 Core Principle

> **Every turn — whether triggered by a user-typed message, a bg-notification, a cron trigger, or a resume — carries a unique `TurnID` assigned by the backend. The frontend matches messages to responses by `TurnID`, never by arrival order.**

The `TurnID` is the **single source of truth** for turn association. It flows through:
- The `turn_started` progress event (announces the turn + carries the user message for notification display)
- Every `ProgressEvent` (streaming, tool, PhaseDone)
- The final reply (`OutboundMsg` / `text` event)
- The `user_echo` event (links optimistic display to the TurnID)

### 2.2 Eliminate the Side Channel

**Replace `InjectUserMessage` with a `turn_started` progress event.**

Instead of `injectCLIUserMessage` (direct asyncCh write / WS `inject_user`), the notification user message is delivered through the **unified progress stream** — the same channel as all other progress events. This guarantees:

1. **Ordering within a turn**: `turn_started` is emitted before `processMessage` runs, so it arrives before any progress/response of the same turn.
2. **No cross-goroutine race with the reply**: the notification display no longer competes with the previous turn's reply on a separate goroutine.
3. **Atomic display + association**: the `turn_started` event carries both the user message content AND the `TurnID` in one atomic payload.

### 2.3 High-Level Flow

```
chatProcessLoop dequeues msg
  │
  ├─ 1. Generate TurnID (per-session monotonic counter)
  ├─ 2. Emit turn_started progress event {TurnID, trigger, content, requestID}
  │     └─ frontend: display user msg (notification) or confirm (user-typed) + tag with TurnID
  │
  ├─ 3. processMessage(ctx, msg) → Run()
  │     └─ every ProgressEvent carries TurnID
  │     └─ PhaseDone carries TurnID
  │
  └─ 4. sendMessage(reply) → OutboundMsg carries TurnID
        └─ frontend: apply reply to the streaming message matched by TurnID
```

---

## 3. Backend Changes

### 3.1 Protocol: Add `TurnID` + `TurnStart` to `ProgressEvent`

```go
// protocol/events.go
type ProgressEvent struct {
    // ... existing fields ...
    TurnID    uint64         `json:"turn_id,omitempty"`
    TurnStart *TurnStartInfo `json:"turn_start,omitempty"` // only on turn_started events
}

// TurnStartInfo carries the user message that triggered this turn.
// Only set when Phase == "turn_started".
type TurnStartInfo struct {
    Trigger    string `json:"trigger"`               // "user" | "notification" | "resume"
    Content    string `json:"content,omitempty"`     // user message text (for notification display)
    RequestID  string `json:"request_id,omitempty"`   // for user-typed: match optimistic message
    SenderName string `json:"sender_name,omitempty"`
}
```

### 3.2 OutboundMsg: Add `TurnID`

```go
// channel/channel.go (or wherever OutboundMsg is defined)
type OutboundMsg struct {
    // ... existing fields ...
    TurnID uint64
}
```

`sendMessage` reads `TurnID` from the active turn context and sets it on the `OutboundMsg`.

### 3.3 TurnID Generation

Add a per-session monotonic counter to `bgSessionState`:

```go
type bgSessionState struct {
    // ... existing fields ...
    turnIDSeq uint64  // atomic increment per turn
}
```

`chatProcessLoop` generates the TurnID when dequeuing a message:

```go
func (a *Agent) chatProcessLoop(...) {
    for msg := range ch {
        // Generate TurnID for this turn
        turnID := a.nextTurnID(chatKey)  // atomic Add on bgSessionState.turnIDSeq

        // Emit turn_started BEFORE processMessage
        a.emitTurnStarted(msg, turnID)

        // Pass TurnID to processMessage via metadata
        if msg.Metadata == nil { msg.Metadata = map[string]string{} }
        msg.Metadata["turn_id"] = strconv.FormatUint(turnID, 10)

        response, err = a.processMessage(reqCtx, msg)  // reads turn_id from metadata
        ...
    }
}
```

### 3.4 Emit `turn_started`

```go
func (a *Agent) emitTurnStarted(msg bus.InboundMessage, turnID uint64) {
    trigger := "user"
    content := ""
    if msg.Metadata[bgNotificationMetadataKey] == "true" {
        trigger = "notification"
        content = msg.Content
    } else if msg.Metadata["resume_turn"] == "true" {
        trigger = "resume"
    }
    // For user-typed: content is empty (frontend already has the optimistic message);
    //   match by requestID.

    a.sendProgress(msg.Channel, msg.ChatID, &protocol.ProgressEvent{
        TurnID: turnID,
        Phase:  "turn_started",
        TurnStart: &protocol.TurnStartInfo{
            Trigger:   trigger,
            Content:   content,
            RequestID: msg.RequestID,
            SenderName: msg.SenderName,
        },
    })
}
```

This goes through the **same `SendProgress` path** as all other progress events → guaranteed to arrive before the turn's subsequent progress events.

### 3.5 Propagate TurnID Through the Run

`processMessage` reads `turn_id` from `msg.Metadata` and sets it on `RunConfig`:

```go
type RunConfig struct {
    // ... existing fields ...
    TurnID uint64
}
```

`runState.initProgress()` stores it; every `ProgressEvent` built by `notifyProgress` / `recordIterationSnapshot` / `broadcastProgress` carries `s.cfg.TurnID`.

`PhaseDone` (the final progress event) also carries `TurnID`.

### 3.6 Remove the Side Channel

```go
// BEFORE
func (a *Agent) injectBgUserMessage(channelName, chatID, senderID, content string) {
    a.injectCLIUserMessage(channelName, chatID, content)  // ← REMOVE
    a.injectInboundWithMetadata(channelName, chatID, senderID, content, map[string]string{
        bgNotificationMetadataKey: "true",
    })
}

// AFTER
func (a *Agent) injectBgUserMessage(channelName, chatID, senderID, content string) {
    // Display is handled by turn_started event in chatProcessLoop.
    a.injectInboundWithMetadata(channelName, chatID, senderID, content, map[string]string{
        bgNotificationMetadataKey: "true",
    })
}
```

`injectCLIUserMessage` and `InjectUserMessage` interface can be **kept for backward compatibility** (SubAgent path, etc.) or removed if fully unused after migration. The **idle-path notification** no longer uses them.

### 3.7 Cancel Path

`handleCancelledRun` already persists same-session notifications as synthetic tool messages (DisplayOnly). The cancel ack (`sendMessage` with `cancelled: "true"`) carries the TurnID, so the frontend associates it correctly.

### 3.8 Busy-Path Notifications (Unchanged)

During an active `Run`, `drainAndInjectBgNotifications` injects notifications as **synthetic tool calls** (not user messages). These don't start a new turn and already flow through normal progress events. **No change needed.** The `TurnID` on these events is the current turn's TurnID (inherited from `runState`).

---

## 4. TUI Changes

### 4.1 Handle `turn_started` (replaces `handleInjectedUserMsg`)

```go
// cli_update.go — new case in Update()
case turnStartedEvent:  // extracted from cliProgressMsg with Phase=="turn_started"
    return m.handleTurnStarted(ev)
```

```go
func (m *cliModel) handleTurnStarted(ev *protocol.ProgressEvent) []tea.Cmd {
    turnID := ev.TurnID
    ts := ev.TurnStart

    // ── Guard: don't start turn N+1 until turn N is finalized ──
    // If the previous turn's reply hasn't arrived yet (cross-goroutine race),
    // finalize it as-is (streamed content is already visible) then proceed.
    if m.typing && m.streamingMsgIdx >= 0 {
        streaming := &m.messages[m.streamingMsgIdx]
        if streaming.turnID != 0 && streaming.turnID != turnID {
            // Previous turn's reply is still pending — finalize it without reply text.
            // The streamed content + iterations are already rendered.
            m.endAgentTurn(streaming.turnID)
        }
    }

    // ── Display the user message ──
    if ts.Trigger == "notification" || ts.Trigger == "resume" {
        // Notification / resume: display the user message (with notification badge)
        m.messages = append(m.messages, cliMessage{
            role:      "user",
            content:   ts.Content,
            timestamp: time.Now(),
            dirty:     true,
            turnID:    turnID,
            isNotification: ts.Trigger == "notification",
        })
    }
    // For trigger=="user": the message was already displayed optimistically in sendMessage.
    //   Just adopt the backend TurnID.

    // ── Adopt backend TurnID ──
    m.agentTurnID = turnID
    m.startStreamingForTurn(turnID)  // create empty streaming assistant with turnID
    return tickCmd()
}
```

### 4.2 Simplify `handleAgentMessage` (reply) — match by TurnID

```go
func (m *cliModel) handleAgentMessage(msg cliOutboundMsg) []tea.Cmd {
    turnID := msg.msg.TurnID  // from OutboundMsg

    // Find the streaming message for this turn (by TurnID, not by streamingMsgIdx)
    idx := m.findStreamingByTurnID(turnID)
    if idx < 0 {
        // Turn not found (e.g. already finalized by a newer turn_started) — skip
        return nil
    }
    // Apply reply content + iterations to messages[idx]
    ...
    m.endAgentTurn(turnID)
}
```

### 4.3 Remove Old Race Guards

The following become **dead code** and are removed:
- `shouldQueue` / `turnAutoStarted` / `m.replyProcessed` in `handleInjectedUserMsg` — replaced by `turn_started` + TurnID matching.
- `insertUserMessageBeforeStreaming` — the user message is now displayed by `handleTurnStarted`.
- The `cliInjectedUserMsg` path for idle notifications — no longer emitted.

> **Note**: `InjectUserMessage` is still called for non-idle paths (e.g. SubAgent `wirePendingMessageDrain`). Those paths keep working — they inject into the Run loop as synthetic tool results, not as user messages.

### 4.4 Optimistic Display for User-Typed Messages (unchanged)

When the user types and presses Enter:
1. `sendMessage` → optimistic user message appended (turnID=0, requestID=R)
2. `startAgentTurn()` → streaming assistant with turnID=0 (pending)
3. `turn_started{TurnID:N, trigger:"user", requestID:R}` arrives → tag both messages with turnID=N, set `m.agentTurnID=N`

No flicker — the optimistic message and streaming slot already exist; `turn_started` just stamps the TurnID.

---

## 5. Web Changes

### 5.1 Handle `turn_started` in `useProgressStream`

```ts
case 'turn_started': {  // progress event with phase === 'turn_started'
    const ts = p.turn_start
    if (ts?.trigger === 'notification' || ts?.trigger === 'resume') {
        // Display the notification user message with a 🔔 badge
        onInjectUserMessage?.(ts.content, p.turn_id, ts.trigger === 'notification')
    } else if (ts?.trigger === 'user') {
        // Link optimistic message to TurnID (by requestID)
        onLinkTurnID?.(ts.request_id, p.turn_id)
    }
    return
}
```

### 5.2 Tag Messages with TurnID

`ChatMessage` gains a `turnID` field. When `turn_started` arrives:
- **Notification**: append a new user message with `turnID=N`, `isNotification=true` (renders a 🔔 badge + muted styling).
- **User-typed**: find the optimistic message by `requestID`, set its `turnID=N`.

### 5.3 Associate Response by TurnID

`appendAssistant` tags the assistant message with the TurnID from the `text` event / PhaseDone:

```ts
const appendAssistant = (content, iterations, eventSeq, turnID) => {
    // ... existing logic ...
    const newMsg: ChatMessage = { ..., turnID }
}
```

The `text` event carries `turn_id` (from `OutboundMsg.TurnID`). The assistant message is linked to the user message with the same `turnID`.

### 5.4 Remove `inject_user` Handler

The `useEffect` that listens for `inject_user` WS messages is **removed** — notifications now arrive via `turn_started` in the progress stream. `user_echo` (for attachment expansion) is retained but also gains `turn_id`.

### 5.5 Visual Distinction

Notification user messages render with:
- A 🔔 icon prefix (or a "Notification" label)
- Muted/secondary text color (vs. primary for user-typed)
- No "sending" state (already delivered)

---

## 6. Race Analysis — Proof of Correctness

### 6.1 Scenario: Notification + concurrent user message (the original bug)

```
Timeline:
  T1: chatProcessLoop finishes turn 1, sends reply(TurnID=1) via sendMessage
  T2: chatProcessLoop calls drainAndProcessNotifications → injectInbound(notification)
  T3: User types message → web sendMessage → optimistic(msg, reqID=R) + WS message
  T4: chatProcessLoop dequeues notification → emit turn_started(TurnID=2, notification)
  T5: chatProcessLoop processes notification → progress(TurnID=2) → reply(TurnID=2)
  T6: chatProcessLoop dequeues user msg → emit turn_started(TurnID=3, user, reqID=R)
  T7: chatProcessLoop processes user msg → progress(TurnID=3) → reply(TurnID=3)
```

**Frontend state (web):**
- reply(1) → assistant msg tagged TurnID=1
- turn_started(2, notification) → user msg "🔔 notification" tagged TurnID=2
- optimistic(msg, reqID=R) → user msg tagged TurnID=0 (pending)
- progress(TurnID=2) → associates with TurnID=2 ✓
- reply(2) → assistant msg tagged TurnID=2 ✓
- turn_started(3, user, reqID=R) → tag optimistic msg with TurnID=3
- progress(TurnID=3) → associates with TurnID=3 ✓
- reply(3) → assistant msg tagged TurnID=3 ✓

**No confusion.** Each message and response is linked by TurnID regardless of arrival order.

### 6.2 Scenario: `turn_started` arrives before previous reply (TUI cross-goroutine race)

```
chatProcessLoop sends reply(TurnID=1) → handleOutbound goroutine (slow)
chatProcessLoop emits turn_started(TurnID=2) → handleProgressDrain goroutine (fast)
```

TUI receives `turn_started(2)` while turn 1's streaming message (turnID=1) has no reply yet:
- `handleTurnStarted` detects `streaming.turnID(1) != turnID(2)` → finalizes turn 1 as-is (streamed content already visible) → starts turn 2.
- Later, reply(1) arrives → `findStreamingByTurnID(1)` returns -1 (turn 1 already finalized) → **no-op** (content was already streamed; reply text matches streamed content).

**No data loss, no wrong association.** The streamed content was already rendered; the reply is a no-op confirmation.

### 6.3 Scenario: Reply dropped (asyncCh full)

```
reply(TurnID=1) → asyncCh full → dropped by handleOutbound
turn_started(TurnID=2) → arrives
```

- TUI: `handleTurnStarted(2)` finalizes turn 1 (streamed content visible) → starts turn 2.
- Web: PhaseDone(TurnID=1) may still arrive → `appendAssistant` with TurnID=1 (defensive finalize). If also dropped → `session(idle)` triggers defensive finalize.

**Graceful degradation** — streamed content is never lost; only the final "commit" signal may be delayed.

### 6.4 Scenario: Cancel mid-turn

```
turn_started(TurnID=1) → progress(TurnID=1) → Ctrl+C → cancel ack(TurnID=1, cancelled)
```

- Cancel ack carries TurnID=1 → frontend finalizes turn 1's streaming message with the cancel marker.
- `handleCancelledRun` persists same-session notifications as DisplayOnly synthetic tools.
- Next `turn_started(TurnID=2)` starts cleanly.

### 6.5 Invariant

> **For every TurnID N, there is at most one user message and at most one assistant response. The frontend never associates a response with the wrong user message, because association is by TurnID, not by position.**

---

## 7. Implementation Plan

### Phase 1: TurnID propagation (backend + minimal frontend)
1. Add `TurnID` to `protocol.ProgressEvent` + `TurnStartInfo` type.
2. Add `TurnID` to `channel.OutboundMsg`; set in `sendMessage`.
3. Add per-session TurnID counter in `bgSessionState`; generate in `chatProcessLoop`.
4. Emit `turn_started` event before `processMessage`.
5. Propagate TurnID through `RunConfig` → `runState` → all progress events.

### Phase 2: TUI migration
6. Add `handleTurnStarted`; match streaming messages by TurnID in `handleAgentMessage`.
7. Remove `shouldQueue` / `turnAutoStarted` / `replyProcessed` guards.
8. Add notification badge rendering for notification user messages.

### Phase 3: Web migration
9. Handle `turn_started` in `useProgressStream`; tag messages with TurnID.
10. Remove `inject_user` WS handler.
11. Add `turn_id` to `text` / `user_echo` WS messages.
12. Add notification badge + muted styling.

### Phase 4: Cleanup
13. Remove `InjectUserMessage` calls from idle notification path (keep for SubAgent if still used).
14. Update knowledge files (`docs/agent/agent.md`, `AGENTS.md` gotchas).

### Phase 5: Tests
15. Backend: unit test `turn_started` emission, TurnID monotonicity, cancel path.
16. TUI: test `handleTurnStarted` with out-of-order arrival (reply before/after turn_started).
17. Web: E2E (Playwright) test notification display + concurrent user message.

---

## 8. Testing Strategy

### Reproduction test (must fail before fix, pass after)

**TUI** (`cli_progress_test.go`):
```go
func TestNotificationDoesNotClobberUserMessage(t *testing.T) {
    // 1. Start turn 1 (user types)
    // 2. Turn 1 completes → reply(TurnID=1) sent
    // 3. drainAndProcessNotifications → injectInbound(notification)
    // 4. Simultaneously: user types another message (queued)
    // 5. turn_started(TurnID=2, notification) arrives
    // 6. turn_started(TurnID=3, user) arrives
    // Assert: no message confusion — turn 2's response is linked to the notification,
    //         turn 3's response is linked to the user's message.
}
```

**Web** (Playwright E2E):
```ts
test('notification + concurrent user message — no confusion', async ({ page }) => {
    // Mock SSE: emit turn_started(notification) + turn_started(user) interleaved
    // Assert: notification message has 🔔 badge, user message does not
    // Assert: each assistant response is under the correct user message
})
```

### Property tests
- TurnID is monotonically increasing per session.
- Every ProgressEvent within a turn carries the same TurnID.
- `turn_started` always precedes other events of the same turn (same channel → ordered).
